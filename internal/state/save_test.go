package state

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Which file each read position belongs to survives a restart beside the
// position itself, and a state file written before it existed still loads.
func TestTheFirstLinesSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := New()
	s.Offsets["/e/tokenfuse.ndjson"] = 4096
	s.Heads = map[string]string{"/e/tokenfuse.ndjson": "312:ab"}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Heads["/e/tokenfuse.ndjson"] != "312:ab" {
		t.Fatalf("the first line did not survive: %v", back.Heads)
	}

	older := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(older, []byte(`{"rule":{},"offsets":{"/e/tokenfuse.ndjson":4096}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	back, err = Load(older)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Heads) != 0 || back.Offsets["/e/tokenfuse.ndjson"] != 4096 {
		t.Fatalf("an older state file loads with its offsets and no first lines: %+v", back)
	}
}

// A state file holding nothing still gives back both halves, ready to use.
func TestAnEmptyStateFileGivesBothHalves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rule == nil || s.Offsets == nil {
		t.Fatalf("want both halves usable, got %+v", s)
	}
}

// A state path that cannot be read is reported and the process starts fresh,
// the same as a corrupt one.
func TestAStatePathThatCannotBeReadIsReportedAndStartsFresh(t *testing.T) {
	dir := t.TempDir() // a directory where the file should be
	s, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "state: read") {
		t.Fatalf("want a read error, got %v", err)
	}
	if s == nil || s.Rule == nil || s.Offsets == nil {
		t.Fatal("and it must still return something usable")
	}
}

// A save that fails at any step leaves the previous state exactly as it was
// and no temporary file behind, and says which step failed.
func TestASaveThatFailsLeavesThePreviousStateIntact(t *testing.T) {
	for _, c := range []struct {
		name string
		// temp, when set, replaces os.CreateTemp for this save.
		temp func(t *testing.T, dir, pattern string) (*os.File, error)
		// block, when set, makes the state path itself unusable.
		block func(t *testing.T, path string) string
		want  string
	}{
		{
			name: "the directory cannot be made",
			block: func(t *testing.T, path string) string {
				parent := filepath.Join(filepath.Dir(path), "file")
				if err := os.WriteFile(parent, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(parent, "state.json")
			},
			want: "state: mkdir",
		},
		{
			name: "no temporary file can be made",
			temp: func(t *testing.T, dir, pattern string) (*os.File, error) {
				return nil, errors.New("no space left")
			},
			want: "state: temp file",
		},
		{
			name: "the write fails",
			temp: func(t *testing.T, dir, pattern string) (*os.File, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					t.Fatal(err)
				}
				name := f.Name()
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				return os.Open(name) // read-only, so the write fails
			},
			want: "state: write",
		},
		{
			name: "the sync fails",
			temp: func(t *testing.T, dir, pattern string) (*os.File, error) {
				r, w, err := os.Pipe() // a pipe takes the write and cannot be synced
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { r.Close() })
				return w, nil
			},
			want: "state: sync",
		},
		{
			name: "the permissions cannot be set",
			temp: func(t *testing.T, dir, pattern string) (*os.File, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					t.Fatal(err)
				}
				// Gone from the directory, still open: write, sync and close
				// succeed, and the chmod by name finds nothing.
				if err := os.Remove(f.Name()); err != nil {
					t.Fatal(err)
				}
				return f, nil
			},
			want: "state: chmod",
		},
		{
			name: "the rename into place fails",
			block: func(t *testing.T, path string) string {
				target := filepath.Join(filepath.Dir(path), "taken")
				if err := os.MkdirAll(filepath.Join(target, "inside"), 0o700); err != nil {
					t.Fatal(err)
				}
				return target
			},
			want: "state: rename into place",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			before := New()
			before.Offsets["/e/tokenfuse.ndjson"] = 7
			if err := Save(path, before); err != nil {
				t.Fatal(err)
			}
			was, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			target := path
			if c.block != nil {
				target = c.block(t, path)
			}
			if c.temp != nil {
				createTemp = func(d, pattern string) (*os.File, error) { return c.temp(t, d, pattern) }
				t.Cleanup(func() { createTemp = os.CreateTemp })
			}
			next := New()
			next.Offsets["/e/tokenfuse.ndjson"] = 99
			err = Save(target, next)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error saying %q, got %v", c.want, err)
			}

			now, rerr := os.ReadFile(path)
			if rerr != nil || !bytes.Equal(now, was) {
				t.Fatalf("the previous state changed: %v\n%s", rerr, now)
			}
			entries, rerr := os.ReadDir(dir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".heraldyx-state-") {
					t.Fatalf("a temporary file was left behind: %s", e.Name())
				}
			}
		})
	}
}
