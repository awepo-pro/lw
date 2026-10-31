package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// noteOpenVault is the vault opener seam, replaced in tests (F7).
var noteOpenVault = vault.Open

// noteMatchesSource reports whether a raw's sourceURL corresponds to the note
// identified by stem in the vault at root (I4).
//
// A raw matches a note iff sourceURL is an absolute local path equal to
// <root>/notes/<stem>.md (SameSource) or ends in exactly /notes/<stem>.md
// (one path segment after notes). URLs, relative paths, other stems and
// deeper paths never match.
func noteMatchesSource(root, stem, sourceURL string) bool {
	if sourceURL == "" || stem == "" {
		return false
	}
	if strings.Contains(sourceURL, "://") {
		return false
	}
	if !filepath.IsAbs(sourceURL) {
		return false
	}
	cleaned := filepath.Clean(sourceURL)
	if root != "" {
		absRoot, err := filepath.Abs(root)
		if err == nil {
			expected := filepath.Join(absRoot, vault.NotesDir, stem+".md")
			if vault.SameSource(cleaned, expected) {
				return true
			}
		}
	}
	cleanedSlash := filepath.ToSlash(cleaned)
	suffix := "/" + vault.NotesDir + "/" + stem + ".md"
	return strings.HasSuffix(cleanedSlash, suffix)
}

// noteDoneTags derives the distilled raw stem for each note stem among stems.
// Returns an empty map on any error or missing vault (D-3, F6).
func noteDoneTags(root string, stems []string) map[string]string {
	tags, err := noteDoneTagsErr(root, stems)
	if err != nil {
		return make(map[string]string)
	}
	return tags
}

// noteDoneTagsErr derives the distilled raw stem for each note stem among stems.
// If opening the vault fails, it returns a non-nil error.
func noteDoneTagsErr(root string, stems []string) (map[string]string, error) {
	tags := make(map[string]string)
	if len(stems) == 0 {
		return tags, nil
	}
	v, err := noteOpenVault(root)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("open vault %s: nil vault", root)
	}
	allRaws := v.RawSources()
	if len(allRaws) == 0 {
		return tags, nil
	}
	for _, stem := range stems {
		if stem == "" {
			continue
		}
		var distinctURLs []string
		seenURLs := make(map[string]bool)
		for _, r := range allRaws {
			if r != nil && noteMatchesSource(root, stem, r.SourceURL) {
				if !seenURLs[r.SourceURL] {
					seenURLs[r.SourceURL] = true
					distinctURLs = append(distinctURLs, r.SourceURL)
				}
			}
		}
		if len(distinctURLs) == 0 {
			continue
		}
		var bestTip *vault.RawSource
		for _, url := range distinctURLs {
			tip, ok := v.RevisionTip(url)
			if !ok || tip == nil {
				continue
			}
			if !noteMatchesSource(root, stem, tip.SourceURL) {
				continue
			}
			if bestTip == nil {
				bestTip = tip
			} else if tip.Ingested.After(bestTip.Ingested.Time) {
				bestTip = tip
			} else if tip.Ingested.Time.Equal(bestTip.Ingested.Time) {
				if tip.Path > bestTip.Path {
					bestTip = tip
				}
			}
		}
		if bestTip != nil {
			tags[stem] = strings.TrimSuffix(filepath.Base(bestTip.Path), ".md")
		}
	}
	return tags, nil
}
