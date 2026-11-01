package htmlexport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/vault"
)

// Options specifies configuration for vault export.
type Options struct {
	Force bool
	Title string // site title, default "lw wiki"
	Now   time.Time
}

// Result reports the summary of an export run.
type Result struct {
	Pages, Raws, Assets int
	Dir                 string
}

// Export compiles the committed vault into a static, offline HTML website.
func Export(v *vault.Vault, outDir string, o Options) (Result, error) {
	if o.Title == "" {
		o.Title = "lw wiki"
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}

	absOutDir, err := filepath.Abs(outDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve outDir %s: %w", outDir, err)
	}

	if vaultRoot := v.Root(); vaultRoot != "" {
		vaultAbs, err := filepath.Abs(vaultRoot)
		if err != nil {
			return Result{}, fmt.Errorf("resolve vault root %s: %w", vaultRoot, err)
		}
		vaultReal, err := filepath.EvalSymlinks(vaultAbs)
		if err != nil {
			vaultReal = vaultAbs
		}

		outReal := evalExistingAncestor(absOutDir)

		if isSameOrSubdir(vaultReal, outReal) || isSameOrSubdir(vaultAbs, absOutDir) {
			return Result{}, fmt.Errorf("refusing to export into vault root or directory: %s", outDir)
		}
	}

	fi, err := os.Stat(absOutDir)
	if err == nil {
		if !fi.IsDir() {
			return Result{}, fmt.Errorf("output path %s exists and is not a directory", outDir)
		}
		entries, err := os.ReadDir(absOutDir)
		if err != nil {
			return Result{}, fmt.Errorf("read outDir %s: %w", outDir, err)
		}
		if len(entries) > 0 && !o.Force {
			return Result{}, fmt.Errorf("output directory %s exists and is not empty (use --force to overwrite)", outDir)
		}
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("stat outDir %s: %w", outDir, err)
	}

	if err := os.MkdirAll(absOutDir, 0755); err != nil {
		return Result{}, fmt.Errorf("create outDir %s: %w", absOutDir, err)
	}

	res := Result{Dir: absOutDir}

	if vaultRoot := v.Root(); vaultRoot != "" {
		assetSrcDir := filepath.Join(vaultRoot, "raw", "assets")
		if stat, err := os.Stat(assetSrcDir); err == nil && stat.IsDir() {
			err = filepath.Walk(assetSrcDir, func(p string, info os.FileInfo, walkErr error) error {
				if walkErr != nil || info.IsDir() {
					return walkErr
				}
				rel, err := filepath.Rel(vaultRoot, p)
				if err != nil {
					return err
				}
				dest := filepath.Join(absOutDir, rel)
				if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
					return err
				}
				if err := copyFileByteForByte(p, dest); err != nil {
					return err
				}
				res.Assets++
				return nil
			})
			if err != nil {
				return Result{}, fmt.Errorf("copy assets: %w", err)
			}
		}
	}

	exportDate := o.Now.Format("2006-01-02")

	type searchItem struct {
		Title   string   `json:"t"`
		Path    string   `json:"p"`
		Type    string   `json:"y"`
		Tags    []string `json:"g"`
		Snippet string   `json:"s"`
	}
	var searchItems []searchItem

	pages := v.Pages()
	graph := v.Graph()
	for _, p := range pages {
		currentDir := path.Dir(p.Path)
		bodyHTML, err := renderWikiBody(v, p, currentDir)
		if err != nil {
			return Result{}, fmt.Errorf("render %s: %w", p.Path, err)
		}

		title := p.FM.Title
		if title == "" {
			title = p.Path
		}

		rootRel := computeRootRel(currentDir)

		var sources []sourceItem
		for _, s := range p.FM.Sources {
			raw, ok := v.RawSource(s)
			label := s
			rawURL := ""
			if ok {
				if raw.Title != "" {
					label = raw.Title
				}
				targetHTML := strings.TrimSuffix(s, ".md") + ".html"
				rel, err := filepath.Rel(currentDir, targetHTML)
				if err != nil {
					rel = targetHTML
				}
				rawURL = filepath.ToSlash(rel)
			}
			sources = append(sources, sourceItem{
				Label: label,
				URL:   rawURL,
			})
		}

		bl := graph.Backlinks(p.Path)
		seenBL := make(map[string]bool)
		var backlinks []backlinkItem
		for _, ref := range bl {
			if seenBL[ref.From] {
				continue
			}
			seenBL[ref.From] = true
			fromPage, _ := v.Page(ref.From)
			blTitle := ref.From
			if fromPage != nil && fromPage.FM.Title != "" {
				blTitle = fromPage.FM.Title
			}
			fromHTML := strings.TrimSuffix(ref.From, ".md") + ".html"
			rel, err := filepath.Rel(currentDir, fromHTML)
			if err != nil {
				rel = fromHTML
			}
			backlinks = append(backlinks, backlinkItem{
				Title: blTitle,
				URL:   filepath.ToSlash(rel),
			})
		}
		sort.Slice(backlinks, func(i, j int) bool {
			if backlinks[i].Title != backlinks[j].Title {
				return backlinks[i].Title < backlinks[j].Title
			}
			return backlinks[i].URL < backlinks[j].URL
		})

		data := wikiPageData{
			Title:      title,
			SiteTitle:  o.Title,
			RootRel:    rootRel,
			Type:       string(p.FM.Type),
			Tags:       p.FM.Tags,
			Confidence: string(p.FM.Confidence),
			Contested:  p.FM.Contested,
			Updated:    p.FM.Updated.String(),
			BodyHTML:   template.HTML(bodyHTML),
			Sources:    sources,
			Backlinks:  backlinks,
			ExportDate: exportDate,
		}

		var buf bytes.Buffer
		if err := wikiPageTmpl.Execute(&buf, data); err != nil {
			return Result{}, fmt.Errorf("execute template for %s: %w", p.Path, err)
		}

		outPagePath := filepath.Join(absOutDir, strings.TrimSuffix(p.Path, ".md")+".html")
		if err := os.MkdirAll(filepath.Dir(outPagePath), 0755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(outPagePath, buf.Bytes(), 0644); err != nil {
			return Result{}, err
		}
		res.Pages++

		targetSiteRel := filepath.ToSlash(strings.TrimSuffix(p.Path, ".md") + ".html")
		tags := p.FM.Tags
		if tags == nil {
			tags = []string{}
		}
		snippet := stripMarkdownSnippet(p.Body)
		searchItems = append(searchItems, searchItem{
			Title:   title,
			Path:    targetSiteRel,
			Type:    string(p.FM.Type),
			Tags:    tags,
			Snippet: snippet,
		})
	}

	raws := v.RawSources()
	for _, r := range raws {
		currentDir := path.Dir(r.Path)
		bodyHTML, err := renderRawBody(v, r, currentDir)
		if err != nil {
			return Result{}, fmt.Errorf("render %s: %w", r.Path, err)
		}

		title := r.Title
		if title == "" {
			title = r.Path
		}

		rootRel := computeRootRel(currentDir)

		var citedBy []citedByItem
		for _, p := range pages {
			citesThis := false
			for _, s := range p.FM.Sources {
				if s == r.Path {
					citesThis = true
					break
				}
			}
			if !citesThis {
				for _, c := range cite.Scan(p.Body) {
					if c.Err == "" && c.Source == r.Path {
						citesThis = true
						break
					}
				}
			}
			if citesThis {
				pTitle := p.FM.Title
				if pTitle == "" {
					pTitle = p.Path
				}
				targetHTML := strings.TrimSuffix(p.Path, ".md") + ".html"
				rel, err := filepath.Rel(currentDir, targetHTML)
				if err != nil {
					rel = targetHTML
				}
				citedBy = append(citedBy, citedByItem{
					Title: pTitle,
					URL:   filepath.ToSlash(rel),
					Path:  p.Path,
				})
			}
		}
		sort.Slice(citedBy, func(i, j int) bool {
			if citedBy[i].Title != citedBy[j].Title {
				return citedBy[i].Title < citedBy[j].Title
			}
			return citedBy[i].Path < citedBy[j].Path
		})

		isHTTP := strings.HasPrefix(r.SourceURL, "http://") || strings.HasPrefix(r.SourceURL, "https://")

		data := rawPageData{
			Title:      title,
			SiteTitle:  o.Title,
			RootRel:    rootRel,
			SourceURL:  r.SourceURL,
			IsHTTPURL:  isHTTP,
			Ingested:   r.Ingested.String(),
			CitedBy:    citedBy,
			BodyHTML:   template.HTML(bodyHTML),
			ExportDate: exportDate,
		}

		var buf bytes.Buffer
		if err := rawPageTmpl.Execute(&buf, data); err != nil {
			return Result{}, fmt.Errorf("execute raw template for %s: %w", r.Path, err)
		}

		outRawPath := filepath.Join(absOutDir, strings.TrimSuffix(r.Path, ".md")+".html")
		if err := os.MkdirAll(filepath.Dir(outRawPath), 0755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(outRawPath, buf.Bytes(), 0644); err != nil {
			return Result{}, err
		}
		res.Raws++
	}

	byType := make(map[string][]pageEntry)
	for _, p := range pages {
		t := string(p.FM.Type)
		if t == "" {
			t = "other"
		}
		title := p.FM.Title
		if title == "" {
			title = p.Path
		}
		targetHTML := strings.TrimSuffix(p.Path, ".md") + ".html"
		tags := p.FM.Tags
		if tags == nil {
			tags = []string{}
		}
		byType[t] = append(byType[t], pageEntry{
			Title: title,
			URL:   filepath.ToSlash(targetHTML),
			Tags:  tags,
			Path:  p.Path,
		})
	}
	for t := range byType {
		sort.Slice(byType[t], func(i, j int) bool {
			ti := strings.ToLower(byType[t][i].Title)
			tj := strings.ToLower(byType[t][j].Title)
			if ti != tj {
				return ti < tj
			}
			return byType[t][i].Path < byType[t][j].Path
		})
	}

	preferredOrder := []string{"entity", "concept", "comparison", "query", "summary"}
	seenTypes := make(map[string]bool)
	var groups []typeGroup
	for _, pt := range preferredOrder {
		if list, ok := byType[pt]; ok && len(list) > 0 {
			groups = append(groups, typeGroup{Type: pt, Pages: list})
			seenTypes[pt] = true
		}
	}
	var remainingTypes []string
	for t := range byType {
		if !seenTypes[t] {
			remainingTypes = append(remainingTypes, t)
		}
	}
	sort.Strings(remainingTypes)
	for _, t := range remainingTypes {
		groups = append(groups, typeGroup{Type: t, Pages: byType[t]})
	}

	var rawItems []rawItem
	for _, r := range raws {
		title := r.Title
		if title == "" {
			title = r.Path
		}
		targetHTML := strings.TrimSuffix(r.Path, ".md") + ".html"
		rawItems = append(rawItems, rawItem{
			Title: title,
			URL:   filepath.ToSlash(targetHTML),
			Path:  r.Path,
		})
	}
	sort.Slice(rawItems, func(i, j int) bool {
		ti := strings.ToLower(rawItems[i].Title)
		tj := strings.ToLower(rawItems[j].Title)
		if ti != tj {
			return ti < tj
		}
		return rawItems[i].Path < rawItems[j].Path
	})

	indexData := indexData{
		SiteTitle:  o.Title,
		PageCount:  len(pages),
		Groups:     groups,
		RawSources: rawItems,
		ExportDate: exportDate,
	}

	var indexBuf bytes.Buffer
	if err := indexTmpl.Execute(&indexBuf, indexData); err != nil {
		return Result{}, fmt.Errorf("execute index template: %w", err)
	}
	if err := os.WriteFile(filepath.Join(absOutDir, "index.html"), indexBuf.Bytes(), 0644); err != nil {
		return Result{}, err
	}

	if err := os.WriteFile(filepath.Join(absOutDir, "style.css"), []byte(defaultCSS), 0644); err != nil {
		return Result{}, err
	}

	if err := os.WriteFile(filepath.Join(absOutDir, "app.js"), []byte(defaultJS), 0644); err != nil {
		return Result{}, err
	}

	jsonBytes, err := json.Marshal(searchItems)
	if err != nil {
		return Result{}, fmt.Errorf("marshal search index: %w", err)
	}
	searchJS := fmt.Sprintf("window.LW_INDEX = %s;\n", jsonBytes)
	if err := os.WriteFile(filepath.Join(absOutDir, "search.js"), []byte(searchJS), 0644); err != nil {
		return Result{}, err
	}

	return res, nil
}

func computeRootRel(currentDir string) string {
	currentDir = filepath.Clean(currentDir)
	if currentDir == "." || currentDir == "" {
		return ""
	}
	rel, err := filepath.Rel(currentDir, ".")
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel) + "/"
}

func isSameOrSubdir(base, target string) bool {
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	if base == target {
		return true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func evalExistingAncestor(p string) string {
	cur := filepath.Clean(p)
	var parts []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			realCur, err := filepath.EvalSymlinks(cur)
			if err == nil {
				cur = realCur
			}
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		parts = append([]string{filepath.Base(cur)}, parts...)
		cur = parent
	}
	for _, part := range parts {
		cur = filepath.Join(cur, part)
	}
	return filepath.Clean(cur)
}

func copyFileByteForByte(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
