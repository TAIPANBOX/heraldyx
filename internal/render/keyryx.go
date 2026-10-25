package render

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"
)

// keyryx is the custody and witness service (agent-passport SPEC.md 6.2:
// source keyryx, reserved until its first release, @decided 2026-10-18; v0.1.0
// was released the same day). Its
// nine types and agent-conform's chain_restarted are described here off the
// keys each one writes, and only those keys are read: each through its own
// shape check, the way [conformFacts] reads agent-conform's, so a key this file
// does not name is never printed and a hostile value costs only itself.
//
// The witness types carry counts and clipped digests in the design
// (security review, wave 2, section 3.2). The digests are never read here,
// because a digest tells an operator nothing they can act on in a mail, and
// the console and the witness's own log hold them. Which key names a witness
// finding carries is not yet fixed by SPEC 6.2 (`@claude 2026-10-18`, the
// reading of the design); a producer that writes other names is described
// without them.

// keyryxTypeSet are the types this file adds, for the fact routing in factLine.
var keyryxTypeSet = map[string]bool{
	"witness_stream_rewritten":    true,
	"witness_stream_truncated":    true,
	"witness_chain_restarted":     true,
	"witness_checkpoint_invalid":  true,
	"witness_checkpoint_unsigned": true,
	"witness_root_missing":        true,
	"witness_root_equivocated":    true,
	"key_rotated":                 true,
	"key_revoked":                 true,
}

// identName is the shape a key name, a kid, an algorithm or a tenant must have
// to be printed. Stricter than [safeString] on purpose: safeString allows
// spaces, and a sentence written by whoever controls a producer's string
// would pass it. A base64url kid, an algorithm and a tenant id all fit.
var identName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{0,63}$`)

// keyryxStream returns `data.stream` when it is a stream file name this box
// prints as written, and says whether the key was present but unusable.
func keyryxStream(e event.Event) (string, bool) {
	s, ok := e.Data["stream"].(string)
	if !ok || !streamFile.MatchString(s) {
		return "", false
	}
	return s, true
}

// streamOf returns the stream file an event names: `data.file` for
// agent-conform's chain_restarted, which names its stream as its verifier does,
// and `data.stream` for keyryx's own types.
func streamOf(e event.Event) (string, bool) {
	if e.Type == "chain_restarted" {
		return conformFile(e)
	}
	return keyryxStream(e)
}

// keyryxTenant returns `data.tenant` when it is an identifier this box prints.
func keyryxTenant(e event.Event) (string, bool) {
	s, ok := e.Data["tenant"].(string)
	if !ok || !identName.MatchString(s) {
		return "", false
	}
	return s, true
}

// keyryxIdent returns one of the key's own identifiers (name, kid, previous_kid,
// alg) when it is printable.
func keyryxIdent(e event.Event, key string) (string, bool) {
	s, ok := e.Data[key].(string)
	if !ok || !identName.MatchString(s) {
		return "", false
	}
	return s, true
}

// keyryxSubject puts the stream, or the tenant, into the subject line for the
// types that name one, because a mailbox is read as a list of subjects. A name
// this box cannot print leaves the base wording, as conformSubject does.
func keyryxSubject(e event.Event, p phrasing) phrasing {
	var subject string
	if s, ok := streamOf(e); ok {
		subject = s
	} else if s, ok := keyryxTenant(e); ok {
		subject = "tenant " + s
	} else {
		return p
	}
	switch e.Type {
	case "witness_stream_rewritten":
		p.what = "found that the bytes of " + subject + " changed after they were witnessed"
	case "witness_stream_truncated":
		p.what = "found " + subject + " holding fewer lines than before"
	case "witness_chain_restarted", "chain_restarted":
		p.what = "found the hash chain of " + subject + " starting again"
	case "witness_checkpoint_invalid":
		p.what = "found a signed checkpoint for " + subject + " that does not verify"
	}
	return p
}

// keyryxFacts names what the event says, off the keys above: the stream or
// tenant, and the counts a witness reports, or the key's name, kid, previous
// kid and algorithm. A value that fails its check is left out; when the stream
// name itself is present and unprintable, the mail says so instead of leaving a
// gap that looks like the producer named nothing.
func keyryxFacts(e event.Event) string {
	var parts []string
	if e.Type == "chain_restarted" {
		return chainRestartedFacts(e)
	}
	if e.Type == "key_rotated" || e.Type == "key_revoked" {
		return keyFacts(e)
	}
	if s, ok := keyryxStream(e); ok {
		parts = append(parts, "Stream file "+s)
	} else if t, ok := keyryxTenant(e); ok {
		parts = append(parts, "Tenant "+t)
	}
	if n, ok := wholeCount(e.Data["lines"], 0); ok {
		parts = append(parts, pluralCount(n, "line", "lines"))
	}
	if n, ok := wholeCount(e.Data["bytes"], 0); ok {
		parts = append(parts, pluralCount(n, "byte", "bytes"))
	}
	if n, ok := wholeCount(e.Data["segments"], 0); ok {
		parts = append(parts, fmt.Sprintf("segment count %d", n))
	}
	_, streamPresent := e.Data["stream"]
	var out string
	if len(parts) > 0 {
		out = strings.Join(parts, ", ") + "."
	}
	if streamPresent {
		if _, ok := keyryxStream(e); !ok {
			if out != "" {
				out += " "
			}
			out += "The stream name in this event is not one this box can print as written, so it is left out."
		}
	}
	return out
}

// keyFacts names a key's name, its new kid and the one it replaced, and its
// algorithm. key_revoked carries no previous kid, so only what it has is named.
func keyFacts(e event.Event) string {
	var parts []string
	if n, ok := keyryxIdent(e, "name"); ok {
		parts = append(parts, "key name "+n)
	}
	if k, ok := keyryxIdent(e, "kid"); ok {
		parts = append(parts, "kid "+k)
	}
	if p, ok := keyryxIdent(e, "previous_kid"); ok {
		parts = append(parts, "replaces kid "+p)
	}
	if a, ok := keyryxIdent(e, "alg"); ok {
		parts = append(parts, "algorithm "+a)
	}
	if len(parts) == 0 {
		return ""
	}
	joined := strings.Join(parts, ", ")
	return strings.ToUpper(joined[:1]) + joined[1:] + "."
}

// chainRestartedFacts names the stream and the line where the new chain starts,
// off agent-conform's `file` and `line`, read through the same checks
// conformFacts uses for its own keys.
func chainRestartedFacts(e event.Event) string {
	file, haveFile := conformFile(e)
	n, haveLine := wholeCount(e.Data["line"], 1)
	var parts []string
	if haveFile {
		parts = append(parts, "Stream file "+file)
	}
	if haveLine {
		parts = append(parts, fmt.Sprintf("the new chain starts at line %d", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + "."
}

// pluralCount is "1 line" or "12 lines".
func pluralCount(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
