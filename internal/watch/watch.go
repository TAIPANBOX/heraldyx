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
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"time"

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
	// Truncations counts files found to be a different file from the one
	// the read position belongs to, because their first line changed: a
	// rotation, a truncation and rewrite, a replacement. Each is read again
	// from its start. A size smaller than the position is NOT one of these
	// on its own (see Shorter): on a shared network volume the size a reader
	// is told can be minutes old.
	Truncations int
	// Shorter counts stretches during which a file looked shorter than what
	// had already been read from it, and nothing showed it to be a different
	// file, so the read position was kept and nothing was read twice. Once
	// per stretch, not once per poll. A reader on another node of an NFS
	// volume can be told an old size, and the writer's own node can hold
	// bytes the server has not been sent yet; reading the file again from
	// byte zero in either case mails the operator its history a second time.
	Shorter int
	// ShorterExpired counts stretches counted in Shorter that lasted the
	// whole HoldWindow. The file is then read again from its start: the
	// writer may have lost the tail this process read (a node that died
	// with bytes it had not flushed) and carried on below it, and waiting
	// longer would stop reading that file in silence. The cost is the old
	// one, mail already sent may be sent again, bounded by the dedup window
	// and the hourly ceiling, and it is counted so it can be said.
	ShorterExpired int
	// ZeroHeld counts lines with a zero byte in them that reading stopped
	// before: once per line held, not once per poll. No event can contain a
	// raw zero byte (JSON forbids it unescaped), and on a shared volume a run
	// of them is what a reader sees where a writer's bytes have not arrived
	// while later ones have. Parsing such a line now would count it
	// malformed and lose every event in the span for good.
	ZeroHeld int
	// ZeroSkipped counts lines with a zero byte in them that were still
	// there after HoldWindow and were skipped. Each one is a defect: any
	// event in that span is lost to this process. A line can be both this
	// and Oversized, and is then counted in both.
	ZeroSkipped int
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
	// NotCovered counts well-formed events refused because the file they sit
	// in has no signed checkpoint covering it (the checkpoint gate, F-018).
	// Never returned by Poll, and said once per file in the mail.
	NotCovered int

	policy   stream.Policy
	coverage Coverage
	// uncovered is the refused files since TakeUncovered last ran, one entry
	// per file, bounded by maxPendingNotices like foreign and unknown.
	uncovered map[string]*Uncovered
	// foreign and unknown hold what was refused or merely unrecognised since
	// the caller last took them, one entry per pair or file and bounded by
	// maxPendingNotices, so a producer inventing a source per line cannot
	// make this process hold a map the size of its log.
	foreign map[foreignKey]*Foreign
	unknown map[string]*Unknown

	// heads is path -> the digest of the first line of the file the read
	// position belongs to (see readHead). Persisted beside the offsets.
	heads map[string]string
	// shorter is path -> when the current stretch of the file ending before
	// its read position began. In memory only: a restart starts the window
	// again, which can only make it longer.
	shorter map[string]time.Time
	// zeroed is path -> the line reading is holding before, and since when.
	zeroed map[string]zeroHold

	// stat and now are seams for the tests: a stale size is a property of
	// a shared volume, and a window is a property of a clock.
	stat func(string) (fs.FileInfo, error)
	now  func() time.Time
}

type zeroHold struct {
	at    int64
	since time.Time
}

// HoldWindow is how long a read position is held for a file that looks
// shorter than it, and how long reading waits before a line with a zero byte
// in it, before giving up on the wait. Long enough for a shared volume to
// catch up: a reader on a GCP cluster run on 2026-10-14 saw stale sizes for
// about two minutes, Linux's NFS client caches a file's attributes for up to
// a minute by default, and a writer's node holds unflushed bytes for about
// thirty seconds by default. Short enough that what it delays is still worth
// mailing. A time
// rather than a count of polls, because what it waits for is a property of
// the volume, not of how often this process looks.
const HoldWindow = 5 * time.Minute

// maxHeadBytes bounds the first line read to tell one file from another.
const maxHeadBytes = 4096

// Holds is the counters a caller prints about held and restarted reads.
type Holds struct {
	Truncations, Shorter, ShorterExpired, ZeroHeld, ZeroSkipped int
}

