// Package render turns one agent-event into the message a human reads.
//
// Two rules here are not style, they are the reason this package exists as its
// own unit with its own tests:
//
//  1. The mail carries identifiers and numbers, never content. An event's
//     `data` may hold anything a producer put there, and some producers sit
//     next to prompts, model output and matched secrets. Mail leaves the
//     operator's perimeter through a server we do not control, so `data` is
//     rendered through an ALLOWLIST of keys whose values must also look like
//     identifiers or numbers. A denylist would be one new producer away from
//     leaking.
//  2. The mail never carries an action. It carries one link, into the
//     operator's own console, opened at the thing that happened. A link that
//     acts is an unauthenticated capability held by anyone who sees or
//     forwards the mail, and mail security gateways prefetch links, which
//     would fire the action before a human read the sentence next to it.
package render

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/heraldyx/internal/rule"
)

// Around is one line of "and what else is going on", passed in by the caller
// rather than gathered here, because this package does no I/O and holds no
// state (see `scripts/one-way-out.sh`).
type Around struct {
	Label   string
	AgentID string
	What    string
}

// Config is what the renderer needs that an event cannot tell it.
type Config struct {
	// Box is the operator's own name for this deployment, used in the subject
	// line so mail rules can file it and a human with two boxes can tell them
	// apart.
	Box string
	// ConsoleURL is the base of the operator's console, e.g.
	// https://box.example.com. Empty means no link is rendered at all, which
	// is honest: a link to nowhere is worse than a sentence saying where to
	// look.
	ConsoleURL string
}

// Message is one rendered mail.
type Message struct {
	Subject string
	Body    string
}

// dataAllowlist is every `data` key that may reach a mailbox. Adding one is a
// deliberate act: the question to answer first is not "is this useful" but
// "can this key ever hold text a model wrote".
var dataAllowlist = map[string]bool{
	"org":           true,
	"occurrences":   true,
	"budget_micros": true,
	"spent_micros":  true,
	"actor":         true,
	"decision":      true,
	"tool":          true,
	"kind":          true,
	"count":         true,
	"window":        true,
	"upstream":      true,
}

// safeString is the shape a string value must have to be rendered: short, one
// line, and made of the characters identifiers and ids are made of. A prompt
// fragment or a model's answer does not survive this.
var safeString = regexp.MustCompile(`^[A-Za-z0-9_:@./\- ]{1,64}$`)

// maxDataFields caps how much of `data` reaches the mail even when every key
// is allowlisted, so a producer that adds twenty fields does not turn the
// message into a dump.
const maxDataFields = 6

// phrasing is what a type MEANS, in the three sentences a human needs: what
// happened, what the box already did about it, and what happens if nobody
// acts. Types the registry does not list fall back to a generic, honest line
// rather than a guess.
type phrasing struct {
	what string
	did  string
	next string
}

