package extract

// markdown.go implements backbone §10's NewFile: a passthrough Extractor
// for local .md/.txt sources — a file that is already markdown (or plain
// text close enough to it) needs no DOM walk, only a Title recovered from
// its first ATX "# " heading and a normalized trailing newline
// (00-conventions.md §2, "every file ends with exactly one newline").

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/slug"
)

// ErrNotText is wrapped by an Extractor that refuses a file because its
// content is not text — invalid UTF-8, or a NUL byte in the first 8 KiB
// (004 F.E3). The walker never reads contents, so this sentinel is how the
// backend reports the verdict back to the folder-ingest caller.
var ErrNotText = errors.New("not text")

// sniffWindow is how many leading bytes the text sniff inspects: enough
// for any real magic-number signature (PNG's is 8 bytes), small enough
// that the verdict is cheap. A NUL or invalid UTF-8 past the window does
// not disqualify a file (004 F.E3).
const sniffWindow = 8 * 1024

// trimTruncatedTail drops the final, possibly truncated UTF-8 sequence
// from a window cut out of a larger file: a multi-byte rune straddling
// the cut leaves an incomplete tail in the window, and that is an
// artifact of the window, not a property of the file. Only the last
// sequence is ever touched — a rune cut by the edge is forgiven, while
// invalid bytes fully inside the window remain and utf8.Valid still
// rejects them.
func trimTruncatedTail(window []byte) []byte {
	n := len(window)
	k := 0
	// Walk back over the trailing continuation bytes of the final rune.
	for k < n && !utf8.RuneStart(window[n-1-k]) {
		k++
	}
	if k == n {
		// The whole window is continuation bytes. A cut rune carries at
		// most UTFMax-1 of them, so this cannot be a cut artifact —
		// leave it for utf8.Valid to reject.
		return window
	}
	var size int
	switch c := window[n-1-k]; {
	case c < utf8.RuneSelf:
		size = 1
	case c < 0xE0:
		size = 2
	case c < 0xF0:
		size = 3
	case c < 0xF8:
		size = 4
	default:
		// 0xF8..0xFF can begin no rune, not even a cut one.
		return window
	}
	if size > k+1 {
		// The final rune's continuation bytes continue past the cut:
		// drop the partial sequence (start byte and its continuations).
		return window[:n-1-k]
	}
	// The final sequence is complete as cut — judge it as-is.
	return window
}

// fileExtractor is the Extractor NewFile returns.
type fileExtractor struct{}

// NewFile returns an Extractor for local .md and .txt files: the content
// is used verbatim as Markdown, never reformatted (backbone §10).
func NewFile() Extractor {
	return fileExtractor{}
}

// CanHandle reports whether uri is a local path (not http/https) with a
// .md, .markdown or .txt extension (004 F.E2 added .markdown — the
// canonical extension of the format this extractor passes through —
// case-insensitive like .md always was).
func (fileExtractor) CanHandle(uri string) bool {
	if isRemoteURL(uri) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(uri))
	return ext == ".md" || ext == ".markdown" || ext == ".txt"
}

