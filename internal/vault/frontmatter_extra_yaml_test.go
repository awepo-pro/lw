package vault

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// decodeYAMLMap decodes YAML bytes into a generic map for comparison.
func decodeYAMLMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("yaml.Unmarshal: %v\ncontent:\n%s", err, string(b))
	}
	return m
}

// F1 flow list - aliases: [a, b] (+ the 7 known keys): after ParseFrontmatter then Encode,
// the emitted block's aliases value decodes to []any{"a","b"}; fm.Extra has no aliases;
// fm.ExtraYAML["aliases"] is non-empty.
func TestFrontmatterExtra_F1_FlowList(t *testing.T) {
	in := "---\n" +
		"title: Test Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"sources: [s1]\n" +
		"confidence: high\n" +
		"contested: true\n" +
		"aliases: [a, b]\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	if _, ok := fm.Extra["aliases"]; ok {
		t.Fatalf("fm.Extra has aliases: %#v", fm.Extra)
	}
	if fm.ExtraYAML["aliases"] == "" {
		t.Fatalf("fm.ExtraYAML[aliases] is empty")
	}

	encoded := fm.Encode()
	m := decodeYAMLMap(t, encoded)
	wantAliases := []any{"a", "b"}
	if !reflect.DeepEqual(m["aliases"], wantAliases) {
		t.Fatalf("m[aliases] = %#v, want %#v", m["aliases"], wantAliases)
	}
}

// F2 block list - cssclasses:\n  - wide\n  - dark -> value []any{"wide","dark"} after the round trip.
func TestFrontmatterExtra_F2_BlockList(t *testing.T) {
	in := "---\n" +
		"title: Test Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"cssclasses:\n" +
		"  - wide\n" +
		"  - dark\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	encoded := fm.Encode()
	m := decodeYAMLMap(t, encoded)
	want := []any{"wide", "dark"}
	if !reflect.DeepEqual(m["cssclasses"], want) {
		t.Fatalf("m[cssclasses] = %#v, want %#v", m["cssclasses"], want)
	}
}

// F3 nested map - meta:\n  owner: me\n  list: [x, y]\n  deep:\n    k: v -> decodes to the same nested value after the round trip.
func TestFrontmatterExtra_F3_NestedMap(t *testing.T) {
	in := "---\n" +
		"title: Test Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"meta:\n" +
		"  owner: me\n" +
		"  list: [x, y]\n" +
		"  deep:\n" +
		"    k: v\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	encoded := fm.Encode()
	m := decodeYAMLMap(t, encoded)
	want := map[string]any{
		"owner": "me",
		"list":  []any{"x", "y"},
		"deep": map[string]any{
			"k": "v",
		},
	}
	if !reflect.DeepEqual(m["meta"], want) {
		t.Fatalf("m[meta] = %#v, want %#v", m["meta"], want)
	}
}

// F4 fixpoint helper
func checkF4Fixpoint(t *testing.T, name, doc string) {
	t.Helper()
	fm1, _, err := ParseFrontmatter([]byte(doc))
	if err != nil {
		t.Fatalf("[%s] Parse 1: %v", name, err)
	}
	enc1 := fm1.Encode()

	fm2, _, err := ParseFrontmatter(enc1)
	if err != nil {
		t.Fatalf("[%s] Parse 2: %v", name, err)
	}
	enc2 := fm2.Encode()

	if string(enc1) != string(enc2) {
		t.Fatalf("[%s] Encode fixpoint mismatch:\n--- enc1:\n%s\n--- enc2:\n%s", name, enc1, enc2)
	}

	fm3, _, err := ParseFrontmatter(enc2)
	if err != nil {
		t.Fatalf("[%s] Parse 3: %v", name, err)
	}
	if !reflect.DeepEqual(fm2, fm3) {
		t.Fatalf("[%s] DeepEqual mismatch:\nfm2=%#v\nfm3=%#v", name, fm2, fm3)
	}
}

