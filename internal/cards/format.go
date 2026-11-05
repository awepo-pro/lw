package cards

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
)

// WriteAnkiTSV writes cards in Anki 2.1.55+ text import TSV format with headers.
func WriteAnkiTSV(w io.Writer, cards []Card, deck string) error {
	headers := []string{
		"#separator:tab",
		"#html:true",
		"#notetype:Basic",
		"#deck:" + deck,
		"#guid column:1",
		"#tags column:4",
	}
	for _, h := range headers {
		if _, err := fmt.Fprintln(w, h); err != nil {
			return err
		}
	}

	for _, c := range cards {
		frontHTML := html.EscapeString(c.Front)
		frontHTML = strings.ReplaceAll(frontHTML, "\n", "<br>")
		frontHTML = strings.ReplaceAll(frontHTML, "\t", " ")

		backHTML := html.EscapeString(c.Back)
		backHTML = strings.ReplaceAll(backHTML, "\n", "<br>")
		backHTML = strings.ReplaceAll(backHTML, "\t", " ")
		backHTML += "<br><small>lw: " + html.EscapeString(c.Path) + "</small>"

		tagsStr := strings.Join(c.Tags, " ")
		tagsStr = strings.ReplaceAll(tagsStr, "\t", " ")
		tagsStr = strings.ReplaceAll(tagsStr, "\n", " ")

		idStr := c.ID
		idStr = strings.ReplaceAll(idStr, "\t", "")
		idStr = strings.ReplaceAll(idStr, "\n", "")

		line := fmt.Sprintf("%s\t%s\t%s\t%s\n", idStr, frontHTML, backHTML, tagsStr)
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	return nil
}

// WriteMarkdown writes cards in the Obsidian Spaced Repetition plugin multi-line format, grouped by page.
func WriteMarkdown(w io.Writer, cards []Card) error {
	if len(cards) == 0 {
		return nil
	}

	type pageGroup struct {
		title    string
		pageType string
		cards    []Card
	}

	var groups []pageGroup
	groupMap := make(map[string]int)

	for _, c := range cards {
		idx, exists := groupMap[c.Path]
		if !exists {
			idx = len(groups)
			groupMap[c.Path] = idx
			pageType := "default"
			if len(c.Tags) > 1 {
				pageType = c.Tags[1]
			}
			groups = append(groups, pageGroup{
				title:    c.Title,
				pageType: pageType,
				cards:    nil,
			})
		}
		groups[idx].cards = append(groups[idx].cards, c)
	}

	for gi, g := range groups {
		if gi > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		header := fmt.Sprintf("#flashcards/lw/%s\n## %s\n", g.pageType, g.title)
		if _, err := io.WriteString(w, header); err != nil {
			return err
		}
		for ci, c := range g.cards {
			if ci > 0 {
				if _, err := io.WriteString(w, "\n"); err != nil {
					return err
				}
			}
			cardBlock := fmt.Sprintf("%s\n?\n%s\n<!-- lw:%s -->\n", c.Front, c.Back, c.ID)
			if _, err := io.WriteString(w, cardBlock); err != nil {
				return err
			}
		}
	}
	return nil
}

// WriteJSON writes cards as an indented JSON array, or [] when none.
func WriteJSON(w io.Writer, cards []Card) error {
	if cards == nil {
		cards = []Card{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(cards)
}
