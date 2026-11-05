package htmlexport

// defaultCSS provides responsive, readable styling with light and dark mode support.
const defaultCSS = `* {
  box-sizing: border-box;
}

:root {
  --bg: #ffffff;
  --text: #24292f;
  --text-muted: #57606a;
  --link: #0969da;
  --border: #d0d7de;
  --code-bg: #f6f8fa;
  --chip-bg: #eaeef2;
  --broken: #cf222e;
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0d1117;
    --text: #c9d1d9;
    --text-muted: #8b949e;
    --link: #58a6ff;
    --border: #30363d;
    --code-bg: #161b22;
    --chip-bg: #21262d;
    --broken: #f85149;
  }
}

body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  line-height: 1.6;
  color: var(--text);
  background: var(--bg);
  max-width: 46rem;
  margin: 0 auto;
  padding: 1rem;
  word-wrap: break-word;
}

a {
  color: var(--link);
  text-decoration: underline;
}

.top-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.5rem;
  border-bottom: 1px solid var(--border);
  padding-bottom: 0.75rem;
  margin-bottom: 1.5rem;
}

.site-title {
  font-weight: 600;
  text-decoration: none;
  font-size: 1.1rem;
}

.search-wrap {
  position: relative;
}

#search-input {
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 0.35rem 0.6rem;
  font-size: 0.9rem;
  width: 14rem;
  max-width: 100%;
}

#search-results {
  display: none;
  position: absolute;
  right: 0;
  top: 100%;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  max-height: 20rem;
  width: 20rem;
  max-width: 90vw;
  overflow-y: auto;
  z-index: 1000;
  margin-top: 4px;
}

.search-item {
  display: block;
  padding: 0.5rem 0.75rem;
  border-bottom: 1px solid var(--border);
  text-decoration: none;
  color: var(--text);
}

.search-item:hover {
  background: var(--code-bg);
}

.search-item-title {
  font-weight: 600;
  color: var(--link);
}

.search-item-type {
  font-size: 0.75rem;
  color: var(--text-muted);
  margin-left: 0.5rem;
}

.search-item-snippet {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin-top: 0.25rem;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.meta-line {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  align-items: center;
  color: var(--text-muted);
  font-size: 0.85rem;
  margin-bottom: 1.5rem;
}

.meta-type {
  font-weight: 600;
  text-transform: capitalize;
}

.chip {
  display: inline-block;
  background: var(--chip-bg);
  color: var(--text);
  padding: 0.1rem 0.45rem;
  border-radius: 12px;
  font-size: 0.75rem;
}

.badge.contested {
  background: #ffebe9;
  color: #cf222e;
  padding: 0.1rem 0.45rem;
  border-radius: 4px;
  font-weight: 600;
  font-size: 0.75rem;
}

.broken {
  color: var(--broken);
  text-decoration: underline dotted var(--broken);
  cursor: help;
}

sup.cite {
  font-size: 0.75em;
  line-height: 1;
  vertical-align: super;
}

sup.cite a {
  text-decoration: none;
}

pre, table {
  max-width: 100%;
  overflow-x: auto;
}

pre {
  background: var(--code-bg);
  padding: 0.75rem;
  border-radius: 6px;
  border: 1px solid var(--border);
}

code {
  font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  font-size: 0.9em;
}

p code, li code {
  background: var(--code-bg);
  padding: 0.15rem 0.3rem;
  border-radius: 4px;
}

img {
  max-width: 100%;
  height: auto;
}

footer {
  border-top: 1px solid var(--border);
  margin-top: 3rem;
  padding-top: 1rem;
  font-size: 0.8rem;
  color: var(--text-muted);
}
`

// defaultJS provides client-side offline search filtering across titles, tags, and snippets.
const defaultJS = `(function () {
  function initSearch() {
    var input = document.getElementById("search-input");
    var results = document.getElementById("search-results");
    if (!input || !results) return;

    var root = document.body.getAttribute("data-root") || "";

    function escapeHTML(str) {
      if (!str) return "";
      return str.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
    }

    input.addEventListener("input", function () {
      var q = input.value.trim().toLowerCase();
      if (!q) {
        results.style.display = "none";
        results.innerHTML = "";
        return;
      }

      var index = window.LW_INDEX || [];
      var matches = [];
      for (var i = 0; i < index.length; i++) {
        var item = index[i];
        var title = (item.t || "").toLowerCase();
        var snippet = (item.s || "").toLowerCase();
        var type = (item.y || "").toLowerCase();
        var tags = item.g || [];

        var matched = title.indexOf(q) !== -1 || snippet.indexOf(q) !== -1 || type.indexOf(q) !== -1;
        if (!matched) {
          for (var j = 0; j < tags.length; j++) {
            if (tags[j].toLowerCase().indexOf(q) !== -1) {
              matched = true;
              break;
            }
          }
        }

        if (matched) {
          matches.push(item);
          if (matches.length >= 20) break;
        }
      }

      if (matches.length === 0) {
        results.innerHTML = '<div style="padding:0.5rem 0.75rem;color:var(--text-muted);font-size:0.85rem;">No results found</div>';
        results.style.display = "block";
        return;
      }

      var html = "";
      for (var k = 0; k < matches.length; k++) {
        var m = matches[k];
        html += '<a href="' + escapeHTML(root + m.p) + '" class="search-item">' +
          '<div class="search-item-title">' + escapeHTML(m.t) +
          (m.y ? '<span class="search-item-type">' + escapeHTML(m.y) + '</span>' : "") +
          '</div>' +
          (m.s ? '<div class="search-item-snippet">' + escapeHTML(m.s) + '</div>' : "") +
          '</a>';
      }
      results.innerHTML = html;
      results.style.display = "block";
    });

    document.addEventListener("click", function (e) {
      if (!results.contains(e.target) && e.target !== input) {
        results.style.display = "none";
      }
    });

    input.addEventListener("focus", function () {
      if (input.value.trim().length > 0 && results.innerHTML.length > 0) {
        results.style.display = "block";
      }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initSearch);
  } else {
    initSearch();
  }
})();
`