// F4 fixpoint - for each of F1-F3 and a mixed page (known keys + scalar extras incl. a quoted "a: b" + two composite keys):
// Encode(Parse(Encode(Parse(x)))) == Encode(Parse(x)) byte for byte, and Parse(Encode(fm)) equals fm under reflect.DeepEqual on all fields.
func TestFrontmatterExtra_F4_Fixpoint(t *testing.T) {
	f1Doc := "---\n" +
		"title: Test Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"sources: [s1]\n" +
		"confidence: high\n" +
		"contested: true\n" +
		"aliases: [a, b]\n" +
		"---\n\nbody\n"

	f2Doc := "---\n" +
		"title: Test Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"cssclasses:\n" +
		"  - wide\n" +
		"  - dark\n" +
		"---\n\nbody\n"

	f3Doc := "---\n" +
		"title: Test Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"meta:\n" +
		"  owner: me\n" +
		"  list: [x, y]\n" +
		"  deep:\n" +
		"    k: v\n" +
		"---\n\nbody\n"

	mixedDoc := "---\n" +
		"title: Mixed Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"tags: [inference]\n" +
		"sources: [s1]\n" +
		"confidence: high\n" +
		"contested: true\n" +
		"alpha_scalar: first\n" +
		"quoted: \"a: b\"\n" +
		"aliases: [a, b]\n" +
		"cssclasses:\n" +
		"  - wide\n" +
		"  - dark\n" +
		"zeta_scalar: last\n" +
		"---\n\nbody\n"

	t.Run("F1", func(t *testing.T) {
		checkF4Fixpoint(t, "F1", f1Doc)
	})
	t.Run("F2", func(t *testing.T) {
		checkF4Fixpoint(t, "F2", f2Doc)
	})
	t.Run("F3", func(t *testing.T) {
		checkF4Fixpoint(t, "F3", f3Doc)
	})
	t.Run("mixed", func(t *testing.T) {
		checkF4Fixpoint(t, "mixed", mixedDoc)
	})
}

// F5 order - unknown keys (scalar and composite) interleave sorted by key in the output (aliases, author, cssclasses, zeta).
func TestFrontmatterExtra_F5_Order(t *testing.T) {
	in := "---\n" +
		"title: T\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"zeta: last\n" +
		"cssclasses:\n" +
		"  - wide\n" +
		"author: Alice\n" +
		"aliases: [a, b]\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	out := string(fm.Encode())

	idxAliases := strings.Index(out, "aliases:")
	idxAuthor := strings.Index(out, "author:")
	idxCSS := strings.Index(out, "cssclasses:")
	idxZeta := strings.Index(out, "zeta:")

	if idxAliases == -1 || idxAuthor == -1 || idxCSS == -1 || idxZeta == -1 {
		t.Fatalf("missing key in output:\n%s", out)
	}
	if !(idxAliases < idxAuthor && idxAuthor < idxCSS && idxCSS < idxZeta) {
		t.Fatalf("keys not sorted:\naliases=%d, author=%d, cssclasses=%d, zeta=%d\nOutput:\n%s",
			idxAliases, idxAuthor, idxCSS, idxZeta, out)
	}
}

// F6 stage carry - retractTombstone-style copy: a test in internal/vault cannot call stage,
// so instead assert fm2 := fm; fm2.Extra = copy+"retracted" then fm2.Encode() still emits
// the composite key (the struct-copy property) and an internal/stage test is NOT added.
func TestFrontmatterExtra_F6_StageCarry(t *testing.T) {
	in := "---\n" +
		"title: T\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"aliases: [a, b]\n" +
		"author: Alice\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}

	fm2 := fm
	extraCopy := make(map[string]string, len(fm.Extra)+1)
	for k, v := range fm.Extra {
		extraCopy[k] = v
	}
	extraCopy["retracted"] = "yes"
	fm2.Extra = extraCopy

	out := fm2.Encode()
	m := decodeYAMLMap(t, out)

	if m["retracted"] != "yes" {
		t.Fatalf("m[retracted] = %#v, want %q", m["retracted"], "yes")
	}
	wantAliases := []any{"a", "b"}
	if !reflect.DeepEqual(m["aliases"], wantAliases) {
		t.Fatalf("m[aliases] = %#v, want %#v", m["aliases"], wantAliases)
	}
}

