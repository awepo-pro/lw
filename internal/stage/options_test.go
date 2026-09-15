// options_test.go proves the PUBLIC seam C24/D-3O introduce (contract §6
// note 3, MASTER §5 T16): OpenEngine's variadic options pin the clock and
// the changeset-id entropy source from outside the package — something
// this package's own tests could previously only do by writing the
// unexported e.now / e.rand fields directly (helpers_test.go). The
// journal assertions go through Engine.Journal().Query, the same public
// path internal/ui/logview reads its events with.
package stage_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// optionsIDPattern is the id shape newChangesetID emits today: "cs-" plus
// 16 hex characters (64 bits, MASTER §9 D-CI).
var optionsIDPattern = regexp.MustCompile(`^cs-[0-9a-f]{16}$`)

// optionsEntropy is the deterministic entropy source these tests inject:
// an io.Reader that fills every read with the same fixed pattern and
// never returns EOF, so OpenChangeset's id retry loop (up to 8 draws of
// 8 bytes each, engine_changeset.go) can call it as often as it likes
// without the source running dry. A bytes.Reader over a fixed buffer
// would be exhausted by the second changeset; this type cannot be.
//
// The same pattern on every read is deliberate: with WithClock's fixed
// timestamp it makes every candidate id identical, so two engines opened
// with the same options produce byte-identical changeset ids.
type optionsEntropy struct{}

func (optionsEntropy) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(0xA5 ^ i)
	}
	return len(p), nil
}

// TestOpenEngineOptions exercises the options exactly as production-adjacent
// callers would — through the exported OpenEngine/WithClock/WithEntropy
// surface and no unexported field.
func TestOpenEngineOptions(t *testing.T) {
	fixed := testutil.FixedClock()
	optionsAuthor := stage.Author{Kind: "agent", Model: "options-test"}

	// Two independent copies of the minimal fixture, each opened with the
	// same pinned clock and a fresh deterministic entropy reader: the two
	// changesets must get IDENTICAL ids (the id is
	// sha256(now || 8 entropy bytes), so both inputs are pinned) and
	// OpenedAt must be the fixed clock itself.
	var firstID string
	for i := 0; i < 2; i++ {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(dir,
			stage.WithClock(fixed),
			stage.WithEntropy(optionsEntropy{}))
		if err != nil {
			t.Fatalf("OpenEngine copy %d: %v", i, err)
		}
		t.Cleanup(func() { e.Close() })

		c, err := e.OpenChangeset("options-test: deterministic id and clock", optionsAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset copy %d: %v", i, err)
		}

		if !optionsIDPattern.MatchString(c.ID) {
			t.Errorf("copy %d: changeset id = %q, want it to match %s", i, c.ID, optionsIDPattern)
		}
		if !c.OpenedAt.Equal(fixed()) {
			t.Errorf("copy %d: OpenedAt = %s, want the fixed clock %s", i, c.OpenedAt, fixed())
		}
		if i == 0 {
			firstID = c.ID
		} else if c.ID != firstID {
			t.Errorf("copy 1: changeset id = %q, want copy 0's identical %q", c.ID, firstID)
		}

		// The journal, read through the same public path
		// internal/ui/logview's queryCmd uses (Engine.Journal().Query).
		// OpenChangeset always writes at least changeset_opened, and every
		// event's TS comes from the injected clock.
		events, err := e.Journal().Query(stage.Filter{})
		if err != nil {
			t.Fatalf("copy %d: Journal().Query: %v", i, err)
		}
		if len(events) < 1 {
			t.Fatalf("copy %d: journal has no events, want at least the changeset_opened one", i)
		}
		for _, ev := range events {
			if !ev.TS.Equal(fixed()) {
				t.Errorf("copy %d: journal event %s TS = %s, want the fixed clock %s", i, ev.Kind, ev.TS, fixed())
			}
		}
	}

	// nil options leave the defaults in place (time.Now,
	// crypto/rand.Reader): OpenedAt tracks the wall clock, and two such
	// engines — even on identical fixture copies — draw different ids,
	// because the entropy differs and the id loop never sees a collision.
	var defaultIDs [2]string
	for i := 0; i < 2; i++ {
		dir := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(dir, stage.WithClock(nil), stage.WithEntropy(nil))
		if err != nil {
			t.Fatalf("OpenEngine defaults copy %d: %v", i, err)
		}
		t.Cleanup(func() { e.Close() })

		c, err := e.OpenChangeset("options-test: defaults", optionsAuthor)
		if err != nil {
			t.Fatalf("OpenChangeset defaults copy %d: %v", i, err)
		}
		if diff := time.Since(c.OpenedAt); diff < -time.Minute || diff > time.Minute {
			t.Errorf("defaults copy %d: OpenedAt = %s, want within 1 minute of %s", i, c.OpenedAt, time.Now())
		}
		defaultIDs[i] = c.ID
	}
	if defaultIDs[0] == defaultIDs[1] {
		t.Errorf("two default engines drew the same id %q, want crypto/rand to vary it", defaultIDs[0])
	}
}
