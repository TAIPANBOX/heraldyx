// Package checkpoint decides whether a stream file is covered by a signed
// checkpoint, the reader half of F-018 (security review 2026-10-17, wave 2).
//
// A producer that signs its stream writes beside it a checkpoint file only it
// can write (agent-passport SPEC.md 6.5.1, format v1). A signed record binds,
// under its key, the line count of the stream, the SHA-256 of its first N
// bytes (`prefix`) and the chain hash of its last line (`head`). This package
// checks the records in the order SPEC 6.5.1 lists, against the stream, and
// answers one question per stream file: is there a signed record, under a key
// this box pinned, whose bytes the stream still has?
//
// What it does NOT do, and why each is held elsewhere:
//
//   - It does not check that a pinned key was valid at the record's `at` time
//     or has not been revoked. SPEC 6.5.1 leaves that to the service that
//     certifies the key (keyryx's key statements), which this box does not
//     read. A pinned key is trusted until the operator removes it.
//   - It checks step 5 (every EARLIER record still holds) for nothing. Only the
//     newest signed record is kept, so the earlier ones are not held. Keeping
//     earlier checkpoints beyond the producer's reach is the witness's job.
//   - Coverage is a property of the stream file, not of each line. A line
//     after the newest checkpoint is admitted by the stream's coverage until
//     the next checkpoint arrives.
//
// This package does the file reads it needs and nothing else: no network, no
// writes. It is used from the poll loop only and is not safe for concurrent use.
package checkpoint

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"
	"github.com/gowebpki/jcs"

	"github.com/TAIPANBOX/heraldyx/internal/regular"
)

// Reasons a stream is or is not covered. They are fixed strings of this
// package, never text read from a producer, so they may be printed.
const (
	ReasonCovered        = "a signed checkpoint covers the stream"
	ReasonNoFile         = "no checkpoint file for this stream"
	ReasonCkptUnreadable = "the checkpoint file could not be read"
	ReasonRefused        = "the checkpoint file holds a record this box refuses"
	ReasonOtherStrm      = "the checkpoint file names another stream"
	ReasonNoSigned       = "no signed checkpoint in the file verifies under a pinned key"
	ReasonShorter        = "the stream is shorter than its newest checkpoint"
	ReasonRewritten      = "the stream's bytes no longer match the newest checkpoint"
	ReasonHead           = "the chain hash of the checkpoint's last line does not match"
	ReasonUnreadable     = "the stream could not be read"
)

const genesis = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

