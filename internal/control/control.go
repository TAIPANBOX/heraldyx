// Package control is the file an operator's commands write and the running
// loop only reads. One writer per file, held by invariant: this file is
// written by the command line, state.json by the loop, never the other way,
// because the loop rewrites state.json on every poll and would erase anything
// a command put there.
package control

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

// maxAcks bounds the file: acknowledgements are typed by a person, and two
// hundred in force at once is a mute, not a list.
const maxAcks = 200

// File is the whole control file.
type File struct {
	Acks []rule.Ack `json:"acks"`
}

// Load reads the file. Missing is empty and not an error. Unreadable or
// malformed is empty AND an error: the loop logs it and holds nothing, and a
// command refuses to overwrite it.
func Load(path string) (File, error) {
	// #nosec G304 -- operator-supplied path.
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return File{}, nil
		}
		return File{}, fmt.Errorf("control: read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("control: %s did not parse: %w", path, err)
	}
	return f, nil
}

// Save writes the file atomically: a temporary file beside it, then a rename.
func Save(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("control: mkdir: %w", err)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("control: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".heraldyx-control-*")
	if err != nil {
		return fmt.Errorf("control: temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("control: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("control: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("control: close: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("control: chmod: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("control: rename into place: %w", err)
	}
	return nil
}

// Put adds a, replacing any acknowledgement of the same key.
func (f *File) Put(a rule.Ack) error {
	if err := rule.ValidateAck(a); err != nil {
		return err
	}
	for i := range f.Acks {
		if f.Acks[i].Key == a.Key {
			f.Acks[i] = a
			return nil
		}
	}
	if len(f.Acks) >= maxAcks {
		return fmt.Errorf("control: %d acknowledgements are already in the file; remove some first", maxAcks)
	}
	f.Acks = append(f.Acks, a)
	return nil
}

// Remove deletes the acknowledgement of key and reports whether there was one.
func (f *File) Remove(key string) bool {
	for i := range f.Acks {
		if f.Acks[i].Key == key {
			f.Acks = append(f.Acks[:i], f.Acks[i+1:]...)
			return true
		}
	}
	return false
}

// Prune drops acknowledgements that ended. Called by commands before they
// save, never by the loop.
func (f *File) Prune(now time.Time) {
	keep := f.Acks[:0]
	for _, a := range f.Acks {
		if now.UnixMilli() < a.Until {
			keep = append(keep, a)
		}
	}
	f.Acks = keep
}

// Active returns the acknowledgements in force at now, by key. An entry that
// fails ValidateAck (a hand-edited file) is left out rather than honoured. So is
// one that ends more than MaxAck from now: a hand edit can give a valid one-hour
// span years in the future, and that would hold a key from now until then.
func (f File) Active(now time.Time) map[string]rule.Ack {
	out := map[string]rule.Ack{}
	latest := now.UnixMilli() + rule.MaxAck.Milliseconds()
	for _, a := range f.Acks {
		if now.UnixMilli() < a.Until && a.Until <= latest && rule.ValidateAck(a) == nil {
			out[a.Key] = a
		}
	}
	return out
}
