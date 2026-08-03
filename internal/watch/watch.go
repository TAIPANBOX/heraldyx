// Package watch follows the shared NDJSON event log the planes couple
// through, and hands back whole events.
//
// It reads. That is the entire relationship this process has with the rest of
// the stack: no socket, no API key, no client, nothing another plane has to
// know exists. A component that only reads a file cannot break the thing it
// watches, which is the property that makes an alerting path safe to add to a
// system that governs money.
package watch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// Watcher follows a set of NDJSON files by byte offset.
type Watcher struct {
	paths   []string
	offsets map[string]int64

	// Malformed counts lines that did not parse, ever, for this process.
	// Surfaced rather than hidden: a producer writing something this build
	// cannot read is a fact an operator should be able to see, and it is not
	// a reason to stop watching.
	Malformed int
	// Truncations counts files that got shorter than the offset we held,
	// which is a rotation or a reset, not an error.
	Truncations int
}

// New returns a watcher over paths, starting from the given offsets (nil for
// a fresh start).
func New(paths []string, offsets map[string]int64) *Watcher {
	w := &Watcher{paths: paths, offsets: map[string]int64{}}
	for k, v := range offsets {
		w.offsets[k] = v
	}
	return w
}

// maxRememberedPaths bounds how many read positions are kept for files that
// are not in the current set. One per event log the box has ever had is a
// handful; this is a backstop against a pathological directory, not a policy.
const maxRememberedPaths = 256

// SetPaths replaces the watched set.
//
// Called on every poll, because the set is not fixed: a plane deployed after
// this process started writes a file that did not exist at startup, and the
// alternative to noticing it is an operator wondering why one plane never
// alerts. A file that appears later is read from its beginning, which is
// right: everything in it is new.
//
// It does NOT forget where it was in a file that is missing from the set right
// now. It used to, and that cost the whole point of persisting offsets:
// resolving the set can come back short for a moment (a directory that cannot
// be stat'ed on one poll, a mount not yet visible), and one such moment threw
// away every read position. The next poll then re-read every log from byte
// zero and mailed the operator its entire history again, bounded only by the
// ten-minute dedup window, which is to say not bounded at all for anything
// older than ten minutes.
//
// Measured on a live cluster 2026-08-02: every restart of the notifier
// re-processed the full event log, and the counter that proved it was the
// digest, which had counted six events between seven and eight times each.
//
// Keeping the position is also the safe direction. A file genuinely replaced
// or rotated is caught in pollOne, where a size smaller than the offset means
// start over; a file that is simply out of sight for one poll keeps its place
// and loses nothing.
func (w *Watcher) SetPaths(paths []string) {
	w.paths = paths
	if len(w.offsets) <= maxRememberedPaths {
		return
	}
	keep := make(map[string]int64, len(paths))
	for _, p := range paths {
		if off, ok := w.offsets[p]; ok {
			keep[p] = off
		}
	}
	w.offsets = keep
}

// Offsets returns a copy of the current read positions, for persisting.
func (w *Watcher) Offsets() map[string]int64 {
	out := make(map[string]int64, len(w.offsets))
	for k, v := range w.offsets {
		out[k] = v
	}
	return out
}

// SkipToEnd moves every offset to the current end of file without reading
// anything.
//
// This is what a FIRST run does, and the reason is worth stating: a box that
// has been running for a month has a log full of old incidents, and a
// notifier that starts by mailing all of them is a notifier the operator
// turns off in the first hour. History belongs in the console. Mail is for
// what happens from now on.
func (w *Watcher) SkipToEnd() error {
	for _, p := range w.paths {
		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("watch: stat %s: %w", p, err)
		}
		w.offsets[p] = info.Size()
	}
	return nil
}

// Poll reads every complete line appended since the last call, in file order.
//
// A trailing partial line is deliberately NOT consumed: the writer on the
// other side is appending, and half an event parsed now is an event lost
// forever. The offset only ever advances past a newline.
func (w *Watcher) Poll() []event.Event {
	var out []event.Event
	for _, p := range w.paths {
		events, err := w.pollOne(p)
		if err != nil {
			// A file we cannot read right now is not a reason to stop
			// watching the others, or to stop watching this one later.
			continue
		}
		out = append(out, events...)
	}
	return out
}

func (w *Watcher) pollOne(path string) ([]event.Event, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A plane that is not deployed writes no file. Not an error.
			return nil, nil
		}
		return nil, err
	}

	from := w.offsets[path]
	if info.Size() < from {
		// Rotated, truncated, or replaced. Start over rather than seek past
		// the end and read nothing forever.
		w.Truncations++
		from = 0
	}
	if info.Size() == from {
		return nil, nil
	}

	// #nosec G304 -- the paths are operator-supplied configuration.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	// Only whole lines. Whatever follows the last newline is a write in
	// progress; leave the offset before it.
	cut := bytes.LastIndexByte(buf, '\n')
	if cut < 0 {
		return nil, nil
	}
	complete := buf[:cut+1]
	w.offsets[path] = from + int64(len(complete))

	var out []event.Event
	for _, line := range bytes.Split(complete, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		e, err := event.Unmarshal(line)
		if err != nil {
			w.Malformed++
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
