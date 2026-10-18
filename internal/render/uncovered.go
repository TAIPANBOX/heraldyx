package render

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Uncovered is one stream file whose events were refused because no signed
// checkpoint covers it, as the renderer needs it. Reason is one of the fixed
// reasons of the checkpoint package, never producer text.
type Uncovered struct {
	File   string
	Reason string
	Count  int
}

// UncoveredStream renders the one notice raised for a stream the checkpoint
// gate refused. It says what was refused and why, so a refusal is never a
// silent gap in the mail: the operator is told the file was not read, and
// which setting decides it.
func UncoveredStream(cfg Config, u Uncovered, now time.Time) Message {
	file := shownName(filepath.Base(u.File))
	head := fmt.Sprintf("[%s] %s has no signed checkpoint, so its events are not mailed", boxName(cfg), file)

	var b strings.Builder
	fmt.Fprintf(&b, "%d event(s) in %s were read and not processed. This box reads a stream only while a signed checkpoint covers it (HERALDYX_CHECKPOINT_GATE is on), and for this file it could not establish one: %s.\n", u.Count, file, u.Reason)
	b.WriteString("\nThey are in no mail and not in the daily summary, and they were not processed as any source. This notice is how you learn they were refused.\n")
	b.WriteString("\nWhat this box already did: refused them and kept reading. It only reads the event log, so it cannot remove them, and they are still in the file.\n")
	b.WriteString("\nIf nobody acts: this file's events stay unmailed, and this box will not mail about this file again (it counts them in its own log). Check the producer's checkpoint file in HERALDYX_CHECKPOINTS, the key it is signed with in HERALDYX_CHECKPOINT_KEYS, and whether the producer's checkpoint writer is running.\n")
	return Message{Subject: head, Body: b.String()}
}
