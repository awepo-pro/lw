package ledger

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Report renders one month. today is "YYYY-MM-DD".
func Report(b Book, month string, today string) string {
	// Filter entries for this month
	var monthEntries []Entry
	for _, e := range b.Entries {
		if e.Month == month {
			monthEntries = append(monthEntries, e)
		}
	}

	if len(monthEntries) == 0 {
		if len(b.Entries) == 0 {
			return fmt.Sprintf("no entries in %s\n", month)
		}
		var latest string
		for _, e := range b.Entries {
			if e.Month > latest {
				latest = e.Month
			}
		}
		return fmt.Sprintf("no entries in %s\nlatest month with entries: %s\n", month, latest)
	}

	// Distinct currencies in monthEntries
	curMap := make(map[string]bool)
	for _, e := range monthEntries {
		curMap[e.Currency] = true
	}
	var curList []string
	for c := range curMap {
		curList = append(curList, c)
	}
	sort.Slice(curList, func(i, j int) bool {
		if curList[i] == "" {
			return true
		}
		if curList[j] == "" {
			return false
		}
		return curList[i] < curList[j]
	})

	var blocks []string
	todayMonth := ""
	if len(today) >= 7 {
		todayMonth = today[:7]
	}

	for _, cur := range curList {
		var curEntries []Entry
		for _, e := range monthEntries {
			if e.Currency == cur {
				curEntries = append(curEntries, e)
			}
		}

		var block strings.Builder

		// Line 1: month and optional currency
		if cur == "" {
			block.WriteString(fmt.Sprintf("%s\n", month))
		} else {
			block.WriteString(fmt.Sprintf("%s %s\n", month, cur))
		}

		// Line 2: spent
		var spentCents int64
		var spentItems int
		spentDates := make(map[string]bool)
		for _, e := range curEntries {
			if (e.Kind == KindDay || e.Kind == KindV1) && e.Cents < 0 {
				spentCents += -e.Cents
				spentItems++
				if e.Date != "" {
					spentDates[e.Date] = true
				}
			}
		}
		daysCount := len(spentDates)
		block.WriteString(fmt.Sprintf("spent %s (%d item(s) on %d day(s))\n", FormatCents(spentCents), spentItems, daysCount))

		// Line 3: fixed
		var fixedCents int64
		var fixedParts []string
		for _, e := range curEntries {
			if e.Kind == KindFixed && e.Cents < 0 {
				fixedCents += -e.Cents
				fixedParts = append(fixedParts, fmt.Sprintf("%s %s", e.Note, FormatCents(-e.Cents)))
			}
		}
		if len(fixedParts) == 0 {
			block.WriteString("fixed 0.00\n")
		} else {
			block.WriteString(fmt.Sprintf("fixed %s (%s)\n", FormatCents(fixedCents), strings.Join(fixedParts, ", ")))
		}

		// Line 4: money in
		var moneyInCents int64
		for _, e := range curEntries {
			if e.Cents > 0 {
				moneyInCents += e.Cents
			}
		}
		block.WriteString(fmt.Sprintf("money in %s\n", FormatCents(moneyInCents)))

		// Line 5: net out
		netOutCents := spentCents + fixedCents - moneyInCents
		block.WriteString(fmt.Sprintf("net out %s\n", FormatCents(netOutCents)))

		// Budget: only in block of loose currency and only when BudgetCents > 0
		if cur == b.Currency && b.BudgetCents > 0 {
			if spentCents <= b.BudgetCents {
				left := b.BudgetCents - spentCents
				block.WriteString(fmt.Sprintf("budget %s: %s left\n", FormatCents(b.BudgetCents), FormatCents(left)))
			} else {
				over := spentCents - b.BudgetCents
				block.WriteString(fmt.Sprintf("budget %s: %s over\n", FormatCents(b.BudgetCents), FormatCents(over)))
			}
		}

		// By category
		type catTotal struct {
			name  string
			cents int64
			count int
		}
		catMap := make(map[string]*catTotal)
		var hasNegative bool
		for _, e := range curEntries {
			if e.Cents < 0 {
				hasNegative = true
				catName := e.Category
				if catName == "" {
					catName = "uncategorized"
				}
				ct, ok := catMap[catName]
				if !ok {
					ct = &catTotal{name: catName}
					catMap[catName] = ct
				}
				ct.cents += -e.Cents
				ct.count++
			}
		}
		if hasNegative {
			var catList []*catTotal
			for _, ct := range catMap {
				catList = append(catList, ct)
			}
			sort.Slice(catList, func(i, j int) bool {
				if catList[i].cents != catList[j].cents {
					return catList[i].cents > catList[j].cents
				}
				return catList[i].name < catList[j].name
			})
			block.WriteString("by category:\n")
			for _, ct := range catList {
				block.WriteString(fmt.Sprintf("  %s %s (%d)\n", ct.name, FormatCents(ct.cents), ct.count))
			}
		}

		// Average per recorded day
		if daysCount > 0 {
			avgCents := (spentCents + int64(daysCount/2)) / int64(daysCount)
			block.WriteString(fmt.Sprintf("average per recorded day %s\n", FormatCents(avgCents)))
		}

		// No entry line
		recordedDatesMap := make(map[string]bool)
		for _, e := range curEntries {
			if (e.Kind == KindDay || e.Kind == KindV1) && e.Date != "" {
				recordedDatesMap[e.Date] = true
			}
		}

		if len(recordedDatesMap) > 0 && todayMonth != "" && month <= todayMonth {
			var recDates []string
			for d := range recordedDatesMap {
				recDates = append(recDates, d)
			}
			sort.Strings(recDates)
			firstDate := recDates[0]
			startDay, err1 := strconv.Atoi(firstDate[8:10])

			var endDay int
			var err2 error
			if month == todayMonth {
				if len(today) >= 10 {
					endDay, err2 = strconv.Atoi(today[8:10])
				}
			} else {
				t, tErr := time.Parse("2006-01", month)
				if tErr == nil {
					endDay = t.AddDate(0, 1, -1).Day()
				}
			}

			if err1 == nil && err2 == nil && endDay >= startDay {
				var missingDays []string
				for d := startDay; d <= endDay; d++ {
					dStr := fmt.Sprintf("%s-%02d", month, d)
					if !recordedDatesMap[dStr] {
						missingDays = append(missingDays, fmt.Sprintf("%02d", d))
					}
				}
				if len(missingDays) > 0 {
					block.WriteString(fmt.Sprintf("no entry on %d day(s): %s\n", len(missingDays), strings.Join(missingDays, " ")))
				}
			}
		}

		blocks = append(blocks, block.String())
	}

	var trimmedBlocks []string
	for _, blk := range blocks {
		trimmedBlocks = append(trimmedBlocks, strings.TrimRight(blk, "\n"))
	}
	return strings.Join(trimmedBlocks, "\n\n") + "\n"
}