var catalog = map[string]phrasing{
	"budget_threshold": {
		what: "is approaching its budget",
		did:  "Nothing yet. The run is still inside its budget and is running normally.",
		next: "When the budget is gone, further calls are refused with a hard 402 and the run stops making progress.",
	},
	"budget_exhausted": {
		what: "has exhausted its budget",
		did:  "Calls from this run are being refused with a hard 402.",
		next: "The run cannot spend again until someone raises its budget.",
	},
	"run_killed": {
		what: "was killed",
		did:  "The run is stopped. Gateways refuse its calls.",
		next: "Nothing. This is a final state until a new run is started.",
	},
	"sustained_loop": {
		what: "is repeating the same step",
		did:  "The loop detector has flagged the run. Spend continues unless a budget stops it.",
		next: "A loop that nobody interrupts spends the whole budget on the same step.",
	},
	// Since tokenfuse#156 this is a CHANGE, not a height: the last minute
	// against the org's own preceding half hour, times a multiple, with the
	// configured rate kept only as a floor. The old sentence, "faster than its
	// configured rate", described the predicate that was replaced.
	"spend_spike": {
		what: "is burning money far faster than it usually does",
		did:  "Nothing automatic. This is a rate observation across the whole org, against its own recent normal.",
		next: "If the rate holds, budgets set for a normal day are gone inside hours.",
	},
	// "Unusual" became true in tokenfuse#158: the count is measured against
	// this agent's own habit rather than a fixed number, so the word now
	// describes what fired. Saying which habit is the useful part.
	"fanout_explosion": {
		what: "is driving far more runs at once than it usually does",
		did:  "Nothing automatic. The runs are being attributed to one agent.",
		next: "Fan-out multiplies spend and makes per-run budgets ineffective.",
	},
	"breaker_tripped": {
		what: "was refused by the breaker",
		did:  "The call was refused with a hard 402 before it reached the provider.",
		next: "The agent sees an error. Whether it retries or stops is up to the agent.",
	},
	"dlp_block": {
		what: "tried to send something that matched a secret pattern",
		did:  "The call was blocked before it left the perimeter.",
		next: "The agent sees an error. The matched value is NOT included in this mail.",
	},
	// NOT "blocked before it left the perimeter", which is what this said
	// until 2026-08-03 and is the opposite of what happens. The firewall
	// evaluates the RESPONSE: the provider call already went out and its cost
	// is recorded as an ordinary allow, and what gets blocked is the answer
	// reaching the agent. An operator told the call was stopped assumes no
	// spend and no exposure, and is wrong about both.
	"taint_block": {
		what: "was refused a tool its taint labels do not allow",
		did:  "The provider call had already gone out and was paid for. What was blocked is the response reaching the agent.",
		next: "The agent sees an error. Its run continues under the same rules.",
	},
	"identity_mismatch": {
		what: "presented a credential that may not speak as the agent it claimed",
		did:  "The call was refused.",
		next: "Either the identity map is wrong or something is using the wrong key.",
	},
	"mcp_drift": {
		what: "is talking to an MCP tool that changed under its pinned lock",
		did:  "The drift was recorded. Whether calls are refused depends on the broker's mode.",
		next: "A tool that changed after it was approved is the rug-pull case this check exists for.",
	},
	// A business unit's monthly cap, refused at the gateway (tokenfuse
	// docs/20). Deliberately says "this gateway": the unit ledger is
	// in-process and per-gateway, resets on restart, and is not
	// fleet-consistent, which its own module says plainly. A sentence
	// implying a fleet-wide cap would be the same class of falsehood as the
	// four fixed on 2026-08-03.
	"unit_cap_exceeded": {
		what: "has spent its business unit's monthly cap",
		did:  "Calls attributed to that unit are being refused at this gateway with a hard 402.",
		next: "Every agent in the unit is refused there until someone raises the cap or the UTC month rolls over.",
	},
	"policy_deny": {
		what: "was denied by policy",
		did:  "The action was refused at the decision point.",
		next: "The agent cannot take this action until the policy changes.",
	},
	"approval_requested": {
		what: "is waiting for a human decision",
		did:  "The action is held. Nothing is running and nothing is refused yet.",
		// Nothing expires a hold. There is no sweeper in the policy plane and
		// `Pending()` there means only "no decision yet", so the previous
		// "it eventually times out" told an operator the system would resolve
		// this without them. It will not, and the agent stays blocked.
		next: "A held action stays held until a human decides. Nothing expires it on its own.",
	},
	// The hold nobody answered, which until 2026-08-03 had no event at all.
	// Its sentence has to be about the HUMAN, not the agent: the agent is
	// doing exactly what it was told, and the only thing that moves this is
	// somebody deciding.
	"approval_unanswered": {
		what: "is still waiting for a human decision nobody has made",
		did:  "The action is held. Nothing is running and nothing is refused.",
		next: "Nothing expires it. The agent waits until a person decides, however long that is.",
	},
	// Not a hold that nobody answered. The policy plane raises this when an
	// agent REDEEMS an approval whose window has closed, which means a human
	// very likely did decide, and the agent came back too late. Sending
	// somebody to look at an unattended approval queue is the wrong errand.
	"approval_timeout": {
		what: "presented an approval that had already expired",
		did:  "The action was refused. The approval was granted earlier and its window had closed by the time the agent redeemed it.",
		next: "Nothing further happens on its own. A fresh approval is needed.",
	},
	"quality_drift": {
		what: "is producing worse output than its baseline",
		did:  "The drift was measured and recorded. No traffic was stopped.",
		next: "Quality drift does not stop anything by itself. It is a signal to look.",
	},
	// `behavior_anomaly` and `excessive_privilege` were here until 2026-08-03
	// and were removed because nothing raises them. Both are concepts of the
	// identity plane, and idryx has no event writer at all: it READS this log
	// to build its graph and answers through its own API. Measured across the
	// producing repositories, neither type is emitted anywhere.
	//
	// A catalog entry is a claim about what another plane did. Keeping one for
	// an event that cannot arrive is the same defect as the four entries this
	// file already carries tests against, only quieter: nobody ever sees it be
	// wrong. If the identity plane grows a writer, add them back by reading its
	// code, not by copying these sentences out of git history.
	"sim_finding": {
		what: "failed a rehearsal",
		// "Production was not touched" was a claim about the operator's setup
		// that this event does not carry: the drill runs against whichever
		// gateway it was pointed at, normally a pre-production one, and
		// nothing here can tell.
		did:  "The drill recorded a guardrail that did not hold, against the gateway it was pointed at.",
		next: "A guardrail that failed a drill will fail the same way in production.",
	},
}

