package deliver

import (
	"os"
	"strings"
	"testing"

	"github.com/TAIPANBOX/heraldyx/internal/render"
)

// Every way a value can end a header line, or carry something a mail reader
// treats as one. CR and LF are the two the mail path refused from the start;
// the rest are the ones a check that names only those two lets through: a NUL
// that truncates a header in a C reader, the vertical tab and form feed some
// parsers fold on, the C1 next-line, and the two Unicode separators an editor
// or a log viewer renders as a new line.
var hostileHeaderValues = map[string]string{
	"a carriage return":      "x\rBcc: attacker@example.com",
	"a line feed":            "x\nBcc: attacker@example.com",
	"a crlf":                 "x\r\nReply-To: attacker@example.com",
	"a nul":                  "x\x00Bcc: attacker@example.com",
	"a vertical tab":         "x\vBcc: attacker@example.com",
	"a form feed":            "x\fBcc: attacker@example.com",
	"a delete":               "x\x7fy",
	"a next-line":            "x\u0085Bcc: attacker@example.com",
	"a line separator":       "x\u2028Bcc: attacker@example.com",
	"a paragraph separator":  "x\u2029Bcc: attacker@example.com",
	"an escape":              "x\x1b[2Jy",
	"a trailing line feed":   "x\n",
	"a lone line separator":  "\u2028",
	"a nul at the very end":  "x\x00",
	"a crlf before a header": "\r\nBcc: attacker@example.com",
}

// Values a header legitimately carries. A check that refused one of these
// would stop an ordinary alert, which is the failure this component exists to
// avoid.
var ordinaryHeaderValues = map[string]string{
	"plain ascii":       "[prod-box] run-42 is approaching its budget",
	"a tab":             "[prod-box]\trun-42",
	"cyrillic":          "[prod-box] бюджет вичерпано",
	"an emoji-free cjk": "[prod-box] 予算",
	"quoted id":         `[prod-box] "agent://acme.example/biller\n" is quoted`,
}

func TestTheFileTransportRefusesAHeaderABreakWouldOpen(t *testing.T) {
	for name, v := range hostileHeaderValues {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/mail.txt"
			f := NewFile(path)
			err := f.Send([]string{"ops@example.com"}, render.Message{Subject: v, Body: "b"})
			if err == nil {
				t.Fatalf("the file transport wrote a subject holding %s", name)
			}
			raw, rerr := os.ReadFile(path) // #nosec G304 -- temp path
			if rerr == nil && strings.Contains(string(raw), "Bcc:") {
				t.Fatalf("a header was opened in the file:\n%q", raw)
			}
		})
	}
}

func TestTheFileTransportRefusesARecipientABreakWouldOpen(t *testing.T) {
	for name, v := range hostileHeaderValues {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/mail.txt"
			f := NewFile(path)
			to := []string{"ops@example.com", "ops2@example.com" + v}
			if err := f.Send(to, render.Message{Subject: "s", Body: "b"}); err == nil {
				t.Fatalf("the file transport wrote a recipient holding %s", name)
			}
			if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 { // #nosec G304 -- temp path
				t.Fatalf("a refused message left bytes in the file:\n%q", raw)
			}
		})
	}
}

func TestEveryLineBreakAndControlIsRefusedInEveryHeader(t *testing.T) {
	for name, v := range hostileHeaderValues {
		for _, where := range []string{"from", "recipient", "subject"} {
			t.Run(where+" with "+name, func(t *testing.T) {
				from, to, subject := "box@example.com", []string{"ops@example.com"}, "s"
				switch where {
				case "from":
					from += v
				case "recipient":
					to = append(to, "ops2@example.com"+v)
				case "subject":
					subject = v
				}
				if raw, err := Compose(from, to, render.Message{Subject: subject, Body: "b"}, now); err == nil {
					t.Fatalf("composed a message with %s in the %s:\n%q", name, where, raw)
				}
			})
		}
	}
}

func TestBothTransportsJudgeAHeaderTheSameWay(t *testing.T) {
	judge := func(t *testing.T, subject string) (fileOK, smtpOK bool) {
		t.Helper()
		f := NewFile(t.TempDir() + "/mail.txt")
		m := render.Message{Subject: subject, Body: "b"}
		fileOK = f.Send([]string{"ops@example.com"}, m) == nil
		_, err := Compose("box@example.com", []string{"ops@example.com"}, m, now)
		return fileOK, err == nil
	}
	for name, v := range hostileHeaderValues {
		t.Run("hostile "+name, func(t *testing.T) {
			fileOK, smtpOK := judge(t, v)
			if fileOK || smtpOK {
				t.Fatalf("file accepted=%v, mail accepted=%v; both must refuse %s", fileOK, smtpOK, name)
			}
		})
	}
	for name, v := range ordinaryHeaderValues {
		t.Run("ordinary "+name, func(t *testing.T) {
			fileOK, smtpOK := judge(t, v)
			if !fileOK || !smtpOK {
				t.Fatalf("file accepted=%v, mail accepted=%v; both must accept %s", fileOK, smtpOK, name)
			}
		})
	}
}
