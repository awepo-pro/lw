package main

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
	"gopkg.in/yaml.v3"
)

// resolvePageArg finds the page a user named: vault.Resolve (path, path without .md, basename),
// else case-insensitive exact title, else case-insensitive exact alias (frontmatter aliases).
// It returns the page, or ambiguous candidates (sorted by path) when a title/alias matches several pages, or neither.
func resolvePageArg(v *vault.Vault, arg string) (page *vault.Page, ambiguous []*vault.Page) {
	if resolvedPath, ok := vault.Resolve(v, arg); ok {
		if p, ok := v.Page(resolvedPath); ok {
			return p, nil
		}
	}

	var baseMatches []*vault.Page
	cleanArg := strings.TrimSuffix(strings.TrimSpace(arg), ".md")
	if !strings.Contains(cleanArg, "/") {
		for _, p := range v.Pages() {
			base := strings.TrimSuffix(filepath.Base(p.Path), ".md")
			if strings.EqualFold(base, cleanArg) {
				baseMatches = append(baseMatches, p)
			}
		}
	}
	if len(baseMatches) > 1 {
		sort.Slice(baseMatches, func(i, j int) bool {
			return baseMatches[i].Path < baseMatches[j].Path
		})
		return nil, baseMatches
	}

	var titleMatches []*vault.Page
	for _, p := range v.Pages() {
		if strings.EqualFold(strings.TrimSpace(p.FM.Title), arg) {
			titleMatches = append(titleMatches, p)
		}
	}
	if len(titleMatches) == 1 {
		return titleMatches[0], nil
	} else if len(titleMatches) > 1 {
		sort.Slice(titleMatches, func(i, j int) bool {
			return titleMatches[i].Path < titleMatches[j].Path
		})
		return nil, titleMatches
	}

	var aliasMatches []*vault.Page
	for _, p := range v.Pages() {
		for _, a := range extractAliases(p) {
			if strings.EqualFold(strings.TrimSpace(a), arg) {
				aliasMatches = append(aliasMatches, p)
				break
			}
		}
	}
	if len(aliasMatches) == 1 {
		return aliasMatches[0], nil
	} else if len(aliasMatches) > 1 {
		sort.Slice(aliasMatches, func(i, j int) bool {
			return aliasMatches[i].Path < aliasMatches[j].Path
		})
		return nil, aliasMatches
	}

	return nil, nil
}

// extractAliases returns all aliases defined in the page frontmatter,
// inspecting both ExtraYAML["aliases"] and Extra["aliases"].
func extractAliases(p *vault.Page) []string {
	var aliases []string
	if rawYAML, ok := p.FM.ExtraYAML["aliases"]; ok && strings.TrimSpace(rawYAML) != "" {
		var list []string
		if err := yaml.Unmarshal([]byte(rawYAML), &list); err == nil {
			aliases = append(aliases, list...)
		} else {
			var single string
			if err := yaml.Unmarshal([]byte(rawYAML), &single); err == nil && strings.TrimSpace(single) != "" {
				aliases = append(aliases, strings.TrimSpace(single))
			}
		}
	}
	if scalar, ok := p.FM.Extra["aliases"]; ok && strings.TrimSpace(scalar) != "" {
		aliases = append(aliases, strings.TrimSpace(scalar))
	}
	return aliases
}
