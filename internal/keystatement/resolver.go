package keystatement

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Verdict is the resolver's answer about one checkpoint record's key.
type Verdict int

const (
	// Found: a statement under the pinned root certifies this kid as an
	// ES256 key for the stream's bound key name, valid at the record's time
	// and not revoked.
	Found Verdict = iota
	// NotCertified: there is no such statement, or it certifies another
	// name, algorithm or time, or the key is revoked, or the stream is bound
	// to no key name. The record is reported as not signed by a key this box
	// trusts, as SPEC 6.5.1 asks.
	NotCertified
	// Wait: nothing is known yet. Nothing was refused; the record is asked
	// about again.
	Wait
)

const (
	// RefreshEvery is how often the files of a verified key and of the root
	// are read again, so a revocation or a change of the root is seen.
	RefreshEvery = time.Minute
	// MissingAfter is how long a kid with no file is waited for before it is
	// not certified: custody copies a certification a moment after it, so a
	// kid still without a file by then is one custody never certified.
	MissingAfter = 2 * time.Minute
	// Skew is how far a record's `at` may run ahead of this box's clock, and
	// how long after its key's not_after a record is still taken when first read.
	Skew = 5 * time.Minute
	// MaxKids bounds what the resolver remembers: a writer of a checkpoint file
	// can name any kid it likes.
	MaxKids = 256
	// StaleAfter is how long an answer read from the directory is trusted
	// without a fresh read of it and of the root: a directory that stops
	// reading turns every key into a wait, never into a key kept forever.
	StaleAfter = 3 * RefreshEvery
	// ForgetAfter is how long a key nobody asked about is remembered.
	ForgetAfter = time.Hour
	// tickEvery is how often the loop looks for a kid it has not read.
	tickEvery = 5 * time.Second
	maxFile   = 1 << 20
)

// RootFile is the delegation root's key set in the directory custody
// publishes into (keyryx KEYRYX_STATEMENTS_DIR).
const RootFile = "delegation-root.jwks.json"