// F7 scalars unchanged - a page whose unknown keys are all scalars encodes byte-identically
// to the base binary's output (golden string literal in the test taken from base BEFORE the change).
func TestFrontmatterExtra_F7_ScalarsUnchanged(t *testing.T) {
	in := "---\n" +
		"title: Scalar Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"tags: [foo, bar]\n" +
		"sources: [s1]\n" +
		"confidence: high\n" +
		"contested: true\n" +
		"author: Alice\n" +
		"empty:\n" +
		"note: \"a: b\"\n" +
		"priority: 2\n" +
		"zeta: last\n" +
		"---\n\nBody text.\n"

	wantGolden := "---\n" +
		"title: Scalar Page\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-02\n" +
		"type: concept\n" +
		"tags: [foo, bar]\n" +
		"sources: [s1]\n" +
		"confidence: high\n" +
		"contested: true\n" +
		"author: Alice\n" +
		"empty: \n" +
		"note: \"a: b\"\n" +
		"priority: 2\n" +
		"zeta: last\n" +
		"---\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	got := string(fm.Encode())
	if got != wantGolden {
		t.Fatalf("F7 Encode mismatch:\n got:\n%s\nwant:\n%s", got, wantGolden)
	}
}

// F8 null/empty - empty: (null) stays in Extra as before; a sequence containing null or an empty [] survives as a composite.
func TestFrontmatterExtra_F8_NullEmpty(t *testing.T) {
	in := "---\n" +
		"title: T\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"empty:\n" +
		"empty_seq: []\n" +
		"seq_null: [null]\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}

	if _, ok := fm.Extra["empty"]; !ok {
		t.Fatalf("fm.Extra missing 'empty': %#v", fm.Extra)
	}
	if _, ok := fm.ExtraYAML["empty"]; ok {
		t.Fatalf("fm.ExtraYAML has 'empty': %#v", fm.ExtraYAML)
	}

	if _, ok := fm.ExtraYAML["empty_seq"]; !ok {
		t.Fatalf("fm.ExtraYAML missing 'empty_seq': %#v", fm.ExtraYAML)
	}
	if _, ok := fm.ExtraYAML["seq_null"]; !ok {
		t.Fatalf("fm.ExtraYAML missing 'seq_null': %#v", fm.ExtraYAML)
	}

	encoded := fm.Encode()
	m := decodeYAMLMap(t, encoded)

	wantSeqNull := []any{nil}
	if !reflect.DeepEqual(m["seq_null"], wantSeqNull) {
		t.Fatalf("m[seq_null] = %#v, want %#v", m["seq_null"], wantSeqNull)
	}
	wantEmptySeq := []any{}
	if !reflect.DeepEqual(m["empty_seq"], wantEmptySeq) {
		t.Fatalf("m[empty_seq] = %#v, want %#v", m["empty_seq"], wantEmptySeq)
	}
}

// F9 known keys never in ExtraYAML; a malformed YAML still errors as before (TestParseFrontmatter* unchanged).
func TestFrontmatterExtra_F9_KnownKeysNeverInExtraYAML(t *testing.T) {
	in := "---\n" +
		"title: T\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"tags: [inference, decoding]\n" +
		"sources: [s1, s2]\n" +
		"confidence: high\n" +
		"contested: true\n" +
		"aliases: [a, b]\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}

	knownKeys := []string{"title", "created", "updated", "type", "tags", "sources", "confidence", "contested"}
	for _, k := range knownKeys {
		if _, ok := fm.ExtraYAML[k]; ok {
			t.Fatalf("fm.ExtraYAML contains known key %q", k)
		}
		if _, ok := fm.Extra[k]; ok {
			t.Fatalf("fm.Extra contains known key %q", k)
		}
	}

	malformed := "---\ntitle: [unterminated\n---\n\nbody\n"
	if _, _, err := ParseFrontmatter([]byte(malformed)); err == nil {
		t.Fatalf("ParseFrontmatter(malformed) = nil error, want error")
	}
}