// Extract reads uri and returns it as a Doc: Markdown is the file's
// content with line endings normalized to "\n" and exactly one trailing
// newline, Title is the document's YAML frontmatter `title:` when the file
// starts with a `---` line closed by another one (008 contract §2 — a
// frontmatter title wins over a heading), else the text of the first "# "
// ATX heading (or "" if neither exists — the caller, e.g.
// stage.ingest_source's tool, falls back to the source's own basename),
// Kind defaults to "article" (backbone §10 rules out any readability
// heuristic that would guess "paper" or "transcript"), and Extractor is
// "passthrough". The markdown itself is passthrough: frontmatter included,
// byte for byte. OriginalBytes is the file exactly as read — before the
// newline normalization above — so the vault keeps a verbatim copy beside a
// raw whose body lw rewrote (058).
func (fileExtractor) Extract(ctx context.Context, uri string) (*Doc, error) {
	b, err := os.ReadFile(uri)
	if err != nil {
		return nil, fmt.Errorf("extract: read %s: %w", uri, err)
	}

	// 004 F.E3: whether a file is text is decided here, in the backend,
	// not in the walker — Walk is stat-only and selects by CanHandle, so
	// the sniff must happen where the bytes are already in hand. Only the
	// first sniffWindow bytes are examined (see sniffWindow).
	window := b
	if len(window) > sniffWindow {
		window = trimTruncatedTail(window[:sniffWindow])
	}
	if !utf8.Valid(window) || bytes.IndexByte(window, 0) >= 0 {
		return nil, fmt.Errorf("extract: %s: %w", uri, ErrNotText)
	}

	body := normalizeNewlines(string(b))
	body = strings.TrimRight(body, "\n")
	if body != "" {
		body += "\n"
	}

	title := frontmatterTitle(body)
	if title == "" {
		title = firstATXH1(body)
	}

	var assets []Asset
	ext := strings.ToLower(filepath.Ext(uri))
	if ext == ".md" || ext == ".markdown" {
		docSlug := DocSlug(title, uri)
		body, assets, _ = scanImageRefs(body, uri, docSlug, true)
		body = strings.TrimRight(body, "\n")
		if body != "" {
			body += "\n"
		}
	}

	return &Doc{
		Title:         title,
		SourceURL:     uri,
		Markdown:      body,
		Kind:          "article",
		Extractor:     "passthrough",
		OriginalBytes: b,
		OriginalExt:   strings.ToLower(filepath.Ext(uri)),
		Assets:        assets,
	}, nil
}

// frontmatterTitle returns the top-level `title:` value from body's YAML
// frontmatter — the block between an opening `---` as the very first line
// and a closing `---` — trimmed, with one level of matching surrounding
// double or single quotes removed. It returns "" when the document does
// not start with `---`, when no closing line exists (the block is then
// just body text), or when the key is absent or empty. A real YAML parser
// is deliberately not used: one top-level scalar key is all passthrough
// content needs, and a line scan cannot mis-parse the rest of the file.
func frontmatterTitle(body string) string {
	lines := strings.Split(body, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return ""
	}

	title := ""
	closed := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			closed = true
			break
		}
		if title == "" && strings.HasPrefix(line, "title:") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "title:"))
		}
	}
	if !closed || title == "" {
		return ""
	}
	if len(title) >= 2 && (title[0] == '"' || title[0] == '\'') && title[len(title)-1] == title[0] {
		title = title[1 : len(title)-1]
	}
	return title
}

// normalizeNewlines rewrites "\r\n" and lone "\r" to "\n", so a file
// saved on a different platform still extracts to byte-identical
// markdown on every run.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// firstATXH1 returns the text of the first line of body that is a
// level-1 ATX heading ("# " followed by non-whitespace), trimmed, or ""
// if body has none. This is deliberately simpler than a full markdown
// parse — passthrough content is trusted to already be well-formed
// markdown — and never looks inside a fenced code block, since a line
// beginning with "# " but preceded by an open "```" fence is source code,
// not a heading; body is scanned top-down so the fence state is exact.
func firstATXH1(body string) string {
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

var supportedImageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".svg":  true,
	".webp": true,
}

func isSupportedImageExt(path string) bool {
	return supportedImageExts[strings.ToLower(filepath.Ext(path))]
}

type extractedAsset struct {
	name       string
	sourcePath string
	bytes      []byte
	stem       string
	sha        string
}

type imageMatch struct {
	start       int
	end         int
	replacement string
}

