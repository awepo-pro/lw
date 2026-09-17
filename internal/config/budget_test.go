package config

import (
	"testing"
)

// TestDefaultMaxTokens pins contract §2b: the out-of-the-box budget is
// 32768, an explicit max_tokens in the user's file still wins, and
// MinRecommendedMaxTokens is the 16000 floor lw doctor warns below.
func TestDefaultMaxTokens(t *testing.T) {
	t.Run("default_is_32768", func(t *testing.T) {
		if got := Default().LLM.MaxTokens; got != 32768 {
			t.Errorf("Default().LLM.MaxTokens = %d, want 32768", got)
		}
	})

	t.Run("explicit_value_wins", func(t *testing.T) {
		dir := withConfigDir(t)
		writeConfig(t, dir, `[llm]
max_tokens = 8192
`)

		got, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.LLM.MaxTokens != 8192 {
			t.Errorf("MaxTokens = %d, want the file's 8192", got.LLM.MaxTokens)
		}
	})

	t.Run("min_recommended_is_16000", func(t *testing.T) {
		if MinRecommendedMaxTokens != 16000 {
			t.Errorf("MinRecommendedMaxTokens = %d, want 16000", MinRecommendedMaxTokens)
		}
	})
}
