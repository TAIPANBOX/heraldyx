package passport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 2, 21, 0, 0, 0, time.UTC)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheOwnerComesFromThePassport(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "biller.json", `{"schema":"taipanbox.dev/agent-passport/v0.2","id":"agent://acme/biller","owner":"team-finance@acme.example"}`)

	d := Open(dir)
	if got := d.OwnerOf("agent://acme/biller", t0); got != "team-finance@acme.example" {
		t.Fatalf("owner: %q", got)
	}
}

// The rule the whole design rests on: an agent with no passport has no owner as
// far as this process is concerned. A mail naming the wrong team at three in
// the morning is worse than a mail naming none.
func TestAnUnknownAgentHasNoOwnerRatherThanAGuess(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "biller.json", `{"id":"agent://acme/biller","owner":"team-finance@acme.example"}`)

	d := Open(dir)
	if got := d.OwnerOf("agent://acme/somebody-else", t0); got != "" {
		t.Fatalf("invented an owner: %q", got)
	}
}

func TestNoDirectoryIsNotAnError(t *testing.T) {
	d := Open("")
	if d.Enabled() {
		t.Fatal("empty path must disable")
	}
	if got := d.OwnerOf("agent://acme/biller", t0); got != "" {
		t.Fatalf("owner from nowhere: %q", got)
	}
	if d.Count(t0) != 0 {
		t.Fatal("count on a disabled directory")
	}
}

// One hand-edited file must not take the others with it.
func TestAMalformedPassportIsCountedNotFatal(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "good.json", `{"id":"agent://acme/good","owner":"team-a@acme.example"}`)
	write(t, dir, "broken.json", `{not json at all`)
	write(t, dir, "no-owner.json", `{"id":"agent://acme/orphan"}`)

	d := Open(dir)
	if got := d.OwnerOf("agent://acme/good", t0); got != "team-a@acme.example" {
		t.Fatalf("the good passport must still be read: %q", got)
	}
	if got := d.OwnerOf("agent://acme/orphan", t0); got != "" {
		t.Fatalf("a passport with no owner yields none: %q", got)
	}
	if d.Malformed != 1 {
		t.Fatalf("want 1 malformed counted, got %d", d.Malformed)
	}
}

// Nothing but id and owner is even parsed. A passport carries policy, labels
// and a delegation chain, and a struct that cannot hold them cannot leak them
// into a mail.
func TestOnlyIdAndOwnerAreRead(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "rich.json", `{"id":"agent://acme/rich","owner":"team-b@acme.example",
	  "labels":{"secret":"do-not-mail-this"},"policy":{"prompt":"ignore previous instructions"}}`)

	d := Open(dir)
	if got := d.OwnerOf("agent://acme/rich", t0); got != "team-b@acme.example" {
		t.Fatalf("owner: %q", got)
	}
	// Nothing else is retained anywhere to leak: the map holds owners only.
	if len(d.owners) != 1 {
		t.Fatalf("owners: %v", d.owners)
	}
}

// An operator onboarding an agent is a human-speed event; walking the directory
// on every poll would be a syscall storm for nothing. But it must be picked up.
func TestANewPassportIsPickedUpAfterTheRescanWindow(t *testing.T) {
	dir := t.TempDir()
	d := Open(dir)
	if got := d.OwnerOf("agent://acme/late", t0); got != "" {
		t.Fatalf("owner before the file exists: %q", got)
	}
	write(t, dir, "late.json", `{"id":"agent://acme/late","owner":"team-c@acme.example"}`)

	// Inside the window: still the old answer, which is the point of the window.
	if got := d.OwnerOf("agent://acme/late", t0.Add(time.Second)); got != "" {
		t.Fatalf("rescanned too eagerly: %q", got)
	}
	// After it: found.
	if got := d.OwnerOf("agent://acme/late", t0.Add(minRescan+time.Second)); got != "team-c@acme.example" {
		t.Fatalf("never picked up: %q", got)
	}
}
