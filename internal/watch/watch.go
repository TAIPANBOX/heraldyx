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

	"sort"

	"github.com/TAIPANBOX/agent-stack-go/event"

	"github.com/TAIPANBOX/heraldyx/internal/stream"
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
	// Capped counts polls where a file's growth since the last offset
	// exceeded maxBytesPerPoll, so only the first maxBytesPerPoll bytes of
	// it were read this poll. Surfaced for the same reason as Malformed and
	// Truncations: a plane producing abnormal volume is a fact an operator
	// should be able to see, not a silent internal detail.
	Capped int
	// Oversized counts completed lines longer than maxBytesPerPoll. Such a
	// line can never be delivered: it does not fit in one read, and holding
	// the offset before it would read its first cap forever while every
	// line after it stays invisible, which is the freeze the cap must not
	// introduce. So it is skipped, the offset moves past it, and it is
	// counted here rather than under Malformed, because it may well have
	// parsed and the operator should know which of the two it was.
	Oversized int
	// ForeignSource counts well-formed events whose `source` the file they
	// were read from may not carry, ever, for this process. Such an event is
	// never returned by Poll: it is not processed as the source it claims.
	// Beside Malformed on purpose, for the same reason: a producer, or a
	// co-tenant of the bus, writing something that is not what its file says
	// it is is a fact an operator should be able to see.
	ForeignSource int
	// UnknownStream counts events read from a file whose stem nothing
	// declares, the line claiming the stem itself. They ARE returned by Poll,
	// because a plane this build has not heard of must not go deaf, and they
	// are counted because trusting a stream in silence is the other way to be
	// wrong.
	UnknownStream int

	policy stream.Policy
	// foreign and unknown hold what was refused or merely unrecognised since
	// the caller last took them, one entry per pair or file and bounded by
	// maxPendingNotices, so a producer inventing a source per line cannot
	// make this process hold a map the size of its log.
	foreign map[foreignKey]*Foreign
	unknown map[string]*Unknown
}

// Foreign is one (file, claimed source) pair whose events were refused since
// the last [Watcher.TakeForeign].
type Foreign struct {
	// File is the path the events were read from, Stem its stream name.
	File, Stem string
	// Claimed is the `source` the events carried, exactly as written. It is
	// producer-written text: a caller that renders it must treat it as such.
	Claimed string
	// Count is how many events of this pair were refused since the last take.
	Count int
	// Allowed is what the file may carry, sorted.
	Allowed []string
}

// Unknown is one file of an undeclared stream that was read since the last
// [Watcher.TakeUnknown].
type Unknown struct {
	File, Stem string
	Count      int
}

type foreignKey struct{ file, claimed string }

// maxPendingNotices bounds each of the two pending maps. Past it the counters
// still grow and the notice for a new pair simply is not raised.
const maxPendingNotices = 256

// maxBytesPerPoll bounds how much of a single file's growth is read in one
// poll. Without a cap, a looping or compromised producer that appends more
// bytes than the process has memory for inside one poll interval gets that
// whole growth loaded into one buffer, and the process an operator relies on
// to say something is wrong is OOM-killed at the exact moment it matters
// most. A few MiB is ample for a normal NDJSON burst (an event line runs a
// few hundred bytes, so this is tens of thousands of them per file per poll)
// and small next to the memory of any box this runs on. What does not fit in
// one poll is read on the next one: the offset only ever advances past whole
// lines actually consumed, so nothing is lost, only delayed. Peak memory per
// poll is two caps, not one: the capped read plus the chunk endOfLine uses
// to skip a line the cap cannot hold (one of exactly cap bytes plus its
// newline counts as such a line; the prose says "longer" and means "does
// not fit with its newline").
const maxBytesPerPoll = 4 * 1024 * 1024 // 4 MiB

// New returns a watcher over paths, starting from the given offsets (nil for
// a fresh start).
//
// The watcher enforces [stream.Default] until [Watcher.SetPolicy] says
// otherwise, so a caller that forgets to configure it is closed rather than
// open.
func New(paths []string, offsets map[string]int64) *Watcher {
	w := &Watcher{paths: paths, offsets: map[string]int64{}, policy: stream.Default()}
	for k, v := range offsets {
		w.offsets[k] = v
	}
	return w
}

// SetPolicy replaces the rule that decides which sources a file may carry.
func (w *Watcher) SetPolicy(p stream.Policy) { w.policy = p }

