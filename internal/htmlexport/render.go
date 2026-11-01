package htmlexport

import (
	"bytes"
	"fmt"
	"html"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/awepo-pro/lw/internal/cite"
	"github.com/awepo-pro/lw/internal/vault"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
	"go.abhg.dev/goldmark/wikilink"
)

var (
	anchorLineRe = regexp.MustCompile(`(?m)^<!-- page ([1-9][0-9]*) -->\r?$`)
	codeFenceRe  = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	htmlTagRe    = regexp.MustCompile(`(?s)<.*?>`)
	mdLinkRe     = regexp.MustCompile(`\[([^\]]+)\]\([^\)]+\)`)
	wikiLinkRe   = regexp.MustCompile(`\[\[(?:[^|\]]*\|)?([^\]]+)\]\]`)
)

type customWikilinkRenderer struct {
	v             *vault.Vault
	currentRelDir string
	closingTag    map[*wikilink.Node]string
}

func (r *customWikilinkRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(wikilink.Kind, r.Render)
}

func (r *customWikilinkRenderer) Render(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n, ok := node.(*wikilink.Node)
	if !ok {
		return ast.WalkStop, fmt.Errorf("unexpected node %T", node)
	}

	if !entering {
		if tag, exists := r.closingTag[n]; exists {
			_, _ = w.WriteString(tag)
		}
		return ast.WalkContinue, nil
	}

	if n.Embed && isImageEmbed(n) {
		target := string(n.Target)
		assetRel := target
		if !strings.HasPrefix(target, "raw/assets/") && !strings.Contains(target, "/") {
			assetRel = path.Join("raw/assets", target)
		}
		rel, err := filepath.Rel(r.currentRelDir, assetRel)
		if err != nil {
			rel = assetRel
		}
		relURL := filepath.ToSlash(rel)
		_, _ = w.WriteString(`<img src="` + html.EscapeString(relURL) + `"`)
		if n.ChildCount() == 1 {
			label := string(n.Target)
			if t, ok := n.FirstChild().(*ast.Text); ok {
				label = string(t.Segment.Value(src))
			}
			if label != target {
				_, _ = w.WriteString(` alt="` + html.EscapeString(label) + `"`)
			}
		}
		_, _ = w.WriteString(`>`)
		return ast.WalkSkipChildren, nil
	}

	target := string(n.Target)
	fragment := string(n.Fragment)

	if target == "" && fragment != "" {
		r.closingTag[n] = "</a>"
		_, _ = w.WriteString(`<a href="#` + html.EscapeString(fragment) + `">`)
		return ast.WalkContinue, nil
	}

	resolvedPath, ok := vault.Resolve(r.v, target)
	if ok {
		targetHTML := strings.TrimSuffix(resolvedPath, ".md") + ".html"
		rel, err := filepath.Rel(r.currentRelDir, targetHTML)
		if err != nil {
			rel = targetHTML
		}
		relURL := filepath.ToSlash(rel)
		if fragment != "" {
			relURL += "#" + fragment
		}
		r.closingTag[n] = "</a>"
		_, _ = w.WriteString(`<a href="` + html.EscapeString(relURL) + `">`)
		return ast.WalkContinue, nil
	}

	r.closingTag[n] = "</span>"
	_, _ = w.WriteString(`<span class="broken" title="unresolved link">`)
	return ast.WalkContinue, nil
}

func isImageEmbed(n *wikilink.Node) bool {
	filename := strings.ToLower(string(n.Target))
	ext := filepath.Ext(filename)
	switch ext {
	case ".apng", ".avif", ".gif", ".jpg", ".jpeg", ".jfif", ".pjpeg", ".pjp", ".png", ".svg", ".webp":
		return true
	default:
		return false
	}
}

type customExtender struct {
	v             *vault.Vault
	currentRelDir string
}

func (e *customExtender) Extend(md goldmark.Markdown) {
	md.Parser().AddOptions(
		parser.WithInlineParsers(
			util.Prioritized(&wikilink.Parser{}, 199),
		),
	)
	md.Renderer().AddOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(&customWikilinkRenderer{
				v:             e.v,
				currentRelDir: e.currentRelDir,
				closingTag:    make(map[*wikilink.Node]string),
			}, 199),
		),
	)
}

