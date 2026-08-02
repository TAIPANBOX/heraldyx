// Package fleet keeps the short answer to "and what else is going on".
//
// An alert about one agent, on its own, is a fact without a situation. The
// operator woken by it immediately wants to know whether this is one agent
// having a bad night or the first of several, and today they would have to open
// the console to find out. So the mail carries a few lines of context: who else
// is near their line, who else is behaving unlike themselves, and in one short
// phrase what each is actually doing.
//
// It is built from the same event log the notifier already reads. No new
// input, no plane to ask, nothing that can be stale in a way the alert is not.
//
// What it deliberately does NOT do is remember across a restart. This is what
// this process has seen since it started, and a fresh process says less rather
// than claiming a picture it does not have. Persisting it would mean an alert
// that describes a fleet from before a rollout, which is worse than a short
// list.
package fleet

import (
	"fmt"
	"sort"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// Window is how far back the picture reaches. Beyond this an observation is
// history rather than "right now", and an alert that says an agent is looping
// when it stopped an hour ago is worse than one that says nothing.
const Window = 30 * time.Minute

// maxAgents bounds the memory. A fleet larger than this is exactly the case
// where the mail should stay short anyway.
const maxAgents = 2000

// Kind separates the two things an operator reacts to differently.
type Kind int

const (
	// NearTheLine is spend approaching a budget: nothing is wrong yet.
	NearTheLine Kind = iota
	// Odd is behaviour unlike the agent's own normal: a loop, a burst, a
	// fan-out, an identity anomaly.
	Odd
)

type observation struct {
	kind Kind
	what string
	when time.Time
}

// Picture is the rolling view.
type Picture struct {
	seen map[string]observation
}

// New returns an empty picture.
func New() *Picture { return &Picture{seen: map[string]observation{}} }

// describe turns an event into the one short phrase a human reads, or "" when
// this event type says nothing about how an agent is behaving.
//
// Deliberately narrow. Every phrase here is built from the event's own type and
// from allowlisted numeric fields, never from free text a producer wrote, for
// the same reason the mail body is: this goes out through somebody else's mail
// server.
func describe(e event.Event) (Kind, string) {
	num := func(key string) (int64, bool) {
		switch v := e.Data[key].(type) {
		case float64:
			return int64(v), true
		case int64:
			return v, true
		case int:
			return int64(v), true
		}
		return 0, false
	}
	occurrences := func() string {
		if n, ok := num("occurrences"); ok && n > 1 {
			return fmt.Sprintf(" (%d times)", n)
		}
		return ""
	}

	switch e.Type {
	case "budget_threshold":
		spent, ok1 := num("spent_micros")
		budget, ok2 := num("budget_micros")
		if ok1 && ok2 && budget > 0 {
			return NearTheLine, fmt.Sprintf("%.0f%% of budget", float64(spent)/float64(budget)*100)
		}
		return NearTheLine, "approaching its budget"
	case "budget_exhausted":
		return NearTheLine, "budget gone, calls refused"
	case "sustained_loop":
		return Odd, "repeating the same step" + occurrences()
	case "fanout_explosion":
		return Odd, "driving many runs at once" + occurrences()
	case "spend_spike":
		return Odd, "burning faster than its configured rate"
	case "mcp_drift":
		return Odd, "its MCP tool changed under the pinned lock"
	case "behavior_anomaly":
		return Odd, "behaving unlike its own history"
	case "impossible_travel":
		return Odd, "used from two places at once"
	case "quality_drift":
		return Odd, "output drifting from its baseline"
	case "dlp_block", "taint_block":
		return Odd, "blocked at the perimeter" + occurrences()
	default:
		return Odd, ""
	}
}

// Note records what an event says about an agent, if anything.
func (p *Picture) Note(e event.Event, now time.Time) {
	if e.AgentID == "" {
		return
	}
	kind, what := describe(e)
	if what == "" {
		return
	}
	if len(p.seen) >= maxAgents {
		if _, known := p.seen[e.AgentID]; !known {
			return
		}
	}
	p.seen[e.AgentID] = observation{kind: kind, what: what, when: now}
}

// Line is one rendered row of context.
type Line struct {
	Kind    Kind
	AgentID string
	What    string
}

// Around returns what else is going on, excluding the agent the alert is
// already about, newest first, at most limit rows. Anything older than
// [Window] is dropped, and dropped from memory too.
func (p *Picture) Around(exclude string, now time.Time, limit int) []Line {
	cutoff := now.Add(-Window)
	for id, o := range p.seen {
		if o.when.Before(cutoff) {
			delete(p.seen, id)
		}
	}

	out := make([]Line, 0, len(p.seen))
	for id, o := range p.seen {
		if id == exclude {
			continue
		}
		out = append(out, Line{Kind: o.kind, AgentID: id, What: o.what})
	}
	// Near-the-line first, because that is the one an operator can still act
	// on cheaply; then by agent id so the order is stable between two mails
	// about the same moment.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].AgentID < out[j].AgentID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Label is the column an operator reads first.
func (k Kind) Label() string {
	if k == NearTheLine {
		return "near the line"
	}
	return "behaving oddly"
}

// Short trims an agent id for a column without losing the recognisable end.
func Short(id string) string {
	if len(id) <= 40 {
		return id
	}
	return "..." + id[len(id)-37:]
}
