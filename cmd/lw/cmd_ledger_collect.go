package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/ledger"
	"github.com/awepo-pro/lw/internal/vault"
)

var dayNoteFileNameRegex = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}\.md$`)

var collectStepHook func(step string) error

type candidateNote struct {
	path string
	name string
	date string
}

func isValidDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func findCandidates(root string) ([]candidateNote, error) {
	var candidates []candidateNote

	// 1. Vault root
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && dayNoteFileNameRegex.MatchString(e.Name()) {
			dateStr := strings.TrimSuffix(e.Name(), ".md")
			if isValidDate(dateStr) {
				candidates = append(candidates, candidateNote{
					path: filepath.Join(root, e.Name()),
					name: e.Name(),
					date: dateStr,
				})
			}
		}
	}

	// 2. Direct children of ledger/
	ledgerDir := filepath.Join(root, vault.LedgerDir)
	lEntries, err := os.ReadDir(ledgerDir)
	if err == nil {
		for _, e := range lEntries {
			if e.Type().IsRegular() && dayNoteFileNameRegex.MatchString(e.Name()) {
				dateStr := strings.TrimSuffix(e.Name(), ".md")
				if isValidDate(dateStr) {
					candidates = append(candidates, candidateNote{
						path: filepath.Join(ledgerDir, e.Name()),
						name: e.Name(),
						date: dateStr,
					})
				}
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].date != candidates[j].date {
			return candidates[i].date < candidates[j].date
		}
		return candidates[i].path < candidates[j].path
	})

	return candidates, nil
}

func collectDayNotes(w io.Writer, root string, today string, includeToday bool, dryRun bool) error {
	candidates, err := findCandidates(root)
	if err != nil {
		return err
	}

	var collectedCount, waitingCount, totalReceipts int

	for _, cand := range candidates {
		if cand.date > today {
			fmt.Fprintf(w, "skipped %s: future date\n", cand.name)
			continue
		}
		if cand.date == today && !includeToday {
			fmt.Fprintf(w, "skipped %s: today (use --include-today)\n", cand.name)
			continue
		}

		data, err := os.ReadFile(cand.path)
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(root, cand.path)
		parsed := ledger.ParseDayNote(relPath, cand.date, data)

		// 1. Empty after G0-G3 and with no image
		if len(parsed.Entries) == 0 && len(parsed.Issues) == 0 && len(parsed.Images) == 0 {
			fmt.Fprintf(w, "skipped %s: empty\n", cand.name)
			continue
		}

		// 2. Not readable
		if len(parsed.Issues) > 0 {
			for _, iss := range parsed.Issues {
				if iss.Line > 0 {
					fmt.Fprintf(w, "waiting %s: line %d: %s\n", cand.name, iss.Line, iss.Msg)
				} else {
					fmt.Fprintf(w, "waiting %s: %s\n", cand.name, iss.Msg)
				}
			}
			waitingCount++
			continue
		}
		if len(parsed.Entries) == 0 && len(parsed.Images) > 0 {
			fmt.Fprintf(w, "waiting %s: only a receipt image\n", cand.name)
			waitingCount++
			continue
		}

		// 3. Readable
		c, wt, r, err := processReadableCandidate(w, root, cand, parsed.Entries, parsed.Images, dryRun)
		if err != nil {
			return err
		}
		collectedCount += c
		waitingCount += wt
		totalReceipts += r
	}

	collectWord := "collected"
	if dryRun {
		collectWord = "would collect"
	}
	fmt.Fprintf(w, "%s %d day note(s), %d waiting\n", collectWord, collectedCount, waitingCount)
	if totalReceipts > 0 && !dryRun {
		fmt.Fprintln(w, "run lw ledger import to store the receipts so lw sync carries them")
	}

	return nil
}

func cmdLedgerCollect(args []string) error {
	fl := flag.NewFlagSet("ledger collect", flag.ContinueOnError)
	fl.SetOutput(os.Stderr)
	vaultPath := fl.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	dryRun := fl.Bool("dry-run", false, "report what would be collected without modifying files")
	includeToday := fl.Bool("include-today", false, "collect today's note as well")
	todayFlag := fl.String("today", "", "today's date (YYYY-MM-DD)")
	dayFlag := fl.String("day", "", "collect only candidate for specified day")
	lineFlag := fl.String("line", "", "override line items for specified day")

	positional, err := parseStorageFlags(fl, args)
	if err != nil {
		return &exitError{code: 2}
	}
	if len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "lw ledger collect: unexpected argument %q\n", positional[0])
		ledgerUsage(os.Stderr)
		return &exitError{code: 2}
	}

	today := time.Now().Format("2006-01-02")
	if *todayFlag != "" {
		if !isValidDate(*todayFlag) {
			fmt.Fprintf(os.Stderr, "lw ledger collect: invalid today %q (want YYYY-MM-DD)\n", *todayFlag)
			ledgerUsage(os.Stderr)
			return &exitError{code: 2}
		}
		today = *todayFlag
	}

	root, err := storageVaultRoot(*vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)

	candidates, err := findCandidates(root)
	if err != nil {
		return err
	}

	// Mode 2: --day D --line L
	if *dayFlag != "" || *lineFlag != "" {
		if *dayFlag == "" || *lineFlag == "" {
			fmt.Fprintln(os.Stderr, "lw ledger collect: --day and --line must be used together")
			ledgerUsage(os.Stderr)
			return &exitError{code: 2}
		}
		if strings.ContainsAny(*lineFlag, "\n\r") || strings.Contains(*lineFlag, "[[") {
			fmt.Fprintln(os.Stderr, "lw ledger collect: --line must not contain newlines or [[ links")
			return &exitError{code: 2}
		}
		if !isValidDate(*dayFlag) {
			fmt.Fprintf(os.Stderr, "lw ledger collect: invalid day %q (want YYYY-MM-DD)\n", *dayFlag)
			return &exitError{code: 2}
		}

		var targetCand *candidateNote
		for i := range candidates {
			if candidates[i].date == *dayFlag {
				targetCand = &candidates[i]
				break
			}
		}
		if targetCand == nil {
			fmt.Fprintf(os.Stderr, "lw ledger collect: candidate not found for day %s\n", *dayFlag)
			return &exitError{code: 2}
		}

		// L is parsed as the items of a G7 day line (amount-first items only)
		lineSynth := fmt.Sprintf("%s, %s", *dayFlag, *lineFlag)
		entries, issues, _, _ := ledger.ParseLoose(ledger.LooseFile, []byte(lineSynth))
		if len(issues) > 0 || len(entries) == 0 {
			if len(issues) > 0 {
				fmt.Fprintf(os.Stderr, "lw ledger collect: %s\n", issues[0].Msg)
			} else {
				fmt.Fprintln(os.Stderr, "lw ledger collect: invalid line items")
			}
			return &exitError{code: 2}
		}

		// Read candidate note for its images
		candData, err := os.ReadFile(targetCand.path)
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(root, targetCand.path)
		parsedNote := ledger.ParseDayNote(relPath, targetCand.date, candData)

		collected, waiting, receipts, err := processReadableCandidate(os.Stdout, root, *targetCand, entries, parsedNote.Images, *dryRun)
		if err != nil {
			return err
		}

		collectWord := "collected"
		if *dryRun {
			collectWord = "would collect"
		}
		fmt.Printf("%s %d day note(s), %d waiting\n", collectWord, collected, waiting)
		if receipts > 0 && !*dryRun {
			fmt.Println("run lw ledger import to store the receipts so lw sync carries them")
		}
		return nil
	}

	// Mode 1: batch collect
	return collectDayNotes(os.Stdout, root, today, *includeToday, *dryRun)
}

func findSymlinkElement(root string, relPath string) (string, bool) {
	relPath = filepath.ToSlash(relPath)
	parts := strings.Split(relPath, "/")
	cur := ""
	for i, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		full := filepath.Join(root, filepath.FromSlash(cur))
		fi, err := os.Lstat(full)
		if err == nil && (fi.Mode()&os.ModeSymlink != 0) {
			elem := cur
			if i < len(parts)-1 {
				elem += "/"
			} else {
				if targetFi, err := os.Stat(full); err == nil && targetFi.IsDir() {
					elem += "/"
				}
			}
			return elem, true
		}
	}
	return "", false
}

func processReadableCandidate(w io.Writer, root string, cand candidateNote, entries []ledger.Entry, images []string, dryRun bool) (collected int, waiting int, receipts int, err error) {
	// 3a0. Check for symlinks in archive path and receipt paths under vault
	archRel := fmt.Sprintf("ledger/%s/%s/%s.md", cand.date[:4], cand.date[5:7], cand.date)
	if symlinkElem, found := findSymlinkElement(root, archRel); found {
		fmt.Fprintf(w, "waiting %s: %s holds a symlink\n", cand.name, symlinkElem)
		return 0, 1, 0, nil
	}

	cleanRoot := filepath.Clean(root)
	for _, target := range images {
		baseName := filepath.Base(target)
		candsToTry := []string{
			filepath.Join(filepath.Dir(cand.path), target),
			filepath.Join(root, baseName),
			filepath.Join(root, vault.LedgerDir, baseName),
		}
		var resolvedSrc string
		for _, tryPath := range candsToTry {
			cleanTry := filepath.Clean(tryPath)
			if cleanTry != cleanRoot && !strings.HasPrefix(cleanTry, cleanRoot+string(filepath.Separator)) {
				continue
			}
			lfi, err := os.Lstat(cleanTry)
			if err != nil || lfi.Mode()&os.ModeSymlink != 0 || !lfi.Mode().IsRegular() {
				continue
			}
			relToRoot, err := filepath.Rel(root, cleanTry)
			if err != nil || strings.HasPrefix(filepath.ToSlash(relToRoot), "ledger/receipts/") {
				continue
			}
			resolvedSrc = cleanTry
			break
		}
		if resolvedSrc != "" {
			destRel := fmt.Sprintf("ledger/receipts/%s/%s/%s", cand.date[:4], cand.date[5:7], baseName)
			if symlinkElem, found := findSymlinkElement(root, destRel); found {
				fmt.Fprintf(w, "waiting %s: %s holds a symlink\n", cand.name, symlinkElem)
				return 0, 1, 0, nil
			}
		}
	}

	candData, err := os.ReadFile(cand.path)
	if err != nil {
		return 0, 0, 0, err
	}

	// 3a. Archive path check
	archPath := filepath.Join(root, archRel)

	archData, archErr := os.ReadFile(archPath)
	archExists := (archErr == nil)
	if archExists {
		if string(archData) != string(candData) {
			fmt.Fprintf(w, "waiting %s: the archive already holds a different %s.md\n", cand.name, cand.date)
			return 0, 1, 0, nil
		}
	}
	if collectStepHook != nil {
		if err := collectStepHook("archive-check"); err != nil {
			return 0, 0, 0, err
		}
	}

	// 3b. bookkeeping.md append check
	centralLine := ledger.CentralLine(cand.date, entries)
	bkPath := filepath.Join(root, ledger.LooseFile)
	var bkData []byte
	var bkHasSource bool

	if existingBk, err := os.ReadFile(bkPath); err == nil {
		bkData = existingBk
		bkEntries, _, _, _ := ledger.ParseLoose(ledger.LooseFile, existingBk)
		for _, e := range bkEntries {
			if e.Kind == ledger.KindDay && e.Source == cand.date {
				bkHasSource = true
				break
			}
		}
	}

	if !bkHasSource {
		var newBk []byte
		if len(bkData) > 0 {
			newBk = append(newBk, bkData...)
			if !strings.HasSuffix(string(bkData), "\n") {
				newBk = append(newBk, '\n')
			}
			newBk = append(newBk, []byte(centralLine+"\n")...)
		} else {
			newBk = []byte(centralLine + "\n")
		}

		if !dryRun {
			bkPerm := os.FileMode(0o644)
			if st, err := os.Stat(bkPath); err == nil {
				bkPerm = st.Mode().Perm()
			}

			tmpDir := filepath.Join(root, ".llmwiki", "tmp")
			if err := os.MkdirAll(tmpDir, 0o755); err != nil {
				return 0, 0, 0, err
			}
			tmpFile, err := os.CreateTemp(tmpDir, "bk-*.tmp")
			if err != nil {
				return 0, 0, 0, err
			}
			tmpName := tmpFile.Name()
			if err := tmpFile.Chmod(bkPerm); err != nil {
				tmpFile.Close()
				os.Remove(tmpName)
				return 0, 0, 0, err
			}
			if _, err := tmpFile.Write(newBk); err != nil {
				tmpFile.Close()
				os.Remove(tmpName)
				return 0, 0, 0, err
			}
			if err := tmpFile.Sync(); err != nil {
				tmpFile.Close()
				os.Remove(tmpName)
				return 0, 0, 0, err
			}
			if err := tmpFile.Close(); err != nil {
				os.Remove(tmpName)
				return 0, 0, 0, err
			}
			if err := os.Rename(tmpName, bkPath); err != nil {
				os.Remove(tmpName)
				return 0, 0, 0, err
			}
		}
	}
	if collectStepHook != nil {
		if err := collectStepHook("append-line"); err != nil {
			return 0, 0, 0, err
		}
	}

	// 3c. Move receipt images (R3: moves receipt images BEFORE it moves the note)
	receiptsMoved := 0

	for _, target := range images {
		// Resolve order:
		// 1. Relative to note's directory
		// 2. Base name at vault root
		// 3. Base name in ledger/
		baseName := filepath.Base(target)
		candsToTry := []string{
			filepath.Join(filepath.Dir(cand.path), target),
			filepath.Join(root, baseName),
			filepath.Join(root, vault.LedgerDir, baseName),
		}

		var resolvedSrc string
		for _, tryPath := range candsToTry {
			cleanTry := filepath.Clean(tryPath)
			// Safety: must be inside vault
			if cleanTry != cleanRoot && !strings.HasPrefix(cleanTry, cleanRoot+string(filepath.Separator)) {
				continue
			}
			lfi, err := os.Lstat(cleanTry)
			if err != nil {
				continue
			}
			if lfi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if !lfi.Mode().IsRegular() {
				continue
			}
			// Must not already be under ledger/receipts/
			relToRoot, err := filepath.Rel(root, cleanTry)
			if err != nil {
				continue
			}
			if strings.HasPrefix(filepath.ToSlash(relToRoot), "ledger/receipts/") {
				continue
			}
			resolvedSrc = cleanTry
			break
		}

		// Destination path
		destRel := fmt.Sprintf("ledger/receipts/%s/%s/%s", cand.date[:4], cand.date[5:7], baseName)
		destPath := filepath.Join(root, destRel)

		if resolvedSrc == "" {
			if _, serr := os.Stat(destPath); serr == nil {
				// Already moved in previous attempt
				receiptsMoved++
				continue
			}
			fmt.Fprintf(w, "no file for image %s\n", target)
			continue
		}

		srcBytes, err := os.ReadFile(resolvedSrc)
		if err != nil {
			return 0, 0, 0, err
		}

		destBytes, destErr := os.ReadFile(destPath)
		if destErr == nil {
			// Destination exists
			if string(srcBytes) == string(destBytes) {
				// Identical: remove source
				if !dryRun {
					os.Remove(resolvedSrc)
				}
				receiptsMoved++
			} else {
				// Different: leave it and print kept
				relSrc, _ := filepath.Rel(root, resolvedSrc)
				fmt.Fprintf(w, "kept %s: a different %s is already in ledger/receipts/%s/%s/\n",
					filepath.ToSlash(relSrc), baseName, cand.date[:4], cand.date[5:7])
			}
		} else {
			// Destination does not exist
			if !dryRun {
				if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
					return 0, 0, 0, err
				}
				if err := os.Rename(resolvedSrc, destPath); err != nil {
					if err := os.WriteFile(destPath, srcBytes, 0o644); err != nil {
						return 0, 0, 0, err
					}
					os.Remove(resolvedSrc)
				}
			}
			receiptsMoved++
		}
	}
	if collectStepHook != nil {
		if err := collectStepHook("move-images"); err != nil {
			return 0, 0, 0, err
		}
	}

	// 3d. Move note to archive path
	if !dryRun {
		if archExists {
			// Identical copy already in archive: remove source
			if err := os.Remove(cand.path); err != nil && !os.IsNotExist(err) {
				return 0, 0, 0, err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(archPath), 0o755); err != nil {
				return 0, 0, 0, err
			}
			if err := os.Rename(cand.path, archPath); err != nil {
				// If rename fails, copy and remove
				if err := os.WriteFile(archPath, candData, 0o644); err != nil {
					return 0, 0, 0, err
				}
				os.Remove(cand.path)
			}
		}
	}
	if collectStepHook != nil {
		if err := collectStepHook("move-note"); err != nil {
			return 0, 0, 0, err
		}
	}

	// 3e. Print collected line
	prefix := "collected"
	if dryRun {
		prefix = "would collect"
	}
	// Strip leading "<date>, " and trailing " [[<date>]]" from centralLine
	itemsStr := centralLine
	itemsStr = strings.TrimPrefix(itemsStr, cand.date+", ")
	linkIdx := strings.LastIndex(itemsStr, " [[")
	if linkIdx != -1 {
		itemsStr = itemsStr[:linkIdx]
	}

	receiptText := ""
	if receiptsMoved > 0 {
		receiptText = fmt.Sprintf(" (%d receipt(s))", receiptsMoved)
	}

	fmt.Fprintf(w, "%s %s: %s -> ledger/%s/%s/%s.md%s\n",
		prefix, cand.date, itemsStr, cand.date[:4], cand.date[5:7], cand.date, receiptText)

	return 1, 0, receiptsMoved, nil
}