// Additional Attempt 2 tests:

// TestFrontmatterExtra_StringListRoundTrip verifies that a list
// ['123', 'true', 'null', '2026-01-01', 'a: b', ”] keeps every element a STRING
// after Parse->Encode->Parse, and satisfies fixpoint Encode(Parse(Encode(Parse(x)))) == Encode(Parse(x)).
func TestFrontmatterExtra_StringListRoundTrip(t *testing.T) {
	in := "---\n" +
		"title: String List\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"items: ['123', 'true', 'null', '2026-01-01', 'a: b', '']\n" +
		"---\n\nbody\n"

	fm1, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	enc1 := fm1.Encode()

	fm2, _, err := ParseFrontmatter(enc1)
	if err != nil {
		t.Fatalf("ParseFrontmatter 2: %v", err)
	}
	enc2 := fm2.Encode()

	m := decodeYAMLMap(t, enc2)
	items, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("m[items] is not []any: %#v", m["items"])
	}
	want := []any{"123", "true", "null", "2026-01-01", "a: b", ""}
	for i, v := range items {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("items[%d] is %T (%#v), want string", i, v, v)
		}
		if s != want[i] {
			t.Fatalf("items[%d] = %q, want %q", i, s, want[i])
		}
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %#v, want %#v", items, want)
	}

	checkF4Fixpoint(t, "string_list", in)
}

// TestFrontmatterExtra_TypedListRoundTrip verifies that a list [1, 2.5, true, null]
// keeps its types after round-trip and satisfies fixpoint.
func TestFrontmatterExtra_TypedListRoundTrip(t *testing.T) {
	in := "---\n" +
		"title: Typed List\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"items: [1, 2.5, true, null]\n" +
		"---\n\nbody\n"

	fm1, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	enc1 := fm1.Encode()

	fm2, _, err := ParseFrontmatter(enc1)
	if err != nil {
		t.Fatalf("ParseFrontmatter 2: %v", err)
	}
	enc2 := fm2.Encode()

	m := decodeYAMLMap(t, enc2)
	items, ok := m["items"].([]any)
	if !ok {
		t.Fatalf("m[items] is not []any: %#v", m["items"])
	}
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4", len(items))
	}
	if items[0] != 1 {
		t.Fatalf("items[0] = %v (%T), want int 1", items[0], items[0])
	}
	if items[1] != 2.5 {
		t.Fatalf("items[1] = %v (%T), want float64 2.5", items[1], items[1])
	}
	if items[2] != true {
		t.Fatalf("items[2] = %v (%T), want bool true", items[2], items[2])
	}
	if items[3] != nil {
		t.Fatalf("items[3] = %v (%T), want nil", items[3], items[3])
	}

	checkF4Fixpoint(t, "typed_list", in)
}

// TestFrontmatterExtra_FlowStaysFlow verifies that flow sequences remain on a single line:
// aliases: [a, b] -> output line is exactly 'aliases: [a, b]', with fixpoint holding.
func TestFrontmatterExtra_FlowStaysFlow(t *testing.T) {
	in := "---\n" +
		"title: T\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"aliases: [a, b]\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	out := string(fm.Encode())
	lines := strings.Split(out, "\n")
	found := false
	for _, line := range lines {
		if line == "aliases: [a, b]" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected exact line %q in output, got:\n%s", "aliases: [a, b]", out)
	}

	checkF4Fixpoint(t, "flow_stays_flow", in)
}

