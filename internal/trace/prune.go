package trace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// freshWindow is how recently a turn dir's newest file must have been
// written for Prune to leave it alone, cap or no cap: a turn lw is writing
// right now (or that failed minutes ago and may still be needed) is never
// the oldest turn that deserves to die.
const freshWindow = 10 * time.Minute

// Prune trims dir to keepBytes by removing whole turn dirs, oldest id
// first, until the total fits or only protected turns remain. Two turns are
// protected: the one this process is about to write (keep), and any whose
// newest file changed within freshWindow of now — size accounting can wait,
// an in-flight turn cannot be re-sent. Only turn-named directories are
// counted, considered, or removed; everything else under dir belongs to
// someone else. Returns the ids it removed.
func Prune(dir string, keepBytes int64, now time.Time, keep string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	type turn struct {
		id        string
		size      int64
		protected bool
	}
	// os.ReadDir sorts by name, and a turn id is a UTC timestamp, so the
	// slice comes out oldest-first, exactly the eviction order.
	var turns []turn
	var total int64
	for _, e := range entries {
		if !e.IsDir() || !turnNameRE.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		size, err := dirSize(p)
		if err != nil {
			return nil, err
		}
		turns = append(turns, turn{id: e.Name(), size: size, protected: e.Name() == keep || fresh(p, now)})
		total += size
	}
	var removed []string
	for i := 0; i < len(turns) && total > keepBytes; i++ {
		t := turns[i]
		if t.protected {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, t.id)); err != nil {
			return removed, err
		}
		removed = append(removed, t.id)
		total -= t.size
	}
	return removed, nil
}

// fresh reports whether any file under dir was modified within freshWindow
// of now. Only files count — a dir's own mtime moves every time a file is
// created in it, which would protect a turn for exactly the wrong reason
// (all its files long cold, the dir freshly touched by the last write).
func fresh(dir string, now time.Time) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return fs.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil && now.Sub(info.ModTime()) < freshWindow {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}
