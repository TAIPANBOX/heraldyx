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
func TestWhatIsInsideASegmentIsStillEscaped(t *testing.T) {
	cfg := Config{ConsoleURL: "https://box"}
	got := OwnerLink(cfg, "team a/b?c#d")
	if strings.ContainsAny(strings.TrimPrefix(got, "https://box/o/"), "?# ") {
		t.Fatalf("a segment reached the URL unescaped: %q", got)
	}
	if !strings.Contains(got, "/o/team%20a/b%3Fc%23d") {
		t.Fatalf("unexpected escaping: %q", got)
	}
}
