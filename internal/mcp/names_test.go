package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNameBijection(t *testing.T) {
	reg := tools.NewRegistry(tools.Deps{})
	if len(reg.List()) < 1 {
		t.Fatal("registry has no tools")
	}
	for _, tool := range reg.List() {
		mcpName := MCPName(tool.Name)
		if got := CanonicalName(mcpName); got != tool.Name {
			t.Errorf("CanonicalName(MCPName(%q)) = %q, want %q", tool.Name, got, tool.Name)
		}
	}
}

func TestNameMappingIsTotal(t *testing.T) {
	for _, test := range []struct {
		canonical string
		mcp       string
	}{
		{canonical: "wiki.search", mcp: "wiki_search"},
		{canonical: "stage.create_page", mcp: "stage_create_page"},
		{canonical: "", mcp: ""},
	} {
		if got := MCPName(test.canonical); got != test.mcp {
			t.Errorf("MCPName(%q) = %q, want %q", test.canonical, got, test.mcp)
		}
		if got := CanonicalName(test.mcp); got != test.canonical {
			t.Errorf("CanonicalName(%q) = %q, want %q", test.mcp, got, test.canonical)
		}
	}
}

func TestServerSchemasPassthrough(t *testing.T) {
	ctx := context.Background()
	reg := tools.NewRegistry(tools.Deps{})
	server := NewServer(reg, "test")
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := reg.List()
	if len(result.Tools) != len(want) {
		t.Fatalf("ListTools returned %d tools, want %d", len(result.Tools), len(want))
	}
	for i, got := range result.Tools {
		wantTool := want[i]
		if got.Name != MCPName(wantTool.Name) {
			t.Errorf("tool %d name = %q, want %q", i, got.Name, MCPName(wantTool.Name))
		}
		if got.Description != wantTool.Description {
			t.Errorf("tool %q description changed", got.Name)
		}
		var gotSchema, wantSchema any
		if err := json.Unmarshal(mustJSON(got.InputSchema), &gotSchema); err != nil {
			t.Fatalf("tool %q returned invalid schema: %v", got.Name, err)
		}
		if err := json.Unmarshal(wantTool.Schema, &wantSchema); err != nil {
			t.Fatalf("registry schema %q invalid: %v", wantTool.Name, err)
		}
		if !reflect.DeepEqual(gotSchema, wantSchema) {
			t.Errorf("tool %q schema changed", got.Name)
		}
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
