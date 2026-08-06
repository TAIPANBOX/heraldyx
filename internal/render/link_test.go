package render

import (
	"net/url"
	"strings"
	"testing"
)

// The path keeps the slashes it is made of, so an agent id reaches a mailbox
// looking like itself.
//
// A mail is text/plain: the address is its own link text, and until 2026-08-03
// every slash arrived as `%2F`, which made the one line an operator has to read
// at three in the morning longer than the id it names and harder to read than
// the id it names.
func TestAnAgentIdInALinkKeepsItsSlashes(t *testing.T) {
	cfg := Config{ConsoleURL: "https://box"}
	const id = "agent://meridian.io/finops/unit-economics-analyst"

	got := AgentLink(cfg, id)
	want := "https://box/a/" + id
	if got != want {
		t.Fatalf("agent link\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "%2F") {
		t.Fatalf("still escaping the separators: %q", got)
	}
	if old := "https://box/a/" + url.PathEscape(id); len(got) >= len(old) {
		t.Fatalf("no shorter than escaping the whole id: %d vs %d", len(got), len(old))
	}

	// The round trip is the point: whatever the console reads after the prefix
	// has to be the id that was sent.
	back, err := url.PathUnescape(strings.TrimPrefix(got, "https://box/a/"))
	if err != nil || back != id {
		t.Fatalf("does not round trip: %q -> %q (%v)", got, back, err)
	}
}

// Anything inside a segment that would change the shape of the URL is still
// escaped. Only the separators are left alone.
//
// The probe moved twice, and for the same reason both times: this test is about
// `escapePath`, and the exported functions built on it have each grown a shape
// check in front of it. It ran through OwnerLink until 2026-08-05, when
// `sanitizeOwner` began refusing "team a/b?c#d" (not a realistic owner), and
// through AgentLink until [addressable] began refusing the same string for the
// same reason: `?` and `#` are not characters an identifier may be made of, so
// an id carrying either is not addressable at all now.
//
// So the three properties are asserted where each one lives. A space is the one
// character an id may be written with that still has to be escaped before it
// can sit in a URL, and it exercises the public function; the `?#` probe
// exercises the refusal; `escapePath` is called directly for what it does with
// a segment, which is unchanged.
func TestWhatIsInsideASegmentIsStillEscaped(t *testing.T) {
	cfg := Config{ConsoleURL: "https://box"}

	got := AgentLink(cfg, "team a/b")
	if strings.ContainsAny(strings.TrimPrefix(got, "https://box/a/"), " ") {
		t.Fatalf("a segment reached the URL unescaped: %q", got)
	}
	if !strings.Contains(got, "/a/team%20a/b") {
		t.Fatalf("unexpected escaping: %q", got)
	}

	if got := AgentLink(cfg, "team a/b?c#d"); got != "" {
		t.Fatalf("an id shaped nothing like an id was still turned into a link: %q", got)
	}

	if got := escapePath("team a/b?c#d"); got != "team%20a/b%3Fc%23d" {
		t.Fatalf("escapePath changed what it does inside a segment: %q", got)
	}
}