var fallback = phrasing{
	what: "raised an event this build does not have a description for",
	did:  "Nothing automatic.",
	next: "Open the console to see what the plane that raised it says about it.",
}

// Event renders one event into a message.
//
// `owner` is empty unless a passport directory was configured and had one: this
// process never invents an owner, and a mail with no owner line is better than
// a mail naming the wrong team at three in the morning.
//
// `around` is what else the notifier has seen recently. An alert about one
// agent is a fact without a situation, and the first thing the operator wants
// to know is whether this is one bad night or the first of several.
func Event(cfg Config, e event.Event, now time.Time, owner string, around []Around) Message {
	subject := rule.Subject(e)
	p, known := catalog[e.Type]
	if !known {
		p = fallback
	}

	head := fmt.Sprintf("[%s] %s %s", boxName(cfg), shortID(subject), p.what)
	if !known {
		head = fmt.Sprintf("[%s] %s: %s", boxName(cfg), shortID(subject), e.Type)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s.\n", describe(e), p.what)
	if facts := factLine(e); facts != "" {
		fmt.Fprintf(&b, "%s\n", facts)
	}
	fmt.Fprintf(&b, "\nWhat this box already did: %s\n", p.did)
	fmt.Fprintf(&b, "\nIf nobody acts: %s\n", p.next)

	if owner != "" {
		fmt.Fprintf(&b, "\nAnswerable for it: %s\n", owner)
	}

	if len(around) > 0 {
		b.WriteString("\nAround it right now:\n")
		for _, a := range around {
			fmt.Fprintf(&b, "  %-14s  %-40s  %s\n", a.Label, a.AgentID, a.What)
		}
	}

	// Three coordinates, never an action. The console is where a freeze or a
	// kill happens, after a sign-in and, for the destructive ones, a passkey.
	// A link that acted would be an unauthenticated capability held by whoever
	// forwards the message, and mail gateways prefetch links.
	if base := consoleBase(cfg); base != "" {
		b.WriteString("\nOpen in your console:\n")
		fmt.Fprintf(&b, "  what happened   %s\n", Link(cfg, e))
		if e.AgentID != "" {
			fmt.Fprintf(&b, "  this agent      %s   (freeze, kill)\n", AgentLink(cfg, e.AgentID))
		}
		if owner != "" {
			fmt.Fprintf(&b, "  its owner       %s   (everything they run)\n", OwnerLink(cfg, owner))
		}
	} else {
		b.WriteString("\nNo console address is configured for this box, so this mail carries no link.\n")
	}
	fmt.Fprintf(&b, "\nRaised by %s at %s. This mail carries identifiers and numbers only, never the content of a call.\n",
		sourceName(e), stamp(e, now))

	return Message{Subject: head, Body: b.String()}
}

// Test renders the message an installer sends while the operator is still at
// the keyboard.
//
// It is written here, as its own message, rather than by handing [Event] a
// synthetic event. That was the first version, and a live install showed what
// it produces: `--test-mail` invented an event of type `install_check`, which
// no catalog entry describes, so the very first mail a box ever sends read
// "raised an event this build does not have a description for", followed by a
// deep link to an incident that does not exist. The one message whose entire
// job is to say "this works" said something is wrong with it.
//
// It carries no link for the same reason. There is nothing to open: no event
// happened. A link to the console root would be a link the operator cannot use
// yet on a box they are still installing, and a link to a fabricated id is
// worse than none.
func Test(cfg Config, now time.Time) Message {
	body := strings.Join([]string{
		"This box can send you mail. Nothing is wrong: an installer sent this so",
		"that a mistake in the address or the mail server is found now, rather than",
		"through an alert that never arrives.",
		"",
		"What a real one looks like: what happened and its numbers, what this box",
		"already did about it, what happens if nobody acts, and one link into your",
		"own console. Never a button that acts from inside the mail.",
		"",
		"Sent by heraldyx at " + stampTime(now) + ". Nothing else in your install",
		"depends on this message arriving.",
		"",
	}, "\n")
	return Message{
		Subject: fmt.Sprintf("[%s] notifications are working", boxName(cfg)),
		Body:    body,
	}
}

// Suppression renders the one notice sent when the hourly ceiling is holding
// events back. It deliberately says nothing about the individual events: if
// the mailbox is the thing under pressure, the fix is fewer messages, not a
// summary of the flood inside one of them.
func Suppression(cfg Config, n int, now time.Time) Message {
	var b strings.Builder
	fmt.Fprintf(&b, "%d further alerts were not sent, because this box reached its hourly limit on mail.\n", n)
	b.WriteString("\nThis is a limit on messages, not on the stack: every event is still recorded, and every control still works.\n")
	if cfg.ConsoleURL != "" {
		fmt.Fprintf(&b, "\nOpen your console to see them:\n%s\n", strings.TrimRight(cfg.ConsoleURL, "/"))
	}
	return Message{
		Subject: fmt.Sprintf("[%s] %d alerts suppressed this hour", boxName(cfg), n),
		Body:    b.String(),
	}
}

// Digest renders the daily summary of everything below the immediate floor.
func Digest(cfg Config, entries []rule.DigestEntry, since time.Time, now time.Time) Message {
	var b strings.Builder
	fmt.Fprintf(&b, "Everything this box saw below its alert threshold since %s.\n\n", stampTime(since))
	for _, e := range entries {
		fmt.Fprintf(&b, "  %6d  %s\n", e.Count, e.Key)
	}
	b.WriteString("\nNone of these were judged worth waking anyone for. They are here so that\n")
	b.WriteString("a pattern nobody alerted on is still something you can see.\n")
	if cfg.ConsoleURL != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimRight(cfg.ConsoleURL, "/"))
	}
	return Message{
		Subject: fmt.Sprintf("[%s] daily summary: %d conditions below the alert line", boxName(cfg), len(entries)),
		Body:    b.String(),
	}
}

