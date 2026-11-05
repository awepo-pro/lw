package htmlexport

import (
	"html/template"
)

type sourceItem struct {
	Label string
	URL   string
}

type backlinkItem struct {
	Title string
	URL   string
}

type wikiPageData struct {
	Title      string
	SiteTitle  string
	RootRel    string
	Type       string
	Tags       []string
	Confidence string
	Contested  bool
	Updated    string
	BodyHTML   template.HTML
	Sources    []sourceItem
	Backlinks  []backlinkItem
	ExportDate string
}

type citedByItem struct {
	Title string
	URL   string
	Path  string
}

type rawPageData struct {
	Title      string
	SiteTitle  string
	RootRel    string
	SourceURL  string
	IsHTTPURL  bool
	Ingested   string
	CitedBy    []citedByItem
	BodyHTML   template.HTML
	ExportDate string
}

type pageEntry struct {
	Title string
	URL   string
	Tags  []string
	Path  string
}

type typeGroup struct {
	Type  string
	Pages []pageEntry
}

type rawItem struct {
	Title string
	URL   string
	Path  string
}

type indexData struct {
	SiteTitle  string
	PageCount  int
	Groups     []typeGroup
	RawSources []rawItem
	ExportDate string
}

var (
	wikiPageTmpl = template.Must(template.New("wikiPage").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} - {{.SiteTitle}}</title>
<link rel="stylesheet" href="{{.RootRel}}style.css">
<script src="{{.RootRel}}search.js" defer></script>
<script src="{{.RootRel}}app.js" defer></script>
</head>
<body data-root="{{.RootRel}}">
<header class="top-bar">
  <a href="{{.RootRel}}index.html" class="site-title">{{.SiteTitle}}</a>
  <div class="search-wrap">
    <input type="search" id="search-input" placeholder="Search..." autocomplete="off">
    <div id="search-results"></div>
  </div>
</header>
<main>
  <article>
    <h1>{{.Title}}</h1>
    <div class="meta-line">
      {{if .Type}}<span class="meta-type">{{.Type}}</span>{{end}}
      {{range .Tags}}<span class="chip">{{.}}</span>{{end}}
      {{if .Confidence}}<span class="confidence">confidence: {{.Confidence}}</span>{{end}}
      {{if .Contested}}<span class="badge contested">contested</span>{{end}}
      {{if .Updated}}<span class="date">updated {{.Updated}}</span>{{end}}
    </div>
    <div class="page-body">
      {{.BodyHTML}}
    </div>
    {{if .Sources}}
    <section class="sources">
      <h2>Sources</h2>
      <ul>
        {{range .Sources}}
        <li>
          {{if .URL}}<a href="{{.URL}}">{{.Label}}</a>{{else}}<span>{{.Label}}</span>{{end}}
        </li>
        {{end}}
      </ul>
    </section>
    {{end}}
    {{if .Backlinks}}
    <section class="backlinks">
      <h2>Linked from</h2>
      <ul>
        {{range .Backlinks}}
        <li><a href="{{.URL}}">{{.Title}}</a></li>
        {{end}}
      </ul>
    </section>
    {{end}}
  </article>
</main>
<footer>
  <p>exported by lw {{.ExportDate}}</p>
</footer>
</body>
</html>
`))

	rawPageTmpl = template.Must(template.New("rawPage").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} - {{.SiteTitle}}</title>
<link rel="stylesheet" href="{{.RootRel}}style.css">
<script src="{{.RootRel}}search.js" defer></script>
<script src="{{.RootRel}}app.js" defer></script>
</head>
<body data-root="{{.RootRel}}">
<header class="top-bar">
  <a href="{{.RootRel}}index.html" class="site-title">{{.SiteTitle}}</a>
  <div class="search-wrap">
    <input type="search" id="search-input" placeholder="Search..." autocomplete="off">
    <div id="search-results"></div>
  </div>
</header>
<main>
  <article>
    <h1>{{.Title}}</h1>
    <div class="meta-line">
      {{if .SourceURL}}
        {{if .IsHTTPURL}}
          <a href="{{.SourceURL}}" target="_blank" rel="noopener noreferrer">{{.SourceURL}}</a>
        {{else}}
          <span>{{.SourceURL}}</span>
        {{end}}
      {{end}}
      {{if .Ingested}}<span class="date">ingested {{.Ingested}}</span>{{end}}
    </div>
    {{if .CitedBy}}
    <section class="cited-by">
      <h2>Cited by</h2>
      <ul>
        {{range .CitedBy}}
        <li><a href="{{.URL}}">{{.Title}}</a></li>
        {{end}}
      </ul>
    </section>
    {{end}}
    <div class="page-body">
      {{.BodyHTML}}
    </div>
  </article>
</main>
<footer>
  <p>exported by lw {{.ExportDate}}</p>
</footer>
</body>
</html>
`))

	indexTmpl = template.Must(template.New("indexPage").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.SiteTitle}}</title>
<link rel="stylesheet" href="style.css">
<script src="search.js" defer></script>
<script src="app.js" defer></script>
</head>
<body data-root="">
<header class="top-bar">
  <h1 class="site-title">{{.SiteTitle}}</h1>
  <div class="search-wrap">
    <input type="search" id="search-input" placeholder="Search..." autocomplete="off">
    <div id="search-results"></div>
  </div>
</header>
<main>
  <p class="page-count">{{.PageCount}} pages</p>
  {{range .Groups}}
  <section class="type-group">
    <h2>{{.Type}}</h2>
    <ul class="page-list">
      {{range .Pages}}
      <li>
        <a href="{{.URL}}">{{.Title}}</a>
        {{range .Tags}}<span class="chip">{{.}}</span>{{end}}
      </li>
      {{end}}
    </ul>
  </section>
  {{end}}
  {{if .RawSources}}
  <details class="raw-sources-details">
    <summary>Raw sources ({{len .RawSources}})</summary>
    <ul class="raw-list">
      {{range .RawSources}}
      <li><a href="{{.URL}}">{{.Title}}</a></li>
      {{end}}
    </ul>
  </details>
  {{end}}
</main>
<footer>
  <p>exported by lw {{.ExportDate}}</p>
</footer>
</body>
</html>
`))
)
