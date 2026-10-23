package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

func TestAskTurnOffersAskToolNames(t *testing.T) {
	reg := tools.NewRegistry(tools.Deps{})
	loop := &Loop{tools: reg}

	// 1. query-plan turn advertises tools.AskToolNames() via toolsFor
	ttQuery := loop.toolsFor(planFor(trace.VerbQuery))
	queryNames := make([]string, 0, len(ttQuery.defs))
	for _, d := range ttQuery.defs {
		queryNames = append(queryNames, tools.CanonicalName(d.Name))
	}
	want := tools.AskToolNames()
	if !reflect.DeepEqual(queryNames, want) {
		t.Fatalf("query plan tools =\n%v\nwant\n%v", queryNames, want)
	}

	// 2. ask-plan turn without web.search in the registry offers the same
	ttAsk := loop.toolsFor(planFor(trace.VerbAsk))
	askNames := make([]string, 0, len(ttAsk.defs))
	for _, d := range ttAsk.defs {
		askNames = append(askNames, tools.CanonicalName(d.Name))
	}
	if !reflect.DeepEqual(askNames, want) {
		t.Fatalf("ask plan (without web.search) tools =\n%v\nwant\n%v", askNames, want)
	}
}

func TestAskSystemPromptIsQueryPrompt(t *testing.T) {
	got := AskSystemPrompt()

	// 1. AskSystemPrompt() == askPromptFor(false)
	if got != askPromptFor(false) {
		t.Fatalf("AskSystemPrompt() != askPromptFor(false)")
	}

	// 2. It does not contain web.search
	if strings.Contains(got, "web.search") {
		t.Fatalf("AskSystemPrompt() unexpectedly contains \"web.search\"")
	}

	// 3. It equals the system message a query-verb turn's ContextBuilder builds
	v, _ := newTestVault(t)
	reg := tools.NewRegistry(tools.Deps{Vault: v})
	cb := NewContextBuilder(v, reg, 100_000)

	session := &Session{ID: "test-session"}
	msgs, err := cb.buildFor(session, "what is in this vault?", planFor(trace.VerbQuery))
	if err != nil {
		t.Fatalf("buildFor(query): %v", err)
	}
	if len(msgs) == 0 {
		t.Fatalf("buildFor returned empty messages")
	}
	if msgs[0].Role != "system" {
		t.Fatalf("msgs[0].Role = %q, want system", msgs[0].Role)
	}
	if msgs[0].Content != got {
		t.Fatalf("ContextBuilder query system message =\n%q\nwant AskSystemPrompt() =\n%q", msgs[0].Content, got)
	}
}