// Link returns the console deep link for an event, or "" when no console
// address is configured.
//
// The path is `/i/{type}:{subject}`, which is the money plane's own incident
// id scheme (`"{kind}:{scope}"`, see tokenfuse `crates/cloud/src/store.rs`),
// so a link built here names the same thing the console already stores. It is
// a GET at a view. There is no action in it, and there is no token in it.
func Link(cfg Config, e event.Event) string {
	base := consoleBase(cfg)
	if base == "" {
		return ""
	}
	return base + "/i/" + url.PathEscape(rule.Key(e))
}

// AgentLink opens the agent's own card, which is where freeze and kill are.
func AgentLink(cfg Config, agentID string) string {
	base := consoleBase(cfg)
	if base == "" || agentID == "" {
		return ""
	}
	return base + "/a/" + url.PathEscape(agentID)
}

// OwnerLink opens who is answerable, and what else they run.
//
// The OWNER, which agent-passport SPEC.md section 4 makes a required passport
// field, and not the `on_behalf_of` principal, which says who the agent was
// acting for at that moment. Often the same human, not always, and the two are
// a different blast radius for a stop (Yurii, 2026-08-02).
func OwnerLink(cfg Config, owner string) string {
	base := consoleBase(cfg)
	if base == "" || owner == "" {
		return ""
	}
	return base + "/o/" + url.PathEscape(owner)
}

