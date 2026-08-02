package render

import (
	"strings"
	"testing"
)

// The first message a box ever sends. It used to be built by handing Event a
// synthetic event of a type no catalog entry describes, so it read "raised an
// event this build does not have a description for" and carried a deep link to
// an incident that does not exist. Found on a live install, not in a test.
func TestTheTestMessageSaysItIsATest(t *testing.T) {
	m := Test(cfg(), now)
	if !strings.Contains(m.Subject, "notifications are working") {
		t.Fatalf("subject: %s", m.Subject)
	}
	for _, wrong := range []string{"does not have a description", "If nobody acts", "What this box already did"} {
		if strings.Contains(m.Body, wrong) {
			t.Errorf("the test message borrowed alert copy: %q\n%s", wrong, m.Body)
		}
	}
	if urlRe.MatchString(m.Body) {
		t.Fatalf("a test message must carry no link: there is nothing to open\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "Nothing is wrong") {
		t.Fatalf("it must say plainly that nothing is wrong:\n%s", m.Body)
	}
}