type citeReplacement struct {
	num    int
	token  string
	raw    string
	offset int
	html   string
}

func renderWikiBody(v *vault.Vault, p *vault.Page, currentRelDir string) (string, error) {
	allCites := cite.Scan(p.Body)
	var validCites []cite.Cite
	for _, c := range allCites {
		if c.Err == "" {
			validCites = append(validCites, c)
		}
	}

	var replacements []citeReplacement
	for i, c := range validCites {
		n := i + 1
		token := fmt.Sprintf("LWCITE%05dLW", n)
		rawHTMLPath := strings.TrimSuffix(c.Source, ".md") + ".html"
		rel, err := filepath.Rel(currentRelDir, rawHTMLPath)
		if err != nil {
			rel = rawHTMLPath
		}
		relURL := filepath.ToSlash(rel)
		href := relURL
		if c.From > 0 {
			href = fmt.Sprintf("%s#p%d", relURL, c.From)
		}
		title := strings.TrimSuffix(strings.TrimPrefix(c.Raw, "^["), "]")
		citeHTML := fmt.Sprintf(`<sup class="cite"><a href="%s" title="%s">[%d]</a></sup>`,
			html.EscapeString(href), html.EscapeString(title), n)

		replacements = append(replacements, citeReplacement{
			num:    n,
			token:  token,
			raw:    c.Raw,
			offset: c.Offset,
			html:   citeHTML,
		})
	}

	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].offset > replacements[j].offset
	})

	body := p.Body
	for _, rep := range replacements {
		if rep.offset <= len(body) && strings.HasPrefix(body[rep.offset:], rep.raw) {
			body = body[:rep.offset] + rep.token + body[rep.offset+len(rep.raw):]
		}
	}

	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			&customExtender{v: v, currentRelDir: currentRelDir},
		),
	)

	var buf bytes.Buffer
	if err := md.Convert([]byte(body), &buf); err != nil {
		return "", err
	}
	outHTML := buf.String()

	for _, rep := range replacements {
		outHTML = strings.ReplaceAll(outHTML, rep.token, rep.html)
	}

	return outHTML, nil
}

func renderRawBody(v *vault.Vault, r *vault.RawSource, currentRelDir string) (string, error) {
	type anchorToken struct {
		token string
		num   string
	}
	var anchors []anchorToken

	matches := anchorLineRe.FindAllStringSubmatch(r.Body, -1)
	body := r.Body
	for _, m := range matches {
		if len(m) >= 2 {
			num := m[1]
			token := fmt.Sprintf("LWPAGEANCHOR%sLWP", num)
			anchors = append(anchors, anchorToken{token: token, num: num})
			body = strings.ReplaceAll(body, m[0], token)
		}
	}

	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			&customExtender{v: v, currentRelDir: currentRelDir},
		),
	)

	var buf bytes.Buffer
	if err := md.Convert([]byte(body), &buf); err != nil {
		return "", err
	}
	outHTML := buf.String()

	for _, a := range anchors {
		anchorTag := fmt.Sprintf(`<a id="p%s"></a>`, a.num)
		outHTML = strings.ReplaceAll(outHTML, a.token, anchorTag)
	}

	return outHTML, nil
}

func stripMarkdownSnippet(body string) string {
	s := codeFenceRe.ReplaceAllString(body, " ")
	s = anchorLineRe.ReplaceAllString(s, " ")
	s = htmlTagRe.ReplaceAllString(s, " ")

	allCites := cite.Scan(s)
	for i := len(allCites) - 1; i >= 0; i-- {
		c := allCites[i]
		if c.Offset < len(s) && strings.HasPrefix(s[c.Offset:], c.Raw) {
			s = s[:c.Offset] + " " + s[c.Offset+len(c.Raw):]
		}
	}

	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = wikiLinkRe.ReplaceAllString(s, "$1")

	var b strings.Builder
	for _, r := range s {
		switch r {
		case '#', '*', '_', '~', '`', '>', '[', ']', '(', ')', '|':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}

	fields := strings.Fields(b.String())
	normalized := strings.Join(fields, " ")

	runes := []rune(normalized)
	if len(runes) > 160 {
		runes = runes[:160]
	}
	return strings.TrimRightFunc(string(runes), unicode.IsSpace)
}