var (
	hashShape = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	atShape   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`)
	countText = regexp.MustCompile(`^[1-9][0-9]{0,15}$`)
	kinds     = map[string]bool{"checkpoint": true, "close": true, "resume": true}
	// allowed is every member SPEC 6.5.1 lists. A line with any other member is
	// refused, not read past.
	allowed = map[string]bool{
		"v": true, "kind": true, "stream": true, "count": true, "head": true,
		"prefix": true, "at": true, "prev": true, "kid": true, "alg": true,
		"sig": true, "unsigned_reason": true,
	}
	required = []string{"v", "kind", "stream", "count", "head", "prefix", "at", "prev"}
)

// Gate answers, per stream file, whether a signed checkpoint covers it.
type Gate struct {
	dir   string
	keys  map[string]*ecdsa.PublicKey
	files map[string]*file
}

// file is what the gate has verified of one checkpoint file. The file is
// append-only in use: a record once verified is not read again unless the
// file gets shorter, which starts it over.
type file struct {
	offset  int64   // bytes verified, through the last complete record
	prev    string  // the `prev` the next record must carry
	refused string  // set once a record is refused; sticky until the file shrinks
	best    *record // the newest record that verified under a pinned key
	gen     uint64  // counts the signed records that have verified
	// The stream verdict, for the generation it was computed at. Recomputed
	// when a new signed record arrives, and kept otherwise: see Covered.
	verdictSet bool
	verdictGen uint64
	covered    bool
	why        string
}

// record is the part of a signed record the stream check needs.
type record struct {
	count  int64
	head   string
	prefix string
}

// NewGate builds a gate that reads checkpoint files from dir and verifies
// signatures against keys, as LoadKeys returns them.
func NewGate(dir string, keys map[string]*ecdsa.PublicKey) *Gate {
	return &Gate{dir: dir, keys: keys, files: map[string]*file{}}
}

// Covered reports whether the stream at streamPath is covered by a signed
// checkpoint, and why not. The checkpoint file is `<stream>.ckpt.ndjson` in the
// gate's directory.
//
// The checkpoint file is read forward from where the last call left off. The
// stream is hashed up to the newest checkpoint's `count` only when a new
// checkpoint arrives, so a stream that grows under an unchanged checkpoint is
// not re-hashed on every poll. The one case not cached is a stream shorter than
// its checkpoint: that can be a stale size on a shared volume, and it is asked
// again on the next poll rather than refused for a whole checkpoint interval.
func (g *Gate) Covered(streamPath string) (bool, string) {
	stream := filepath.Base(streamPath)
	ckpt := filepath.Join(g.dir, stream+".ckpt.ndjson")
	info, err := os.Stat(ckpt)
	if err != nil {
		delete(g.files, ckpt)
		return false, ReasonNoFile
	}
	f := g.files[ckpt]
	if f == nil || info.Size() < f.offset {
		f = &file{prev: genesis}
		g.files[ckpt] = f
	}
	if f.refused == "" && info.Size() > f.offset {
		f.advance(ckpt, stream, info.Size(), g.keys)
	}
	if f.refused != "" {
		return false, f.refused
	}
	if f.best == nil {
		return false, ReasonNoSigned
	}
	if !f.verdictSet || f.verdictGen != f.gen {
		covered, why := verifyStream(streamPath, f.best)
		if why == ReasonShorter {
			return false, why
		}
		f.verdictSet, f.verdictGen = true, f.gen
		f.covered, f.why = covered, why
	}
	return f.covered, f.why
}

// advance verifies the complete records between the file's offset and size,
// in order, against SPEC 6.5.1 steps 1 to 3 and the signature of step 2. A
// final line with no LF is a torn append and is left for the next call.
func (f *file) advance(path, stream string, size int64, keys map[string]*ecdsa.PublicKey) {
	fh, err := os.Open(path) // #nosec G304 -- the checkpoint path is operator-supplied configuration, the same as the event paths
	if err != nil {
		f.refused = ReasonCkptUnreadable
		return
	}
	defer fh.Close()
	r := bufio.NewReader(io.NewSectionReader(fh, f.offset, size-f.offset))
	for {
		raw, err := r.ReadBytes('\n')
		if err == io.EOF {
			return
		}
		if err != nil {
			f.refused = ReasonCkptUnreadable
			return
		}
		body := raw[:len(raw)-1]
		rec, signed, why := checkRecord(body, f.prev, stream, keys)
		if why != "" {
			f.refused = why
			return
		}
		f.prev = link(body)
		f.offset += int64(len(raw))
		if signed {
			f.best = rec
			f.gen++
		}
	}
}

// checkRecord applies SPEC 6.5.1 steps 1 to 3 to one record (body, without its
// LF), and step 2's signature check when the record is signed. It returns the
// record and whether it is a signed record that verified under a pinned key.
// A record that is well formed but unsigned, or signed by a key this box did
// not pin, is chained and returned as not signed, which is what SPEC 6.5.1
// asks: it is reported as such, never as valid. A line that breaks the format
// or the chain returns a reason, and the file is refused.
func checkRecord(body []byte, prev, stream string, keys map[string]*ecdsa.PublicKey) (*record, bool, string) {
	canon, err := jcs.Transform(body)
	if err != nil || !bytes.Equal(canon, body) {
		return nil, false, ReasonRefused
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, false, ReasonRefused
	}
	for k := range obj {
		if !allowed[k] {
			return nil, false, ReasonRefused
		}
	}
	for _, k := range required {
		if _, ok := obj[k]; !ok {
			return nil, false, ReasonRefused
		}
	}
	if string(obj["v"]) != "1" {
		return nil, false, ReasonRefused
	}
	var kind, name string
	if json.Unmarshal(obj["kind"], &kind) != nil || !kinds[kind] {
		return nil, false, ReasonRefused
	}
	if json.Unmarshal(obj["stream"], &name) != nil {
		return nil, false, ReasonRefused
	}
	if name != stream {
		return nil, false, ReasonOtherStrm
	}
	var count int64
	if !countText.Match(obj["count"]) {
		return nil, false, ReasonRefused
	}
	if count, err = strconv.ParseInt(string(obj["count"]), 10, 64); err != nil {
		return nil, false, ReasonRefused
	}
	head, okHead := hashValue(obj["head"])
	prefix, okPrefix := hashValue(obj["prefix"])
	gotPrev, okPrev := hashValue(obj["prev"])
	var at string
	if json.Unmarshal(obj["at"], &at) != nil || !atShape.MatchString(at) || !okHead || !okPrefix || !okPrev {
		return nil, false, ReasonRefused
	}
	// Step 3: the chain of records. The first record carries the genesis value.
	if gotPrev != prev {
		return nil, false, ReasonRefused
	}
	_, hasAlg := obj["alg"]
	_, hasKid := obj["kid"]
	_, hasSig := obj["sig"]
	_, hasReason := obj["unsigned_reason"]
	if hasReason {
		if hasAlg || hasKid || hasSig {
			return nil, false, ReasonRefused
		}
		return nil, false, ""
	}
	if !hasAlg || !hasKid || !hasSig {
		return nil, false, ReasonRefused
	}
	var alg, kid, sig string
	if json.Unmarshal(obj["alg"], &alg) != nil || alg != "ES256" ||
		json.Unmarshal(obj["kid"], &kid) != nil || json.Unmarshal(obj["sig"], &sig) != nil {
		return nil, false, ReasonRefused
	}
	pub, pinned := keys[kid]
	if !pinned || !verifySignature(pub, obj, sig) {
		return nil, false, ""
	}
	return &record{count: count, head: head, prefix: prefix}, true, ""
}

// verifySignature checks sig over the JCS serialization of the record with sig
// removed (SPEC 6.5.1, "Signing"): ES256, 64 bytes of r then s.
func verifySignature(pub *ecdsa.PublicKey, obj map[string]json.RawMessage, sig string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != 64 {
		return false
	}
	unsigned := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		if k != "sig" {
			unsigned[k] = v
		}
	}
	input, err := json.Marshal(unsigned)
	if err != nil {
		return false
	}
	canon, err := jcs.Transform(input)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(canon)
	r := new(big.Int).SetBytes(raw[:32])
	s := new(big.Int).SetBytes(raw[32:])
	return ecdsa.Verify(pub, digest[:], r, s)
}

// verifyStream applies SPEC 6.5.1 steps 4 and 5's newest-record part to the
// stream: it must hold at least `count` complete lines, its first N bytes must
// hash to `prefix`, and the chain hash of line `count` must be `head`.
func verifyStream(streamPath string, rec *record) (bool, string) {
	fh, err := regular.Open(streamPath) // a link, a FIFO or anything but a regular file is unreadable (invariant 33)
	if err != nil {
		return false, ReasonUnreadable
	}
	defer fh.Close()
	r := bufio.NewReaderSize(fh, 1<<16)
	h := sha256.New()
	var last []byte
	for i := int64(0); i < rec.count; i++ {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return false, ReasonShorter
		}
		if err != nil {
			return false, ReasonUnreadable
		}
		h.Write(line)
		last = line[:len(line)-1]
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != rec.prefix {
		return false, ReasonRewritten
	}
	head, err := event.ChainHashRaw(last)
	if err != nil || head != rec.head {
		return false, ReasonHead
	}
	return true, ReasonCovered
}

// link is the `prev` value of the record after body: SHA-256 of body's bytes.
func link(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// hashValue returns a JSON string member when it is a sha256 digest.
func hashValue(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil || !hashShape.MatchString(s) {
		return "", false
	}
	return s, true
}

// LoadKeys reads the pinned public keys: a JSON object from kid to the
// base64url uncompressed P-256 point (65 bytes, 0x04 first). A keys file with
// no key in it is refused, because a gate that could verify nothing would
// refuse every stream and say nothing about why.
func LoadKeys(path string) (map[string]*ecdsa.PublicKey, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the keys path is operator-supplied configuration
	if err != nil {
		return nil, fmt.Errorf("checkpoint keys: %w", err)
	}
	var pinned map[string]string
	if err := json.Unmarshal(raw, &pinned); err != nil {
		return nil, fmt.Errorf("checkpoint keys %s: want a JSON object from kid to key: %w", path, err)
	}
	if len(pinned) == 0 {
		return nil, fmt.Errorf("checkpoint keys %s: no key is pinned, so no record could be verified", path)
	}
	keys := make(map[string]*ecdsa.PublicKey, len(pinned))
	for kid, enc := range pinned {
		b, err := base64.RawURLEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("checkpoint keys %s: kid %q is not base64url", path, kid)
		}
		// Parsing checks the 0x04 prefix, the length and that the point lies on
		// P-256. Setting the coordinates by hand would skip that check.
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), b)
		if err != nil {
			return nil, fmt.Errorf("checkpoint keys %s: kid %q is not an uncompressed point on P-256", path, kid)
		}
		keys[kid] = pub
	}
	return keys, nil
}
