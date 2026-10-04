// Package state persists what heraldyx must not forget across a restart:
// where it had read to, and what it had already said.
//
// Both halves matter for the same reason. A process that forgets its offsets
// re-reads the log and re-sends old alerts; a process that forgets its dedup
// counters re-sends the current ones. In a container that restarts on a
// rollout, either one turns a quiet incident into a mailbox full of the same
// incident, which is how an operator learns to filter this sender to trash.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/rule"
)

// Snapshot is the whole of the persisted state.
type Snapshot struct {
	Rule    *rule.State      `json:"rule"`
	Offsets map[string]int64 `json:"offsets"`
	// Raised is notice key -> unix millis it was first raised, for the
	// notices this process raises about its own input (a refused source, an
	// unknown stream). One key is raised once, ever: a process that forgot
	// would mail the same pair on every rollout. Bounded by maxRaised, oldest
	// first out. Absent in a state file written before it existed.
	Raised map[string]int64 `json:"raised,omitempty"`
}

// maxRaised bounds [Snapshot.Raised]. A producer inventing a source per line
// can mint pairs without limit, so the memory of them has to have one; the
// cost of the bound is that the oldest pair, once forgotten, can be raised
// once more, which is the quiet direction to be wrong in.
const maxRaised = 256

// Raise records that the notice named key is being raised at now, and reports
// whether this is the first time. False means it was raised before and must
// not be again.
func (s *Snapshot) Raise(key string, now time.Time) bool {
	if s.Raised == nil {
		s.Raised = map[string]int64{}
	}
	if _, seen := s.Raised[key]; seen {
		return false
	}
	if len(s.Raised) >= maxRaised {
		oldestKey, oldest := "", int64(0)
		for k, ms := range s.Raised {
			if oldestKey == "" || ms < oldest || (ms == oldest && k < oldestKey) {
				oldestKey, oldest = k, ms
			}
		}
		delete(s.Raised, oldestKey)
	}
	s.Raised[key] = now.UnixMilli()
	return true
}

// New returns an empty snapshot.
func New() *Snapshot {
	return &Snapshot{Rule: rule.NewState(), Offsets: map[string]int64{}}
}

// Load reads the snapshot at path.
//
// A missing file is a first run, not an error. A file that does not parse is
// reported as a fresh snapshot AND an error, so the caller can log the fact
// loudly and still start: refusing to run because a cache is corrupt would
// leave the operator with no alerts at all, which is strictly worse than a
// window of possible duplicates.
func Load(path string) (*Snapshot, error) {
	// #nosec G304 -- operator-supplied path.
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(), nil
		}
		return New(), fmt.Errorf("state: read %s: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return New(), fmt.Errorf("state: %s did not parse, starting fresh: %w", path, err)
	}
	if s.Rule == nil {
		s.Rule = rule.NewState()
	}
	if s.Offsets == nil {
		s.Offsets = map[string]int64{}
	}
	return &s, nil
}

// Save writes the snapshot atomically: a temporary file in the same directory,
// then a rename. A crash during a write leaves the previous state intact
// rather than half of the new one.
func Save(path string, s *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("state: mkdir %s: %w", filepath.Dir(path), err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".heraldyx-state-*")
	if err != nil {
		return fmt.Errorf("state: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(raw); err != nil {
		// The close error is deliberately discarded here and only here: the
		// write already failed, the temp file is removed by the defer above,
		// and reporting a close failure instead of the write failure would
		// hide the reason this save did not happen.
		_ = tmp.Close()
		return fmt.Errorf("state: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("state: chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename into place: %w", err)
	}
	return nil
}