func consoleBase(cfg Config) string { return strings.TrimRight(cfg.ConsoleURL, "/") }

// describe names the actor in the first sentence: the run when there is one,
// with its agent, else the agent alone.
func describe(e event.Event) string {
	if e.RunID != "" {
		return fmt.Sprintf("Run %s (agent %s)", shortID(e.RunID), shortID(e.AgentID))
	}
	return fmt.Sprintf("Agent %s", shortID(e.AgentID))
}

// factLine renders the allowlisted parts of `data` as one line, or "" when
// nothing survived the allowlist.
func factLine(e event.Event) string {
	if len(e.Data) == 0 {
		return ""
	}

	// Budget events get the one sentence a human actually wants, built from
	// two allowlisted numbers, instead of two raw micro-dollar integers.
	if spent, ok := micros(e.Data["spent_micros"]); ok {
		if budget, ok2 := micros(e.Data["budget_micros"]); ok2 && budget > 0 {
			pct := float64(spent) / float64(budget) * 100
			return fmt.Sprintf("Spent %s of %s (%.0f%%).", usd(spent), usd(budget), pct)
		}
	}

	keys := make([]string, 0, len(e.Data))
	for k := range e.Data {
		if dataAllowlist[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	parts := make([]string, 0, maxDataFields)
	for _, k := range keys {
		if len(parts) >= maxDataFields {
			break
		}
		if v, ok := safeValue(e.Data[k]); ok {
			parts = append(parts, k+" "+v)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.ToUpper(parts[0][:1]) + parts[0][1:] + func() string {
		if len(parts) == 1 {
			return "."
		}
		return ", " + strings.Join(parts[1:], ", ") + "."
	}()
}

// safeValue renders one `data` value, or reports that it must not be rendered.
func safeValue(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		if !safeString.MatchString(t) {
			return "", false
		}
		return t, true
	case bool:
		return fmt.Sprintf("%t", t), true
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t)), true
		}
		return fmt.Sprintf("%.2f", t), true
	case int:
		return fmt.Sprintf("%d", t), true
	case int64:
		return fmt.Sprintf("%d", t), true
	default:
		// Objects, arrays, null: never rendered. A nested object is exactly
		// where a producer would put something long and human-written.
		return "", false
	}
}

func micros(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	default:
		return 0, false
	}
}

func usd(micro int64) string {
	return fmt.Sprintf("$%.2f", float64(micro)/1_000_000)
}

// shortID keeps a long agent URI readable in a subject line without losing the
// part a human recognises.
func shortID(id string) string {
	if len(id) <= 48 {
		return id
	}
	return id[:20] + "..." + id[len(id)-24:]
}

func boxName(cfg Config) string {
	if cfg.Box == "" {
		return "agent stack"
	}
	return cfg.Box
}

func sourceName(e event.Event) string {
	if e.Source == "" {
		return "an unnamed plane"
	}
	return e.Source
}

// stamp prefers the event's own timestamp and falls back to now, so a producer
// with a broken clock cannot produce a mail with no time in it at all.
func stamp(e event.Event, now time.Time) string {
	if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
		return stampTime(t)
	}
	return stampTime(now)
}

func stampTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}
