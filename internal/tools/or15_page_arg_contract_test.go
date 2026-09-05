package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// MASTER §10 OR-15. The `page` argument resolves through vault.Resolve,
// which matches an exact path, "<target>.md", a case-insensitive basename
// and "<dir>/<target>.md" — but NEVER a page's frontmatter title
// (backbone §2.9).
//
// wiki.search shows the model every hit as "<path> — <Title>", so a schema
// description or error message promising that a title resolves walks the
// model into search -> title -> not found -> search, burning rounds against
// S5-T3's cap on the most-used tool in the set. These tests pin the
// model-facing text to what the code actually does.

// The measurement OR-15 came from: most titles do NOT resolve. If this ever
// starts passing for every page, title resolution has been added and the
// descriptions may say so again.
func TestPageTitlesMostlyDoNotResolve(t *testing.T) {
	dir := testutil.CopyFixture(t, "minimal")
	v, err := vault.Open(dir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	reg := NewRegistry(newTestDeps(t, dir))

	var failed int
	for _, p := range v.Pages() {
		if p.FM.Title == "" {
			continue
		}
		args, _ := json.Marshal(map[string]string{"page": p.FM.Title})
		res, err := reg.Call(context.Background(), "wiki.get", args)
		if err != nil {
			t.Fatalf("wiki.get(%q): unexpected go error %v", p.FM.Title, err)
		}
		if res.IsError {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("every title resolved — vault.Resolve gained title matching; revisit OR-15's wording")
	}
	t.Logf("%d page title(s) do not resolve, as vault.Resolve's contract implies", failed)
}

// The model-facing text must not advertise title resolution.
func TestPageArgTextDoesNotPromiseTitleResolution(t *testing.T) {
	reg := minimalRegistry(t)
	for _, name := range []string{"wiki.get", "wiki.neighbors", "wiki.backlinks"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		desc := schema.Properties["page"].Description
		if desc == "" {
			t.Fatalf("%s: page property has no description", name)
		}
		if strings.Contains(desc, "title/basename") {
			t.Errorf("%s page description still offers title resolution: %q", name, desc)
		}
		if !strings.Contains(strings.ToLower(desc), "basename") {
			t.Errorf("%s page description must name the basename form: %q", name, desc)
		}
	}
}

// Both failure messages must point at the basename/path, not the title.
func TestPageArgFailureMessagesPointAtThePath(t *testing.T) {
	reg := minimalRegistry(t)
	ctx := context.Background()

	missing, err := reg.Call(ctx, "wiki.get", json.RawMessage(`{"page":"  "}`))
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !missing.IsError {
		t.Fatal("a blank page argument must be IsError")
	}
	if strings.Contains(missing.Content, "path or title") {
		t.Errorf("missing-arg message still says %q", missing.Content)
	}

	notFound, err := reg.Call(ctx, "wiki.get", json.RawMessage(`{"page":"KV Cache"}`))
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if !notFound.IsError {
		t.Fatal("a title must not resolve; if it now does, revisit OR-15")
	}
	if strings.Contains(notFound.Content, "exact path or title") {
		t.Errorf("not-found message still tells the model to search for a title: %q", notFound.Content)
	}
	if !strings.Contains(strings.ToLower(notFound.Content), "basename") {
		t.Errorf("not-found message must name the basename form: %q", notFound.Content)
	}
	t.Logf("not-found message: %s", notFound.Content)
}
