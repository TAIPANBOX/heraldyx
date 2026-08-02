// Package passport answers one question: who is answerable for this agent.
//
// The event envelope does not carry it. `agent_id` says which agent acted and
// `on_behalf_of` says who it was acting for at that moment, but neither is the
// OWNER: agent-passport SPEC.md section 4 makes `owner` a required field of the
// passport document, "a human or team principal", and that document lives on
// disk rather than in the stream.
//
// So this is an optional input. Point HERALDYX_PASSPORTS at a directory of
// passport JSON and an alert can name who to call and link to them; leave it
// unset and the alert simply does not carry that line. Nothing here is
// invented: an agent with no passport on disk has no owner as far as this
// process is concerned, and the mail says nothing rather than guessing.
//
// The distinction matters more than it looks, because the two answers have
// different blast radii when somebody decides to stop something. The owner is
// the team answerable for the agent existing at all. The `on_behalf_of`
// principal is whoever set this particular run in motion. Often the same
// human. Not always.
package passport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Directory maps agent ids to owners, re-reading the directory when it changes.
type Directory struct {
	path string

	mu      sync.RWMutex
	owners  map[string]string
	scanned time.Time
	// Malformed counts passport files that did not parse. Surfaced rather than
	// hidden: a file somebody hand-edited into invalidity is worth seeing, and
	// it is not a reason to stop reading the others.
	Malformed int
}

// Open returns a directory reader, or a disabled one when path is empty.
func Open(path string) *Directory {
	return &Directory{path: strings.TrimSpace(path), owners: map[string]string{}}
}

// Enabled reports whether any directory was configured.
func (d *Directory) Enabled() bool { return d != nil && d.path != "" }

// minRescan bounds how often the directory is walked. Passports change when an
// operator onboards an agent, which is a human-speed event; walking a
// directory every two seconds to notice it is a syscall storm for nothing.
const minRescan = 30 * time.Second

// OwnerOf returns the owner of an agent, or "" when there is no passport for
// it, no directory configured, or the passport has no owner.
func (d *Directory) OwnerOf(agentID string, now time.Time) string {
	if !d.Enabled() || agentID == "" {
		return ""
	}
	d.refresh(now)
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.owners[agentID]
}

// Count returns how many owners are known, for the startup log.
func (d *Directory) Count(now time.Time) int {
	if !d.Enabled() {
		return 0
	}
	d.refresh(now)
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.owners)
}

func (d *Directory) refresh(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.scanned.IsZero() && now.Sub(d.scanned) < minRescan {
		return
	}
	d.scanned = now

	matches, err := filepath.Glob(filepath.Join(d.path, "*.json"))
	if err != nil {
		return
	}
	owners := make(map[string]string, len(matches))
	malformed := 0
	for _, m := range matches {
		raw, err := os.ReadFile(m) // #nosec G304 -- operator-supplied directory
		if err != nil {
			malformed++
			continue
		}
		// Only the two fields this process has any business reading. A passport
		// carries policy, labels and a delegation chain; none of that belongs
		// in a mail, and a struct that cannot hold them cannot leak them.
		var doc struct {
			ID    string `json:"id"`
			Owner string `json:"owner"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			malformed++
			continue
		}
		if doc.ID == "" || doc.Owner == "" {
			continue
		}
		owners[doc.ID] = doc.Owner
	}
	d.owners = owners
	d.Malformed = malformed
}
