package slug

import (
	"regexp"
	"strings"
	"testing"
)

// segmentRE is internal/stage's vaultPathSegmentRE literal (internal/stage/
// validate.go) duplicated here — slug is a leaf package and must not import
// stage — so every Make result is held to the exact shape the validator
// enforces on a raw-source path segment. The regex itself also pins the
// stronger property the second-look review cares about: no leading "-",
// no trailing "-", no "--" anywhere.
var segmentRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestMake(t *testing.T) {
	t.Run("table", func(t *testing.T) {
		// Every A-805 "resulting names" pair (008 contract §12), plus the
		// extra pairs the MASTER §5 R-808 assertions name.
		cases := []struct{ in, want string }{
			{"Quaternion 四元數簡介", "quaternion"},
			{"Quaternion Notes", "quaternion-notes"},
			{"四元數", ""},
			{"Café Déjà Vu", "cafe-deja-vu"},
			{"🚀 Rocket Notes 🚀", "rocket-notes"},
			{"Straße", "strasse"},
			{"Ｑｕａｔｅｒｎｉｏｎ", "quaternion"},
			{"İstanbul", "istanbul"},
			{"  --a--  ", "a"},
			{"", ""},
			// The rest of the nine-letter transliteration table.
			{"Ærø", "aero"},
			{"Łódź", "lodz"},
			{"Þór", "thor"},
			{"đañı", "dani"},
		}
		for _, tc := range cases {
			if got := Make(tc.in); got != tc.want {
				t.Errorf("Make(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	t.Run("output_is_a_valid_segment_or_empty", func(t *testing.T) {
		corpus := []string{
			// CJK, kana, Hangul, Cyrillic, Arabic — no Latin letters at all.
			"四元數簡介",
			"日本語のタイトル",
			"カタカナのテスト",
			"한국어 제목",
			"Русский заголовок",
			"عنوان عربي",
			// Emoji, including ZWJ sequences.
			"🚀",
			"👩‍🚀 launch",
			"👨‍👩‍👧‍👦 family notes",
			"🏁🚀🏁",
			// Combining marks, both precomposed and decomposed.
			"é",
			"ééé",
			"áb́ć",
			"Ångström",
			// Full-width forms.
			"Ｑｕａｔｅｒｎｉｏｎ",
			"ＡＢＣ１２３",
			// Control characters.
			"\x00\x01\x02",
			"\t\n\r x \v\f",
			// Path separators and dot segments.
			"a/b",
			"a//b",
			"/leading/trailing/",
			".",
			"..",
			"a/./b",
			"../..",
			"./../../..",
			// Punctuation and separators only.
			"!!!???",
			"---",
			" _underscore_ ",
			"  --a--  ",
			// Ordinary text, very long input in both scripts.
			"CamelCase123 and mixed   spaces",
			"Straße and Ærø and Łódź",
			strings.Repeat("ab ", 1000),
			strings.Repeat("四", 5000),
			strings.Repeat("é", 2000),
		}
		for _, in := range corpus {
			got := Make(in)
			if got == "" {
				continue // callers own the fallback; Make may give up
			}
			if !segmentRE.MatchString(got) {
				t.Errorf("Make(%q) = %q, which is neither \"\" nor a vaultPathSegmentRE match", clip(in), got)
			}
		}
	})
}

// clip shortens an input for a failure message, so a 5000-rune corpus entry
// does not flood the log.
func clip(s string) string {
	const n = 40
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