func buildProtectedMask(body string) []bool {
	n := len(body)
	protected := make([]bool, n)
	if n == 0 {
		return protected
	}

	// 1. YAML frontmatter: opening `---` on the very first line closed by `---` line.
	nl1 := strings.IndexByte(body, '\n')
	firstLineEnd := nl1
	if firstLineEnd < 0 {
		firstLineEnd = n
	}
	if strings.TrimSpace(body[:firstLineEnd]) == "---" && nl1 >= 0 {
		pos := nl1 + 1
		for pos < n {
			nextNL := strings.IndexByte(body[pos:], '\n')
			var lineEnd int
			if nextNL < 0 {
				lineEnd = n
			} else {
				lineEnd = pos + nextNL
			}
			if strings.TrimSpace(body[pos:lineEnd]) == "---" {
				end := lineEnd
				if nextNL >= 0 {
					end++ // include \n
				}
				for i := 0; i < end && i < n; i++ {
					protected[i] = true
				}
				break
			}
			if nextNL < 0 {
				break
			}
			pos = lineEnd + 1
		}
	}

	// 2. Fenced code blocks: ``` or ~~~
	pos := 0
	inFence := false
	var fenceChar byte
	var fenceLen int

	for pos < n {
		nextNL := strings.IndexByte(body[pos:], '\n')
		var lineEnd int
		if nextNL < 0 {
			lineEnd = n
		} else {
			lineEnd = pos + nextNL
		}
		lineStr := body[pos:lineEnd]

		if pos < n && protected[pos] {
			if nextNL < 0 {
				break
			}
			pos = lineEnd + 1
			continue
		}

		s := 0
		for s < len(lineStr) && lineStr[s] == ' ' {
			s++
		}
		trimmed := lineStr[s:]

		if !inFence {
			if s <= 3 && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
				ch := trimmed[0]
				count := 0
				for count < len(trimmed) && trimmed[count] == ch {
					count++
				}
				if count >= 3 {
					info := trimmed[count:]
					if ch == '~' || !strings.ContainsRune(info, '`') {
						inFence = true
						fenceChar = ch
						fenceLen = count
					}
				}
			}
		} else {
			isClosing := false
			if s <= 3 && len(trimmed) >= fenceLen {
				k := 0
				for k < len(trimmed) && trimmed[k] == fenceChar {
					k++
				}
				if k >= fenceLen {
					isClosing = true
					for ; k < len(trimmed); k++ {
						if trimmed[k] != ' ' && trimmed[k] != '\t' && trimmed[k] != '\r' {
							isClosing = false
							break
						}
					}
				}
			}
			if isClosing {
				inFence = false
			}
			end := lineEnd
			if nextNL >= 0 {
				end++
			}
			for i := pos; i < end && i < n; i++ {
				protected[i] = true
			}
		}

		if inFence {
			end := lineEnd
			if nextNL >= 0 {
				end++
			}
			for i := pos; i < end && i < n; i++ {
				protected[i] = true
			}
		}

		if nextNL < 0 {
			break
		}
		pos = lineEnd + 1
	}

	// 3. Inline code spans: `...`
	i := 0
	for i < n {
		if protected[i] || body[i] != '`' {
			i++
			continue
		}
		k := i
		for k < n && body[k] == '`' && !protected[k] {
			k++
		}
		btLen := k - i

		matched := false
		closeStart := -1
		j := k
		for j < n {
			if protected[j] {
				break
			}
			if body[j] == '\n' {
				nextJ := j + 1
				for nextJ < n && (body[nextJ] == ' ' || body[nextJ] == '\t' || body[nextJ] == '\r') {
					nextJ++
				}
				if nextJ < n && body[nextJ] == '\n' {
					break
				}
			}
			if body[j] == '`' {
				m := j
				for m < n && body[m] == '`' && !protected[m] {
					m++
				}
				if (m - j) == btLen {
					matched = true
					closeStart = j
					break
				}
				j = m
				continue
			}
			j++
		}

		if matched {
			for idx := i; idx < closeStart+btLen; idx++ {
				protected[idx] = true
			}
			i = closeStart + btLen
		} else {
			i = k
		}
	}

	return protected
}