var (
	streamRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,250}\.ndjson$`)
	keyRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// KidRe is a delegated key's kid, an RFC 7638 thumbprint: the only thing
	// that names a file this box opens in the directory.
	KidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// CheckDir reads HERALDYX_CHECKPOINT_STATEMENTS: a clean absolute path to a
// real directory that nobody but its owner can write and that this process's
// own uid does not own: the directory keyryx custody publishes its statements
// into, which this process mounts read only.
func CheckDir(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return fmt.Errorf("%q is not a clean absolute path", dir)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory (%s)", dir, info.Mode().Type())
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s can be written by its group or other users (mode %o); only custody writes it", dir, info.Mode().Perm())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) == os.Geteuid() && os.Geteuid() != 0 {
		return fmt.Errorf("%s belongs to this process's own uid; custody writes it, and this process only reads it", dir)
	}
	return nil
}

// ParseStreamKeys reads HERALDYX_CHECKPOINT_STREAM_KEYS: `stream|key-name`,
// comma or line separated, the delegated key name each stream's checkpoint key
// is certified under, as keyryx's witness reads KEYRYX_WITNESS_STREAM_KEYS. A
// stream bound to no name is never covered.
func ParseStreamKeys(spec string) (map[string]string, error) {
	out := map[string]string{}
	for _, item := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == '\n' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		stream, name, ok := strings.Cut(item, "|")
		if !ok || !streamRe.MatchString(stream) || !keyRe.MatchString(name) || strings.HasPrefix(name, reserved) {
			return nil, fmt.Errorf("%q is not stream|key-name", item)
		}
		if _, dup := out[stream]; dup {
			return nil, fmt.Errorf("%s is bound twice", stream)
		}
		out[stream] = name
	}
	if len(out) == 0 {
		return nil, errors.New("no stream is bound to a key name, so no stream could be covered")
	}
	return out, nil
}

// entry is what the resolver knows about one kid.
type entry struct {
	st        *Statement
	revokedAt int64
	first     time.Time // first asked about
	asked     time.Time // last asked about
	read      time.Time // last read attempted; zero until read once
	okRead    time.Time // last read that returned custody's file
	missing   bool      // the last read found no file, or none it could read
	// unreadable is set when the last read found a file that did not open:
	// not custody's word either way, so a key never verified waits on it
	// for as long as it lasts (the gate's MaxUndecided bounds the stream).
	unreadable bool
	reverify   bool // the root changed since this was verified
}

// Resolver answers, from memory only, whether a checkpoint record's kid is a
// key custody certified for its stream; a loop of its own (Run) reads the
// files custody publishes. Key and Still never touch a file, so the poll loop
// never waits on the directory.
type Resolver struct {
	dir      string
	pin      map[string]string
	bindings map[string]string
	now      func() time.Time
	logf     func(string, ...any)

	mu      sync.Mutex
	roots   Roots
	rootsAt time.Time // last root read attempted
	rootsOK time.Time // last root read that gave a root
	missing string
	full    bool // the bound was reached, said once
	entries map[string]*entry
	// revoked is every revocation this process has seen, kid to time. The
	// entries are pruned and capped; this is not, so a revoked kid forgotten
	// for want of being asked about comes back revoked, whatever its file
	// says then. Only custody revokes, rarely, so it stays small.
	revoked map[string]int64
	kick    chan struct{}
}

// NewResolver builds a resolver over dir. Nothing is read until Run or Step.
func NewResolver(dir string, pin, bindings map[string]string, now func() time.Time, logf func(string, ...any)) *Resolver {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Resolver{dir: dir, pin: pin, bindings: bindings, now: now, logf: logf, entries: map[string]*entry{}, revoked: map[string]int64{}, kick: make(chan struct{}, 1)}
}

// Key answers for a record of stream signed by kid at at, read for the first
// time now. Beyond Still's checks, the record is taken only while its key is
// alive by this box's clock (up to not_after plus Skew) and only when its `at`
// is not ahead of that clock by more than Skew: a key that has ended, or one
// taken from a producer, cannot sign a record dated into its life and have it
// taken later.
func (r *Resolver) Key(stream, kid string, at time.Time) (*ecdsa.PublicKey, Verdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	st, v := r.stillLocked(stream, kid, at, now)
	if v != Found {
		return nil, v
	}
	if at.After(now.Add(Skew)) || now.Unix() >= st.NotAfter+int64(Skew/time.Second) {
		return nil, NotCertified
	}
	return st.Public, Found
}

// Still answers for a record already taken: whether its key still covers it.
// A key that ended since still does; a key revoked since does not, nor one
// whose statement no longer verifies under custody's current root.
func (r *Resolver) Still(stream, kid string, at time.Time) (*ecdsa.PublicKey, Verdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, v := r.stillLocked(stream, kid, at, r.now())
	if v != Found {
		return nil, v
	}
	return st.Public, Found
}

// stillLocked is Still's decision under r.mu, returning the statement it
// judged so a caller reads it in the same critical section.
func (r *Resolver) stillLocked(stream, kid string, at, now time.Time) (*Statement, Verdict) {
	name, bound := r.bindings[stream]
	if !bound || !KidRe.MatchString(kid) {
		return nil, NotCertified
	}
	e := r.entries[kid]
	if e == nil {
		r.pruneLocked(now)
		if len(r.entries) >= MaxKids {
			// Asked again later, once a forgotten kid has made room: a
			// crowd of kids in one file must not cost another stream its key.
			if !r.full {
				r.full = true
				r.logf("checkpoint: %d keys are remembered, the most this box keeps; a new key waits until one is forgotten", MaxKids)
			}
			return nil, Wait
		}
		r.full = false
		e = &entry{first: now, revokedAt: r.revoked[kid]}
		r.entries[kid] = e
		e.asked = now
		select {
		case r.kick <- struct{}{}:
		default:
		}
		return nil, Wait
	}
	e.asked = now
	if e.st == nil {
		switch {
		case e.read.IsZero():
			return nil, Wait
		case e.unreadable, e.missing && now.Sub(e.first) < MissingAfter:
			return nil, Wait
		default:
			return nil, NotCertified
		}
	}
	st := e.st
	switch {
	case st.Name != name, st.Alg != "ES256", !st.ValidAt(at):
		return nil, NotCertified
	case e.revokedAt != 0 && now.Unix() >= e.revokedAt:
		// Judged on this box's clock, never the record's.
		return nil, NotCertified
	case now.Sub(e.okRead) > StaleAfter, now.Sub(r.rootsOK) > StaleAfter:
		// Not confirmed lately: neither its file nor the root has been
		// read for a while. A wait, never a key kept on an old answer.
		return nil, Wait
	}
	return st, Found
}

// pruneLocked forgets kids that have stayed not certified past the time a
// missing file is waited for, and kids nobody has asked about for
// ForgetAfter, so neither a file naming kid after kid nor a long life of
// renewed keys fills the resolver. The keys the gate still asks about (each
// stream's newest record's, every poll) are never forgotten. A key forgotten
// and asked about again is read again from custody's file.
func (r *Resolver) pruneLocked(now time.Time) {
	for kid, e := range r.entries {
		switch {
		case e.st == nil && !e.read.IsZero() && now.Sub(e.first) >= MissingAfter+RefreshEvery:
			delete(r.entries, kid)
		case now.Sub(e.asked) >= ForgetAfter:
			delete(r.entries, kid)
		}
	}
}

// Run reads the directory until ctx is done.
func (r *Resolver) Run(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		r.Step()
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
		case <-t.C:
		}
	}
}

// Step makes the reads that are due, once: the root key set every
// RefreshEvery, a kid not read yet at once, a kid with no file every tick, and
// every other kid every RefreshEvery. Run calls it; a test does too.
func (r *Resolver) Step() {
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		r.logf("checkpoint: the statements directory: %v; checkpoints wait", err)
		return
	}
	defer root.Close()
	r.mu.Lock()
	now := r.now()
	rootDue := r.roots == nil || now.Sub(r.rootsAt) >= RefreshEvery
	r.mu.Unlock()
	if rootDue {
		r.readRoots(root, now)
	}
	r.mu.Lock()
	if r.roots == nil {
		r.mu.Unlock()
		return
	}
	roots := r.roots
	var due []string
	for kid, e := range r.entries {
		switch {
		case e.read.IsZero(), e.reverify,
			e.missing && now.Sub(e.read) >= tickEvery,
			now.Sub(e.read) >= RefreshEvery:
			due = append(due, kid)
		}
	}
	r.mu.Unlock()
	for _, kid := range due {
		r.readKid(root, roots, kid, now)
	}
}

// errShape is a file that is there and is not of the shape custody writes: a
// link, not a regular file, or over its size cap. It is custody's directory
// saying something wrong, not a read that failed now.
var errShape = errors.New("not of the shape custody writes")

// readFile reads name from the directory: a regular file, never through a
// link, at most maxFile bytes.
func readFile(root *os.Root, name string) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%s is a link: %w", name, errShape)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file: %w", name, errShape)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("%s is over its size cap: %w", name, errShape)
	}
	return b, nil
}

func (r *Resolver) readRoots(root *os.Root, now time.Time) {
	b, err := readFile(root, RootFile)
	var roots Roots
	var missing []string
	if err == nil {
		roots, missing, err = RootsFromJWKS(b, r.pin)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rootsAt = now
	if err != nil {
		// No root to verify under: nothing new is verified, and what was
		// verified turns into a wait once it is StaleAfter old.
		r.roots = nil
		r.logf("checkpoint: the delegation root's key set: %v; checkpoints wait", err)
		return
	}
	r.rootsOK = now
	if said := strings.Join(missing, ","); said != r.missing {
		r.missing = said
		if said != "" {
			r.logf("checkpoint: custody's delegation root does not carry the pinned kids %s; a statement under them does not verify", said)
		}
	}
	// Only the pinned kids custody still publishes are a root: a half
	// custody revoked is no longer in its key set, and a statement it signed
	// stops verifying at the next read of that statement.
	r.roots = roots
	for _, e := range r.entries {
		e.reverify = true // read again under this root, in this step
	}
}

// readKid reads and verifies a kid's file outside the lock, under the roots
// of this step, and then records what it found.
func (r *Resolver) readKid(root *os.Root, roots Roots, kid string, now time.Time) {
	b, err := readFile(root, kid+".json")
	var st Statement
	var revoked int64
	var bad error
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		// Not custody's word either way (the file could not be opened or
		// read now): waited for as a missing file is, never a refusal.
	default:
		var resp struct {
			Statement json.RawMessage `json:"statement"`
			RevokedAt json.Number     `json:"revoked_at"`
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		dec.DisallowUnknownFields()
		if bad = dec.Decode(&resp); bad == nil {
			revoked, bad = strconv.ParseInt(resp.RevokedAt.String(), 10, 64)
		}
		if bad == nil && revoked < 0 {
			bad = errors.New("a negative revocation time")
		}
		if bad == nil {
			st, bad = Verify(resp.Statement, roots)
			if bad == nil && st.Kid != kid {
				bad = errors.New("the statement certifies another kid")
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[kid]
	if e == nil {
		return // forgotten while it was read
	}
	e.read, e.reverify = now, false
	if errors.Is(err, errShape) {
		bad, err = err, nil // custody's word, and a wrong one: not trusted
	}
	e.unreadable = err != nil && !errors.Is(err, fs.ErrNotExist)
	if err != nil {
		e.missing = true
		if errors.Is(err, fs.ErrNotExist) {
			e.st = nil // a file custody wrote is gone: nothing is believed
		} else {
			r.logf("checkpoint: custody's file for %s does not open now: %v; it is read again", kid, err)
		}
		return
	}
	e.missing = false
	if bad != nil {
		r.logf("checkpoint: custody's file for %s is not trusted: %v", kid, bad)
		e.st = nil
		return
	}
	e.okRead = now
	// A revocation, once seen, stays: custody never takes one back.
	if e.revokedAt != 0 && (revoked == 0 || revoked > e.revokedAt) {
		revoked = e.revokedAt
	}
	e.st, e.revokedAt = &st, revoked
	if revoked != 0 {
		r.revoked[kid] = revoked
	}
}