// TakeForeign returns the (file, claimed source) pairs refused since the last
// call, in a stable order, and forgets them. What was refused is never handed
// back as an event; this is how the caller learns it happened.
func (w *Watcher) TakeForeign() []Foreign {
	out := make([]Foreign, 0, len(w.foreign))
	for _, f := range w.foreign {
		out = append(out, *f)
	}
	w.foreign = nil
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Claimed < out[j].Claimed
	})
	return out
}

// TakeUnknown returns the undeclared streams read since the last call, in a
// stable order, and forgets them.
func (w *Watcher) TakeUnknown() []Unknown {
	out := make([]Unknown, 0, len(w.unknown))
	for _, u := range w.unknown {
		out = append(out, *u)
	}
	w.unknown = nil
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

func (w *Watcher) noteForeign(path, stem, claimed string) {
	w.ForeignSource++
	key := foreignKey{path, claimed}
	if f, ok := w.foreign[key]; ok {
		f.Count++
		return
	}
	if len(w.foreign) >= maxPendingNotices {
		return
	}
	if w.foreign == nil {
		w.foreign = map[foreignKey]*Foreign{}
	}
	w.foreign[key] = &Foreign{File: path, Stem: stem, Claimed: claimed, Count: 1, Allowed: w.policy.AllowedFor(stem)}
}

func (w *Watcher) noteUnknown(path, stem string) {
	w.UnknownStream++
	if u, ok := w.unknown[path]; ok {
		u.Count++
		return
	}
	if len(w.unknown) >= maxPendingNotices {
		return
	}
	if w.unknown == nil {
		w.unknown = map[string]*Unknown{}
	}
	w.unknown[path] = &Unknown{File: path, Stem: stem, Count: 1}
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
// endOfLine reads forward from `at`, one cap at a time, and reports the
// offset just past the first newline it meets, or -1 when the file ends
// before one does. Bounded per read so skipping an oversized line costs no
// more memory than reading an ordinary poll.
func endOfLine(f *os.File, at int64) (int64, error) {
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		return -1, err
	}
	chunk := make([]byte, maxBytesPerPoll)
	pos := at
	for {
		n, err := f.Read(chunk)
		if i := bytes.IndexByte(chunk[:n], '\n'); i >= 0 {
			return pos + int64(i) + 1, nil
		}
		pos += int64(n)
		if err == io.EOF {
			return -1, nil
		}
		if err != nil {
			return -1, err
		}
	}
}

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
	grown := info.Size() - from
	if grown == 0 {
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

	limit := grown
	if limit > maxBytesPerPoll {
		limit = maxBytesPerPoll
		w.Capped++
	}
	buf, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, err
	}

	// Only whole lines. Whatever follows the last newline is a write in
	// progress; leave the offset before it.
	cut := bytes.LastIndexByte(buf, '\n')
	if cut < 0 {
		if int64(len(buf)) < maxBytesPerPoll {
			return nil, nil
		}
		// A full buffer with no newline in it is not a write in progress,
		// it is a line the cap cannot hold. Holding the offset before it
		// would read its first cap forever and every line after it would
		// stay invisible: the OOM turned into a silent per-file freeze. So
		// the offset moves past it, to the newline that ends it, read in
		// cap-sized pieces so the skip itself is bounded too. If that
		// newline has not been written yet, the offset stays and the next
		// poll looks again.
		end, err := endOfLine(f, from+int64(len(buf)))
		if err != nil {
			return nil, err
		}
		if end < 0 {
			return nil, nil
		}
		w.offsets[path] = end
		w.Oversized++
		return nil, nil
	}
	complete := buf[:cut+1]
	w.offsets[path] = from + int64(len(complete))

	var out []event.Event
	stem := stream.Stem(path)
	for _, line := range bytes.Split(complete, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		e, err := event.Unmarshal(line)
		if err != nil {
			w.Malformed++
			continue
		}
		// The source an event claims is checked against the file it sits in,
		// AFTER it parsed and BEFORE anything else sees it. A line that fails
		// here is counted and never returned, so nothing downstream (the
		// rule, the renderer, the fleet picture, the digest) can process it
		// as the source it named.
		switch w.policy.Check(stem, e.Source) {
		case stream.Foreign:
			w.noteForeign(path, stem, e.Source)
			continue
		case stream.AllowedUnknownStem:
			w.noteUnknown(path, stem)
		case stream.Allowed:
		}
		out = append(out, e)
	}
	return out, nil
}