func resolveImageDestination(dest, uri string, readBytes bool) (string, string, []byte, bool) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return "", "", nil, false
	}
	lower := strings.ToLower(dest)
	if strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "ftp://") ||
		strings.HasPrefix(lower, "//") ||
		strings.HasPrefix(dest, "/") ||
		strings.HasPrefix(dest, "../../raw/assets/") {
		return "", "", nil, false
	}

	relPath := dest
	if idx := strings.IndexAny(relPath, "#?"); idx >= 0 {
		relPath = relPath[:idx]
	}
	if unescaped, err := url.PathUnescape(relPath); err == nil {
		relPath = unescaped
	}

	imgPath := filepath.Clean(filepath.Join(filepath.Dir(uri), relPath))
	imgExt := strings.ToLower(filepath.Ext(imgPath))
	if !isSupportedImageExt(imgPath) {
		return "", "", nil, false
	}

	info, err := os.Lstat(imgPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", "", nil, false
	}

	var bytes []byte
	if readBytes {
		var rerr error
		bytes, rerr = os.ReadFile(imgPath)
		if rerr != nil {
			return "", "", nil, false
		}
	}

	return imgPath, imgExt, bytes, true
}

func assignAssetName(docSlug, imgPath, imgExt string, imgBytes []byte, extracted *[]extractedAsset) string {
	imgStem := strings.TrimSuffix(filepath.Base(imgPath), filepath.Ext(imgPath))
	imgSlug := slug.Make(imgStem)
	if imgSlug == "" {
		imgSlug = "image"
	}

	var stem string
	if imgSlug == docSlug || strings.HasPrefix(imgSlug, docSlug+"-") {
		stem = imgSlug
	} else {
		stem = docSlug + "-" + imgSlug
	}

	h := sha256.Sum256(imgBytes)
	imgSHA := hex.EncodeToString(h[:])

	for _, ea := range *extracted {
		if ea.stem == stem && (ea.sourcePath == imgPath || ea.sha == imgSHA) {
			return ea.name
		}
	}

	candidate := stem + imgExt
	taken := func(name string) bool {
		for _, ea := range *extracted {
			if ea.name == name {
				return true
			}
		}
		return false
	}

	chosenName := candidate
	if taken(chosenName) {
		for n := 2; ; n++ {
			c := fmt.Sprintf("%s-%d%s", stem, n, imgExt)
			if !taken(c) {
				chosenName = c
				break
			}
		}
	}

	*extracted = append(*extracted, extractedAsset{
		name:       chosenName,
		sourcePath: imgPath,
		bytes:      imgBytes,
		stem:       stem,
		sha:        imgSHA,
	})
	return chosenName
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parseHTMLImgSrc(tag string) (srcVal string, valStart, valEnd int, ok bool) {
	p := 4
	n := len(tag)
	for p < n {
		for p < n && (tag[p] == ' ' || tag[p] == '\t' || tag[p] == '\n' || tag[p] == '\r') {
			p++
		}
		if p >= n || tag[p] == '>' || tag[p] == '/' {
			break
		}
		nameStart := p
		for p < n && tag[p] != '=' && tag[p] != ' ' && tag[p] != '\t' && tag[p] != '\n' && tag[p] != '\r' && tag[p] != '>' && tag[p] != '/' {
			p++
		}
		name := tag[nameStart:p]
		for p < n && (tag[p] == ' ' || tag[p] == '\t' || tag[p] == '\n' || tag[p] == '\r') {
			p++
		}
		if p < n && tag[p] == '=' {
			p++
			for p < n && (tag[p] == ' ' || tag[p] == '\t' || tag[p] == '\n' || tag[p] == '\r') {
				p++
			}
			if p < n && (tag[p] == '"' || tag[p] == '\'') {
				quote := tag[p]
				vStart := p + 1
				p++
				for p < n && tag[p] != quote {
					p++
				}
				if p < n && tag[p] == quote {
					vEnd := p
					p++
					if strings.EqualFold(name, "src") {
						return tag[vStart:vEnd], vStart, vEnd, true
					}
				}
			} else {
				for p < n && tag[p] != ' ' && tag[p] != '\t' && tag[p] != '\n' && tag[p] != '\r' && tag[p] != '>' && tag[p] != '/' {
					p++
				}
			}
		}
	}
	return "", 0, 0, false
}

func scanImageRefs(body, uri, docSlug string, rewrite bool) (string, []Asset, []string) {
	protected := buildProtectedMask(body)
	n := len(body)

	var matches []imageMatch
	var extracted []extractedAsset
	var referencedPaths []string
	seenPaths := make(map[string]bool)

	i := 0
	for i < n {
		if protected[i] {
			i++
			continue
		}

		// Form 3: Obsidian embed ![[...]]
		if i+3 <= n && body[i:i+3] == "![[" {
			closeIdx := -1
			for q := i + 3; q+1 < n && body[q] != '\n'; q++ {
				if body[q] == ']' && body[q+1] == ']' {
					closeIdx = q
					break
				}
			}
			if closeIdx > 0 {
				endP := closeIdx + 2
				allUnprotected := true
				for p := i; p < endP; p++ {
					if protected[p] {
						allUnprotected = false
						break
					}
				}
				if allUnprotected {
					raw := body[i+3 : closeIdx]
					var dest, caption string
					if pipe := strings.IndexByte(raw, '|'); pipe >= 0 {
						dest = strings.TrimSpace(raw[:pipe])
						caption = strings.TrimSpace(raw[pipe+1:])
					} else {
						dest = strings.TrimSpace(raw)
					}

					imgPath, imgExt, imgBytes, ok := resolveImageDestination(dest, uri, rewrite)
					if ok {
						if !seenPaths[imgPath] {
							seenPaths[imgPath] = true
							referencedPaths = append(referencedPaths, imgPath)
						}
						if rewrite {
							imgStem := strings.TrimSuffix(filepath.Base(imgPath), filepath.Ext(imgPath))
							chosenName := assignAssetName(docSlug, imgPath, imgExt, imgBytes, &extracted)
							alt := imgStem
							if caption != "" && !isDigits(caption) {
								alt = caption
							}
							matches = append(matches, imageMatch{
								start:       i,
								end:         endP,
								replacement: fmt.Sprintf("![%s](../../raw/assets/%s)", alt, chosenName),
							})
						}
						i = endP
						continue
					}
				}
			}
		}

		// Form 1: Markdown inline image ![alt](dest)
		if i+2 <= n && body[i:i+2] == "![" && (i+3 > n || body[i+2] != '[') {
			bracketDepth := 0
			closeBracket := -1
			for p := i + 1; p < n && body[p] != '\n'; p++ {
				if body[p] == '[' {
					bracketDepth++
				} else if body[p] == ']' {
					bracketDepth--
					if bracketDepth == 0 {
						closeBracket = p
						break
					}
				}
			}
			if closeBracket > 0 && closeBracket+1 < n && body[closeBracket+1] == '(' {
				alt := body[i+2 : closeBracket]
				p := closeBracket + 2
				for p < n && (body[p] == ' ' || body[p] == '\t') {
					p++
				}
				var dest, quotedTitle string
				valid := false
				endP := -1

				if p < n && body[p] == '<' {
					closeAngle := -1
					for q := p + 1; q < n && body[q] != '\n'; q++ {
						if body[q] == '>' {
							closeAngle = q
							break
						}
					}
					if closeAngle > 0 {
						dest = body[p+1 : closeAngle]
						p = closeAngle + 1
						for p < n && (body[p] == ' ' || body[p] == '\t') {
							p++
						}
						if p < n && (body[p] == '"' || body[p] == '\'') {
							qChar := body[p]
							closeQ := -1
							for q := p + 1; q < n && body[q] != '\n'; q++ {
								if body[q] == qChar {
									closeQ = q
									break
								}
							}
							if closeQ > 0 {
								quotedTitle = body[p : closeQ+1]
								p = closeQ + 1
								for p < n && (body[p] == ' ' || body[p] == '\t') {
									p++
								}
							}
						}
						if p < n && body[p] == ')' {
							valid = true
							endP = p + 1
						}
					}
				} else {
					destStart := p
					parenDepth := 0
					for p < n && body[p] != '\n' {
						if body[p] == '(' {
							parenDepth++
						} else if body[p] == ')' {
							if parenDepth > 0 {
								parenDepth--
							} else {
								break
							}
						} else if (body[p] == ' ' || body[p] == '\t') && parenDepth == 0 {
							break
						}
						p++
					}
					dest = body[destStart:p]
					for p < n && (body[p] == ' ' || body[p] == '\t') {
						p++
					}
					if p < n && (body[p] == '"' || body[p] == '\'') {
						qChar := body[p]
						closeQ := -1
						for q := p + 1; q < n && body[q] != '\n'; q++ {
							if body[q] == qChar {
								closeQ = q
								break
							}
						}
						if closeQ > 0 {
							quotedTitle = body[p : closeQ+1]
							p = closeQ + 1
							for p < n && (body[p] == ' ' || body[p] == '\t') {
								p++
							}
						}
					}
					if p < n && body[p] == ')' {
						valid = true
						endP = p + 1
					}
				}

				if valid {
					allUnprotected := true
					for k := i; k < endP; k++ {
						if protected[k] {
							allUnprotected = false
							break
						}
					}
					if allUnprotected {
						imgPath, imgExt, imgBytes, ok := resolveImageDestination(dest, uri, rewrite)
						if ok {
							if !seenPaths[imgPath] {
								seenPaths[imgPath] = true
								referencedPaths = append(referencedPaths, imgPath)
							}
							if rewrite {
								chosenName := assignAssetName(docSlug, imgPath, imgExt, imgBytes, &extracted)
								var repl string
								if quotedTitle != "" {
									repl = fmt.Sprintf("![%s](../../raw/assets/%s %s)", alt, chosenName, quotedTitle)
								} else {
									repl = fmt.Sprintf("![%s](../../raw/assets/%s)", alt, chosenName)
								}
								matches = append(matches, imageMatch{
									start:       i,
									end:         endP,
									replacement: repl,
								})
							}
							i = endP
							continue
						}
					}
				}
			}
		}

		// Form 2: HTML <img ...>
		if i+4 <= n && strings.ToLower(body[i:i+4]) == "<img" {
			if i+4 == n || body[i+4] == ' ' || body[i+4] == '\t' || body[i+4] == '\n' || body[i+4] == '\r' || body[i+4] == '/' {
				inQuote := byte(0)
				tagEnd := -1
				for p := i + 4; p < n; p++ {
					b := body[p]
					if inQuote != 0 {
						if b == inQuote {
							inQuote = 0
						}
					} else if b == '"' || b == '\'' {
						inQuote = b
					} else if b == '>' {
						tagEnd = p + 1
						break
					}
				}
				if tagEnd > 0 {
					allUnprotected := true
					for k := i; k < tagEnd; k++ {
						if protected[k] {
							allUnprotected = false
							break
						}
					}
					if allUnprotected {
						tagStr := body[i:tagEnd]
						srcVal, valStart, valEnd, hasSrc := parseHTMLImgSrc(tagStr)
						if hasSrc {
							imgPath, imgExt, imgBytes, ok := resolveImageDestination(srcVal, uri, rewrite)
							if ok {
								if !seenPaths[imgPath] {
									seenPaths[imgPath] = true
									referencedPaths = append(referencedPaths, imgPath)
								}
								if rewrite {
									chosenName := assignAssetName(docSlug, imgPath, imgExt, imgBytes, &extracted)
									repl := tagStr[:valStart] + "../../raw/assets/" + chosenName + tagStr[valEnd:]
									matches = append(matches, imageMatch{
										start:       i,
										end:         tagEnd,
										replacement: repl,
									})
								}
								i = tagEnd
								continue
							}
						}
					}
				}
			}
		}

		i++
	}

	newBody := body
	if rewrite && len(matches) > 0 {
		var out strings.Builder
		last := 0
		for _, m := range matches {
			out.WriteString(body[last:m.start])
			out.WriteString(m.replacement)
			last = m.end
		}
		out.WriteString(body[last:])
		newBody = out.String()
	}

	var assets []Asset
	for _, ea := range extracted {
		assets = append(assets, Asset{
			Name:       ea.name,
			SourcePath: ea.sourcePath,
			Bytes:      ea.bytes,
		})
	}

	return newBody, assets, referencedPaths
}