// Holds returns the current counters.
func (w *Watcher) Holds() Holds {
	return Holds{
		Truncations:    w.Truncations,
		Shorter:        w.Shorter,
		ShorterExpired: w.ShorterExpired,
		ZeroHeld:       w.ZeroHeld,
		ZeroSkipped:    w.ZeroSkipped,
	}
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
	w := &Watcher{
		paths:   paths,
		offsets: map[string]int64{},
		policy:  stream.Default(),
		heads:   map[string]string{},
		shorter: map[string]time.Time{},
		zeroed:  map[string]zeroHold{},
		stat:    os.Stat,
		now:     time.Now,
	}
	for k, v := range offsets {
		w.offsets[k] = v
	}
	return w
}

// SetHeads restores which file each read position belongs to, as
// [Watcher.Heads] returned it before a restart. Without it a restart can
// still tell a file that looks shorter from a replaced one, but only by
// waiting out HoldWindow.
func (w *Watcher) SetHeads(heads map[string]string) {
	w.heads = make(map[string]string, len(heads))
	for k, v := range heads {
		w.heads[k] = v
	}
}

// Heads returns a copy of the first-line digests, for persisting beside the
// offsets.
func (w *Watcher) Heads() map[string]string {
	out := make(map[string]string, len(w.heads))
	for k, v := range w.heads {
		out[k] = v
	}
	return out
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
// or rotated is caught in pollOne, where a first line that changed means
// start over; a file that is simply out of sight for one poll keeps its place
// and loses nothing.
func (w *Watcher) SetPaths(paths []string) {
	w.paths = paths
	if len(w.offsets) <= maxRememberedPaths && len(w.heads) <= maxRememberedPaths {
		return
	}
	keep := make(map[string]int64, len(paths))
	keepHeads := make(map[string]string, len(paths))
	for _, p := range paths {
		if off, ok := w.offsets[p]; ok {
			keep[p] = off
		}
		if h, ok := w.heads[p]; ok {
			keepHeads[p] = h
		}
	}
	w.offsets = keep
	w.heads = keepHeads
	for p := range w.shorter {
		if _, ok := keep[p]; !ok {
			delete(w.shorter, p)
		}
	}
	for p := range w.zeroed {
		if _, ok := keep[p]; !ok {
			delete(w.zeroed, p)
		}
	}
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
//
// It also remembers each file's first line, so the position it sets belongs
// to that file and a replacement is told apart at once rather than waited
// for.
func (w *Watcher) SkipToEnd() error {
	for _, p := range w.paths {
		info, err := w.stat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("watch: stat %s: %w", p, err)
		}
		w.offsets[p] = info.Size()
		w.rememberHead(p)
	}
	return nil
}

// rememberHead stores the first line of the file at path, when it has a
// whole one, as the file the read position belongs to.
func (w *Watcher) rememberHead(path string) {
	// #nosec G304 -- the paths are operator-supplied configuration.
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if h, ok := readHead(f); ok {
		w.heads[path] = h
	}
}

// readHead reads the file's first line, newline included and bounded at
// maxHeadBytes, and returns its length and SHA-256 digest. That is what tells
// one file from another here, and the reasons are specific to where these
// files live:
//
//   - It is read through the data path, which on an NFS volume is what the
//     server holds, while a stat can be minutes old.
//   - It is the same on every node. The device number of a file differs per
//     mount, so a pod that moves node would see every file as new.
//   - It changes when a file is truncated and rewritten in place, which keeps
//     the inode number, and an inode number is reused after a delete on
//     ext4 and xfs, so an inode alone misses both.
//   - Every event line carries its own timestamp and identifiers, so two
//     files of this bus do not share a first line.
//   - Only a digest is kept, so no event content reaches the state file.
//
// ok is false when there is no whole first line to compare yet: an empty
// file, a first line still being written, a read error, or a first line with
// a zero byte in it (a span the volume has not filled in, which says nothing
// about which file this is).
func readHead(f *os.File) (string, bool) {
	buf := make([]byte, maxHeadBytes)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i+1]
	} else if n < maxHeadBytes {
		return "", false
	}
	if bytes.IndexByte(buf, 0) >= 0 {
		return "", false
	}
	return fmt.Sprintf("%d:%x", len(buf), sha256.Sum256(buf)), true
}