// TestFrontmatterExtra_FlowMapAndNestedBlockMapRoundTrip verifies that a flow map
// and a nested block map round-trip correctly and satisfy fixpoint.
func TestFrontmatterExtra_FlowMapAndNestedBlockMapRoundTrip(t *testing.T) {
	flowDoc := "---\n" +
		"title: Flow Map\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"flow_map: {k1: v1, k2: 42, k3: [x, y]}\n" +
		"---\n\nbody\n"

	fmFlow, _, err := ParseFrontmatter([]byte(flowDoc))
	if err != nil {
		t.Fatalf("ParseFrontmatter flow: %v", err)
	}
	encFlow := fmFlow.Encode()
	mFlow := decodeYAMLMap(t, encFlow)
	wantFlow := map[string]any{
		"k1": "v1",
		"k2": 42,
		"k3": []any{"x", "y"},
	}
	if !reflect.DeepEqual(mFlow["flow_map"], wantFlow) {
		t.Fatalf("mFlow[flow_map] = %#v, want %#v", mFlow["flow_map"], wantFlow)
	}
	checkF4Fixpoint(t, "flow_map", flowDoc)

	blockDoc := "---\n" +
		"title: Nested Block Map\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"nested_block:\n" +
		"  lvl1:\n" +
		"    lvl2:\n" +
		"      leaf: value\n" +
		"      num: 99\n" +
		"---\n\nbody\n"

	fmBlock, _, err := ParseFrontmatter([]byte(blockDoc))
	if err != nil {
		t.Fatalf("ParseFrontmatter block: %v", err)
	}
	encBlock := fmBlock.Encode()
	mBlock := decodeYAMLMap(t, encBlock)
	wantBlock := map[string]any{
		"lvl1": map[string]any{
			"lvl2": map[string]any{
				"leaf": "value",
				"num":  99,
			},
		},
	}
	if !reflect.DeepEqual(mBlock["nested_block"], wantBlock) {
		t.Fatalf("mBlock[nested_block] = %#v, want %#v", mBlock["nested_block"], wantBlock)
	}
	checkF4Fixpoint(t, "nested_block_map", blockDoc)
}

// TestFrontmatterExtra_AnchorAliasExpands verifies that an anchor/alias in a composite
// expands to its target and drops anchor/alias syntax.
func TestFrontmatterExtra_AnchorAliasExpands(t *testing.T) {
	in := "---\n" +
		"title: Anchor Test\n" +
		"created: 2026-08-01\n" +
		"updated: 2026-08-01\n" +
		"type: concept\n" +
		"base_anchor: &common\n" +
		"  role: admin\n" +
		"  active: true\n" +
		"item_anchor: &item\n" +
		"  id: 10\n" +
		"users:\n" +
		"  alice: *common\n" +
		"items:\n" +
		"  - *item\n" +
		"---\n\nbody\n"

	fm, _, err := ParseFrontmatter([]byte(in))
	if err != nil {
		t.Fatalf("ParseFrontmatter: %v", err)
	}
	enc := fm.Encode()
	out := string(enc)

	if strings.Contains(out, "*common") || strings.Contains(out, "*item") {
		t.Fatalf("output still contains alias reference:\n%s", out)
	}

	m := decodeYAMLMap(t, enc)
	wantUsers := map[string]any{
		"alice": map[string]any{
			"role":   "admin",
			"active": true,
		},
	}
	if !reflect.DeepEqual(m["users"], wantUsers) {
		t.Fatalf("m[users] = %#v, want %#v", m["users"], wantUsers)
	}

	wantItems := []any{
		map[string]any{
			"id": 10,
		},
	}
	if !reflect.DeepEqual(m["items"], wantItems) {
		t.Fatalf("m[items] = %#v, want %#v", m["items"], wantItems)
	}

	checkF4Fixpoint(t, "anchor_alias", in)
}

// TestFrontmatterExtra_NoYAMLMarshalInVault verifies the Correction C-3 constraint:
// no production .go file in internal/vault contains yaml.Marshal, yaml.MarshalIndent, or yaml.NewEncoder.
func TestFrontmatterExtra_NoYAMLMarshalInVault(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("os.ReadDir: %v", err)
	}

	forbidden := []string{
		"yaml.Marshal",
		"yaml.MarshalIndent",
		"yaml.NewEncoder",
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("os.ReadFile(%s): %v", name, err)
		}
		for _, f := range forbidden {
			if strings.Contains(string(content), f) {
				t.Fatalf("file %s contains forbidden %q (Correction C-3 constraint violated)", name, f)
			}
		}
	}
}
