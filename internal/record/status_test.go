package record

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeJournal(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		d := dispatch()
		d.About = "budget_exhausted:run-" + string(rune('a'+i))
		j.Sent(d, now.Add(time.Duration(i)*time.Minute))
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusCountsWhatWasSent(t *testing.T) {
	st, err := ReadStatus(writeJournal(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Present || st.Records != 3 {
		t.Fatalf("records: %+v", st)
	}
	if st.Accepted != 3 || st.Refused != 0 {
		t.Fatalf("outcomes: %+v", st)
	}
	if st.ByKind["alert"] != 3 {
		t.Fatalf("kinds: %v", st.ByKind)
	}
	if !st.Ok() {
		t.Fatalf("a freshly written journal is intact: %+v", st)
	}
	out := st.String()
	for _, want := range []string{"records: 3 (alert 3)", "outcome: 3 accepted", "chain:   verifies", "last:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// The whole point of the file. A quiet edit is reported, and it is NOT
// repaired: a tool that tidied a break would be the thing that quietly changed
// the record.
func TestStatusReportsATamperedRecordAsBroken(t *testing.T) {
	path := writeJournal(t, 3)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before := string(raw)
	tampered := strings.Replace(before, "ops@example.com", "nobody@example.com", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ok() || st.Breaks == 0 {
		t.Fatalf("an edited record must not read as intact: %+v", st)
	}
	if !strings.Contains(st.String(), "BROKEN") {
		t.Fatalf("the report must say so plainly:\n%s", st.String())
	}

	after, _ := os.ReadFile(path)
	if string(after) != tampered {
		t.Fatal("reading the journal must never rewrite it")
	}
}

// A chain of one binds nothing, and the report must say that rather than
// "verifies". Found by running the tool against a real one-record journal from
// `make demo`, editing the only line, and watching it still report a good
// chain: correct behaviour for a hash chain, and a misleading sentence.
func TestASingleRecordIsNotAVerifiedChain(t *testing.T) {
	st, err := ReadStatus(writeJournal(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	out := st.String()
	if strings.Contains(out, "verifies") {
		t.Fatalf("one record cannot be a verified chain:\n%s", out)
	}
	if !strings.Contains(out, "a chain of one binds nothing") {
		t.Fatalf("say what it can and cannot prove:\n%s", out)
	}

	// And two records DO bind.
	st2, err := ReadStatus(writeJournal(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st2.String(), "chain:   verifies") {
		t.Fatalf("two records make one link:\n%s", st2.String())
	}
}

// A box that has had nothing to say has written nothing, and that is ordinary.
func TestStatusOnAMissingJournalIsNotAFailure(t *testing.T) {
	st, err := ReadStatus(filepath.Join(t.TempDir(), "never-written.ndjson"))
	if err != nil {
		t.Fatalf("a journal that does not exist yet is not an error: %v", err)
	}
	if st.Present || !st.Ok() {
		t.Fatalf("%+v", st)
	}
	if !strings.Contains(st.String(), "records: none yet") {
		t.Fatalf("say so plainly:\n%s", st.String())
	}
}

func TestStatusWithRecordingOffSaysSo(t *testing.T) {
	st, err := ReadStatus("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.String(), "recording is off") {
		t.Fatalf("%s", st.String())
	}
}

func TestStatusSplitsRefusals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.ndjson")
	j, _ := Open(path)
	j.Sent(dispatch(), now)
	d := dispatch()
	d.Err = errTest{}
	j.Sent(d, now.Add(time.Minute))
	j.Close()

	st, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Accepted != 1 || st.Refused != 1 {
		t.Fatalf("%+v", st)
	}
}

type errTest struct{}

func (errTest) Error() string { return "550 5.7.1 relay denied" }