// Poll reads every complete line appended since the last call, in file order.
//
// A trailing partial line is deliberately NOT consumed: the writer on the
// other side is appending, and half an event parsed now is an event lost
// forever. The offset only ever advances past a newline, and not past a line
// with a zero byte in it until HoldWindow has passed (see read).
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
	info, err := w.stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// A plane that is not deployed writes no file. Not an error.
			return nil, nil
		}
		return nil, err
	}

	from := w.offsets[path]
	size := info.Size()
	_, wasShorter := w.shorter[path]
	if size == from && !wasShorter {
		return nil, nil
	}

	// #nosec G304 -- the paths are operator-supplied configuration.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Which file this is comes first, whatever the size says. A first line
	// that changed is a different file, read from its start, even when it is
	// already longer than the position held for the old one: reading it from
	// that position would start mid-line and never see its beginning.
	if head, ok := readHead(f); ok {
		stored, known := w.heads[path]
		w.heads[path] = head
		if known && head != stored {
			w.Truncations++
			w.restart(path)
			return w.read(path, f, 0, size)
		}
	}

	if size >= from {
		delete(w.shorter, path)
		if size == from {
			return nil, nil
		}
		return w.read(path, f, from, size-from)
	}

	// The size says the file ends before what was already read from it, and
	// nothing says it is a different file. Ask the data rather than the size:
	// one byte just before the position. If it is there, the size was old (a
	// reader's attribute cache) and reading goes on from the position at
	// once. If it is not, the file this process can see really is shorter,
	// and the position is held until it catches up.
	probe := make([]byte, 1)
	if n, err := f.ReadAt(probe, from-1); n == 1 {
		delete(w.shorter, path)
		return w.read(path, f, from, 0)
	} else if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	now := w.now()
	since, ok := w.shorter[path]
	if !ok {
		w.shorter[path] = now
		w.Shorter++
		return nil, nil
	}
	if now.Sub(since) < HoldWindow {
		return nil, nil
	}
	w.ShorterExpired++
	w.restart(path)
	delete(w.heads, path)
	return w.read(path, f, 0, size)
}

// restart forgets everything held about path's read position, which is about
// to start from zero.
func (w *Watcher) restart(path string) {
	w.offsets[path] = 0
	delete(w.shorter, path)
	delete(w.zeroed, path)
}

// holdZero decides what to do with a line with a zero byte in it at offset
// at: true to stop before it and look again next poll, false to skip it
// because it has been held for the whole HoldWindow. The window is the
// line's own: a hold that moves to another line starts again.
func (w *Watcher) holdZero(path string, at int64) bool {
	now := w.now()
	h, ok := w.zeroed[path]
	if !ok || h.at != at {
		w.zeroed[path] = zeroHold{at: at, since: now}
		w.ZeroHeld++
		return true
	}
	if now.Sub(h.since) < HoldWindow {
		return true
	}
	delete(w.zeroed, path)
	w.ZeroSkipped++
	return false
}

// read consumes the whole lines in up to maxBytesPerPoll bytes of f from
// offset from, and moves the read position past exactly what it consumed.
//
// grown is what the size says was added since from. It decides only whether
// this poll counts as capped: what is read is what the data holds, up to the
// cap, because on a shared volume the size can be older than the data.
func (w *Watcher) read(path string, f *os.File, from, grown int64) ([]event.Event, error) {
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, err
	}
	if grown > maxBytesPerPoll {
		w.Capped++
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxBytesPerPoll))
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
		// A full buffer with zero bytes in it may be a span the volume has
		// not filled in, hiding the newline the cap is looking for. It is
		// held like any line with a zero byte in it.
		if bytes.IndexByte(buf, 0) >= 0 && w.holdZero(path, from) {
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

	var out []event.Event
	stem := stream.Stem(path)
	at := from
	rest := buf[:cut+1]
	// The gate is asked once per read, about the whole file, and only if a line
	// here turns out to be an event: a file with nothing but malformed or
	// zero-byte lines is not refused for want of a checkpoint.
	var covered, asked bool
	var why string
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		line := rest[:i]
		start := at
		at += int64(i) + 1
		rest = rest[i+1:]

		// A zero byte cannot be in an event, and on a shared volume it is
		// a span a writer's bytes have not reached yet. Stop before it and
		// look again, for HoldWindow, rather than parse it now as
		// malformed and lose the events in it for good.
		if bytes.IndexByte(line, 0) >= 0 {
			if w.holdZero(path, start) {
				w.offsets[path] = start
				return out, nil
			}
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		e, err := event.Unmarshal(line)
		if err != nil {
			w.Malformed++
			continue
		}
		// The checkpoint gate (F-018) comes before the source policy: a file
		// the gate does not cover is refused whatever it claims, counted and
		// said once per file, and never returned.
		if w.coverage != nil {
			if !asked {
				covered, why = w.coverage(path)
				asked = true
			}
			if !covered {
				w.noteUncovered(path, why)
				continue
			}
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
	w.offsets[path] = at
	delete(w.zeroed, path)
	return out, nil
}
