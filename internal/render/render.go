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
//  3. An identifier a producer wrote is rendered safely, or not at all, and
//     NEVER stops the message. `agent_id`, `run_id`, `type` and `source` come
//     off the wire, and in tokenfuse `agent_id` is whatever the caller put in
//     its own `x-fuse-agent-id` header. A line break in one of them used to
//     reach the subject, where `deliver.Compose` correctly refused to build a
//     message from it, and the alert was then never sent: an agent could mute
//     itself by choosing its own name. See [safeID].
package render

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
	"github.com/TAIPANBOX/agent-stack-go/passport"
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
	// Which identity detector fired. Approved by the user 2026-08-10, and the
	// question the escalation rule asks was answered before it went in: idryx
	// writes this from a detector's own Name() method, a compile-time constant
	// in that repository, never from anything a model produced. Without it an
	// identity finding mails as "raised an event this build does not have a
	// description for", which names no fault at all.
	"detector": true,
}

// safeString is the shape a string value must have to be rendered: short, one
// line, and made of the characters identifiers and ids are made of. A prompt
// fragment or a model's answer does not survive this.
var safeString = regexp.MustCompile(`^[A-Za-z0-9_:@./\- ]{1,64}$`)

// maxDataFields caps how much of `data` reaches the mail even when every key
// is allowlisted, so a producer that adds twenty fields does not turn the
// message into a dump.
const maxDataFields = 6

// maxOwnerLength bounds a passport's owner value before it can reach a mail
// or a console link. 64 mirrors the cap this file already holds every other
// short, identifier-shaped string reaching the mail to (see safeString
// above): long enough for a team name, an email address or a Slack handle,
// short enough that "Answerable for it: <owner>" stays one line a human
// reads at three in the morning rather than a paragraph a bad file dumped
// into it, and short enough that the console link stays a link.
const maxOwnerLength = 64

// ownerShape is the character set a passport's owner value must be made of to
// be rendered. Deliberately the same identifier-like set safeString holds
// `data` values to, so this file has one definition of "safe to put in a
// mail" rather than two that can drift apart. A team name, an email address
// and a Slack handle all fit it; a newline or another control character does
// not, which is the point: the first would inject a line into a plain-text
// mail body and either would still be a value not worth linking to even
// URL-escaped.
var ownerShape = regexp.MustCompile(`^[A-Za-z0-9_:@./\- ]+$`)

// sanitizeOwner returns owner unchanged when it is short enough and shaped
// like an owner, and "" otherwise.
//
// The owner's provenance is different from `data`'s: it comes from a file the
// operator wrote or generated, not from a producer that sits next to prompts
// and model output. That makes this check a defence against an oversized or
// oddly-shaped FILE rather than an adversarial one, and correspondingly lower
// severity than the allowlist above. It is not a reason to skip it: a
// passport directory can be large, machine-generated, or synced from an
// inventory system, and a multi-line owner value reaches this file's own
// `Fprintf` call with no escaping at all.
//
// "" is not a special case: it is exactly what [Event] already does for an
// agent with no passport, so a value that fails this check falls back to the
// no-owner rendering rather than being truncated or escaped into something
// that still reads as a real owner. A mangled owner is worse than none, for
// the same reason a guessed one is.
func sanitizeOwner(owner string) string {
	if owner == "" || len(owner) > maxOwnerLength || !ownerShape.MatchString(owner) {
		return ""
	}
	return owner
}

// maxIDBytes bounds an identifier out of the envelope before it is rendered.
// 255 is agent-passport SPEC 3.1's own cap on an agent:// URI, the number
// `agent-stack-go/passport` enforces, so nothing well-formed is anywhere near
// it. The bound is not decoration: an id is rendered in the subject, in the
// first sentence and in up to two links, and until 2026-08-05 only the first
// two were bounded, which left a producer able to set the SIZE of the message
// and push it past a mail server's limit. That is the same outcome as the line
// break, reached the long way round.
const maxIDBytes = 255

// idShape is the character set an identifier must be made of to be rendered as
// it was written. Deliberately the same class as [safeString] and [ownerShape],
// so this file has one definition of "safe to put in a mail" rather than three
// that can drift, minus their 64-byte cap, which an agent URI legitimately
// exceeds (`shortID` exists for exactly that).
var idShape = regexp.MustCompile(`^[A-Za-z0-9_:@./\- ]+$`)

// unusableID stands in for an identifier that carries nothing at all. The
// envelope requires `agent_id` (agent-passport SPEC 6.1), so an event with none
// is a producer breaking the contract, and "Agent " followed by nothing reads
// like this box lost it.
const unusableID = "(no id)"

// safeID returns an identifier as it may appear in a message, and reports
// whether it was already safe to render as written.
//
// The consequence of failing is deliberately NOT the one [sanitizeOwner] has.
// An owner that fails its check is dropped, because the mail is still about
// something without it. An identifier IS what the mail is about, so dropping it
// would leave an alert naming nothing, and refusing to build the message at all
// is how this defect worked in the first place. So an unusable id is escaped
// and bounded, never dropped and never fatal: fidelity in the id is what may be
// lost here, and the alert is not.
//
// `strconv.Quote` is the escape because of the one property this needs above
// readability: its output can contain no line break, no control character and
// no invalid byte, whatever the input was, so a value that reaches
// `deliver.Compose` through this function cannot make it refuse. It also keeps
// the bytes a human can still recognise, which a placeholder would not.
//
// The agent-passport URI grammar (`^agent://[a-z0-9.-]+/[a-z0-9._/-]+$`) was
// considered as the rule here and is not it, for three reasons. It is
// agent-specific, and `run_id`, `type` and `source` reach the same subject line
// with no grammar of their own, so a grammar-shaped gate would leave the
// identical hole open one field over. It rejects ids this box renders correctly
// today, uppercase among them, and a mail that mangles a working id to satisfy
// a grammar is a worse mail. And a grammar violation is not what breaks
// anything: a line break and an unbounded length are.
func safeID(id string) (string, bool) {
	if id == "" {
		return unusableID, false
	}
	if len(id) <= maxIDBytes && idShape.MatchString(id) {
		return id, true
	}
	cut := id
	if len(cut) > maxIDBytes {
		cut = cut[:maxIDBytes]
	}
	return strconv.Quote(cut), false
}

// addressable reports whether an id can be turned into a console link.
//
// A link is a coordinate, and a mangled id is not a coordinate: `escapePath`
// would percent-encode a line break into a well-formed URL that addresses
// nothing, which is exactly the reasoning already written down for
// [OwnerLink]. The mail says the link is missing and why, rather than leaving a
// gap an operator reads as a bug in this box.
func addressable(id string) bool {
	_, ok := safeID(id)
	return ok
}

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
	// Read from idryx's own detectors rather than from its README, per this
	// repo's own catalog rule. The wording is deliberately about the GRAPH and
	// not about the agent's intent: every one of those detectors reports a
	// discrepancy between two of the operator's own records, and several of
	// them say in their own doc comments that the likeliest cause is an
	// inventory gap.
	//
	// One entry covers all of them because the bus carries one type and the
	// detector name travels in `data.detector`, which is the decision recorded
	// in agent-passport SPEC 6.2. The detector is now allowlisted, so the mail
	// names which one fired.
	"identity_finding": {
		what: "matched an identity rule",
		did:  "Nothing. The identity plane reads; it never changes a directory, a permission or an agent.",
		next: "Nothing automatic. The finding stays in the identity graph until somebody looks at it.",
	},
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
	// ------------------------------------------- the box's own dependencies
	//
	// Every other entry in this catalog is about the agent: something it did,
	// or something this stack refused it. This one is about the BOX. A
	// dependency it needs died underneath a run that was behaving perfectly,
	// and the subject of the mail is a run that did nothing wrong.
	//
	// It exists because the failure had no event at all. `@yurii 2026-08-25`:
	// "коли лягає апстрім, шлюз чисто вертає 502, і жоден план цього не
	// записує; у конверті подій немає типу для того, що зламалась власна
	// залежність коробки".
	//
	// The sentences below are the NEUTRAL ones, for an event that does not say
	// which of three very different things happened. The type alone does not
	// carry enough to say anything more: it covers a call that never happened,
	// a call that went through with nothing governing it, and a call refused by
	// nobody's decision, and those want opposite moves from an operator.
	// [qualify] is what says which, off `data.effect`.
	//
	// They are written out in full here rather than left empty for qualify to
	// fill, because an entry that is only correct when another function also
	// runs is one that renders "What this box already did:" followed by nothing
	// the day somebody reorders two lines in [Event].
	"dependency_failed": {
		what: "was affected by a failure in one of this box's own dependencies",
		did:  "The failure is in something this box depends on, underneath a call this run made. Whether the call still went through, and what it cost, is what an operator needs next, and this event does not say.",
		next: "Nothing automatic. Open the console at this incident to see what the gateway returned for the call: that answer is what decides whether this cost anything and whether any policy saw it.",
	},

	// ---------------------------------------------------------- costcrew
	//
	// The FinOps console. Read against its own code on 2026-08-23, which is
	// what this file requires of a catalog entry, and the one fact that shapes
	// every sentence below is that COSTCREW ENFORCES NOTHING. It records. It
	// has no gateway, no interceptor and no refusal path: `internal/enforce` is
	// a separate binary the console never calls, and everything here is a
	// statement about the console's own record, never about traffic.
	//
	// So no `did` says anything was stopped, and no `next` promises anything
	// will be. The one exception is a suspension, which does stop that agent
	// being given further work by this console, and says so in those words.
	"anomaly_triaged": {
		what: "was given a spend anomaly to investigate",
		did:  "The console assigned it. The money was already spent: an anomaly is found in the bill, after the fact.",
		next: "Nothing expires it. The finding stays open until somebody explains, accepts or dismisses it.",
	},
	"anomaly_explained": {
		what: "wrote up what caused a spend anomaly",
		did:  "The explanation was recorded against the finding. Nothing was refunded and nothing was stopped.",
		next: "The finding stays open until a person accepts or dismisses the explanation.",
	},
	"anomaly_accepted": {
		what: "had its explanation of a spend anomaly accepted",
		did:  "A person closed the finding. The spend stands as explained.",
		next: "Nothing further. The finding is closed and stays in the record.",
	},
	"anomaly_dismissed": {
		what: "had a spend anomaly dismissed",
		did:  "A person closed the finding without accepting the explanation. The spend stands either way.",
		next: "Nothing further. Dismissal closes the finding; it does not undo the charge.",
	},
	// The five that change the roster. An operator reads these to answer "who
	// changed what and does it still add up", so each says who acted where the
	// event carries it.
	"agent_hired": {
		what: "was added to the roster",
		did:  "The console recorded the hire, its guards and its rights. Nothing was provisioned anywhere else.",
		next: "It will be given work on the next sprint plan. Its identity is a name this console chose unless something attested it.",
	},
	"agent_removed": {
		what: "was taken off the roster",
		did:  "The console removed it and no longer publishes its passport. What it did stays on the board and in the journal.",
		next: "Nothing further. Removal does not touch anything it already spent or produced.",
	},
	"agent_transferred": {
		what: "changed hands",
		did:  "The console moved the agent and the work still open on it to the new owner. Work already closed stays charged to whoever authorised it.",
		next: "From here the new owner answers for what it spends. Nothing about the agent's behaviour changed.",
	},
	"agent_rebriefed": {
		what: "had its brief rewritten",
		did:  "The console recorded a new mission, rights or budget guard for it. Only its owner or an admin can do this.",
		next: "The new guard applies from now. A guard is a record, not a limit: this console does not refuse anything when one is passed.",
	},
	"agent_state_changed": {
		what: "was moved on or off the rota",
		did:  "The console recorded the new state with a reason. A suspended agent is given no further work by this console.",
		next: "It stays in that state until somebody changes it back. Suspension is a pause and undoes nothing already done.",
	},
	// The three an operator reads as governance rather than as an incident.
	"budgets_set": {
		what: "had team budgets written against it",
		did:  "The console recorded the budgets. Nothing was pushed to any gateway and no spend was capped.",
		next: "Variance is measured against these from now. Nothing enforces them.",
	},
	"forecast_frozen": {
		what: "had a forecast frozen for the period",
		did:  "The console pinned the forecast so later accuracy is measured against what was actually claimed at the time.",
		next: "Nothing further. The freeze is a record, and the period's accuracy is scored against it when it closes.",
	},
	"sprint_planned": {
		what: "had a sprint approved",
		did:  "The console created the tasks and assigned them. Each carries the guard its analyst was hired with.",
		next: "The work starts against those guards. Passing one is recorded, never refused.",
	},
	"explainer_published": {
		what: "published a written explanation to a team",
		did:  "A person stamped it. Only a stamp publishes: nothing an agent writes leaves the console unreviewed.",
		next: "Nothing further. A stamp is not taken back.",
	},
	"sim_finding": {
		what: "failed a rehearsal",
		// "Production was not touched" was a claim about the operator's setup
		// that this event does not carry: the drill runs against whichever
		// gateway it was pointed at, normally a pre-production one, and
		// nothing here can tell.
		did:  "The drill recorded a guardrail that did not hold, against the gateway it was pointed at.",
		next: "A guardrail that failed a drill will fail the same way in production.",
	},
	// ---------------------------------------- an error budget nobody enforces
	//
	// The quality plane's error budget, and the entry where the honest `did`
	// line is the uncomfortable one. Every other type in this catalog reports
	// something that HAPPENED to the agent: a call refused, a budget cut off, a
	// hold placed. This one reports a MEASUREMENT. verdryx counted the eligible
	// runs in a window, compared the ratio of good ones to the objective, and
	// wrote down how much of the error budget is left. Nothing else occurred.
	//
	// That is not a gap somebody forgot to fill, and the sentences below must
	// not read as though it were. Read against the other planes on 2026-08-26:
	//
	//   - wardryx's decision-path packages, `internal/pdp` and
	//     `internal/policy`, directly import no clock, no randomness, no
	//     network and no database, which `scripts/decision-path-purity.sh` in
	//     that repository enforces, so a number computed somewhere else is not
	//     one they can consult. Take the limit of that as wardryx's own
	//     CLAUDE.md states it: the gate checks DIRECT imports, there is a
	//     transitive path to a database through the approval branch, and the
	//     honest claim is about the decision code's own determinism rather than
	//     about every decision ever made.
	//   - verdryx writes no policy. Grepped there the same day: not one of the
	//     four indicators this type names appears in that repository's code
	//     yet, let alone anything that acts on them.
	//   - and there is no autonomy tier anywhere in this stack to lower.
	//
	// An entry implying the agent had been restrained would be the 2026-08-03
	// defect again, where four entries described a consequence the producing
	// plane does not have, and it would be worse than any of those four: an
	// operator told the estate has already reined an agent in has been given a
	// reason not to do the thing this mail exists to prompt.
	//
	// It arrives on two triggers only, "exhausted" and "fast_burn", and
	// [sloBurn] is what tells them apart. A SLOW burn is computed and never
	// reaches this bus. Severity in this estate is fixed per type rather than
	// chosen at the emission site, so one type is one paging band, and "your
	// budget will be gone by Friday" does not belong in the band of "your budget
	// is gone": the slow figure lives in verdryx's report and its JSON output,
	// where a dashboard reads it, and it is deliberately not an alert. That is
	// the generalisation of tokenfuse's own lesson from 2026-08-03, when
	// `breaker_tripped` was lowered from critical, because a type that pages for
	// the design working teaches an operator to filter the sender.
	//
	// The sentences here are the NEUTRAL ones, for an event that does not say
	// which trigger fired. They are written out in full rather than left for
	// [sloBurn] to fill, for the reason `dependency_failed` above gives: an
	// entry that is only correct when another function also runs is one that
	// renders "What this box already did:" followed by nothing the day somebody
	// reorders two lines in [Event].
	"slo_burn": {
		what: "has an error budget this box was told about",
		did:  "Nothing. The quality plane measured the objective over its window and worked out how much of the error budget is left. That measurement is the whole of what happened: no traffic was stopped, no policy was written or changed, and nothing about this agent was lowered, because there is no control in this stack that a budget feeds into. What to do about it is a decision nobody has made yet.",
		next: "Nothing automatic. This event does not say whether the budget is already gone or is only being spent too fast, and those are a statement about the past and a forecast, so open the console at this incident rather than reading one of them into it.",
	},
}

// qualify adjusts a phrasing where the EVENT carries something that changes
// what it means, rather than letting the type alone decide.
//
// Two types need it today, and they need it for the same reason: one type over
// several outcomes an operator must not confuse, with the catalog keyed on type
// alone, so without this the mail would have to pick one of them and be wrong
// about the rest.
//
// `dependency_failed` covers three, and the worst of those confusions is the
// middle one: told "a call failed" when the truth is "the call went through and
// no policy examined it", an operator goes looking for a broken agent and never
// learns their estate spent an interval ungoverned. `slo_burn` covers two, an
// objective already missed against one being missed faster than its window
// allows, which are a statement about the past and a forecast: they want
// different moves and only one of them still has time in it.
//
// It reads `data` and renders NONE of it. Every sentence below is this file's
// own prose, selected by matching a closed set of values, so nothing a producer
// wrote reaches a mailbox and `dataAllowlist` is untouched. That is not a
// stylistic preference: `data.detail` on `dependency_failed` is a transport
// error string, which is exactly the "text somebody else wrote" the allowlist
// exists to keep out, and adding any key to that list is a decision CLAUDE.md
// sends to the user rather than a thing a change like this may do on its way
// past. The same answer covers `slo_burn`'s eleven fields, nine of which are
// numbers an operator would quite like to see, which is what makes that type
// the tempting one.
func qualify(e event.Event, p phrasing) phrasing {
	switch e.Type {
	case "dependency_failed":
		return dependencyFailed(e, p)
	case "slo_burn":
		return sloBurn(e, p)
	case "budget_threshold":
		return budgetThreshold(e, p)
	default:
		return p
	}
}

// dependencyFailed says which of three outcomes a failed dependency produced,
// off `data.effect`.
func dependencyFailed(e event.Event, p phrasing) phrasing {
	dep := dependencyName(e)

	// The three the contract names, plus the honest neutral for anything else.
	// A `default` that guessed at one of the three would be the fallback
	// problem in miniature: a confident sentence about somebody else's system,
	// with nothing behind it.
	switch effect, _ := e.Data["effect"].(string); effect {
	case "allowed_ungoverned":
		p.what = "was let through with no policy applied to it"
		p.did = "It let the call through: " + dep + " could not be reached, this gateway is configured to fail open, and it synthesized an allow in place of a decision. Nothing examined this call, and no policy of yours said yes to it."
		p.next = "While that plane stays unreachable every further call is let through the same way, and nothing goes back afterwards to mark which ones they were. A gateway failing open reports exactly what a governed one reports, which is why this arrives as mail rather than as a number on a dashboard."
	case "denied_unasked":
		p.what = "was refused because no policy plane could be asked about it"
		p.did = "It refused the call: " + dep + " could not be reached, this gateway is configured to fail closed, and it synthesized a deny in place of a decision. No policy refused this call. Nothing examined it, and the same call may well be allowed the moment that plane answers again."
		p.next = "Every call that needs a decision is refused the same way until it answers. This is an outage in the box's own dependency and not an agent doing something it should not, so freezing the agent is the wrong move here."
	case "call_failed":
		p = callFailed(e, p, dep)
	default:
		p.did = "The failure is in " + dep + ", underneath a call this run made. Whether the call still went through, and what it cost, is what an operator needs next, and this event does not say."
	}
	return p
}

// callFailed splits one effect on the STAGE it happened at, because the money
// answer is opposite at two of them and the money answer is what an operator
// reads first.
//
// At the buffered stages the gateway settles `Microusd::ZERO` against both the
// run's budget and the unit ledger, under its own comment "Failed call cost us
// nothing" (tokenfuse `crates/gateway/src/proxy.rs`, read 2026-08-25). So the
// outage genuinely cost nothing and the mail may say so.
//
// Mid-stream it is the reverse, and saying "nothing was charged" there would be
// the same class of falsehood as the four this catalog was audited for on
// 2026-08-03. The response has already gone out with its own status and part of
// the answer has already reached the agent; `SettleGuard`'s `Drop` then settles
// whatever usage was parsed, and a stream that started 2xx and reported no
// usage settles the RESERVED ESTIMATE rather than zero. The agent is also left
// holding a truncated answer instead of an error, which is the part it is least
// likely to notice.
//
// The stage is read as one value against a constant, not parsed: a stage this
// build does not know takes the buffered wording only if it is not the streamed
// one, which is the safe direction, since the streamed sentence claims a
// delivery that a non-streamed failure did not make.
func callFailed(e event.Event, p phrasing, dep string) phrasing {
	if stage, _ := e.Data["stage"].(string); stage == "stream" {
		p.what = "had its answer cut off part way through"
		p.did = "Part of the answer had already reached the agent when " + dep + " broke, so this is not a call that never happened, and it was not free either: what the provider reported as used is charged to the run, and a stream that started successfully and then reported no usage at all is charged the estimate that was reserved for it."
		p.next = "Nothing automatic. The agent is holding a truncated answer rather than an error, which is the part worth looking at: a run that reads it as a whole one carries on from half a result."
		return p
	}
	p.what = "could not be served, because a dependency of this box failed"
	p.did = "The call did not complete: " + dep + " could not be reached or did not finish, so the agent was given an error from this gateway rather than an answer. Nothing was charged for it, and the money reserved against the run was released in full."
	p.next = "Nothing automatic. The agent has an error rather than an answer, and whether it retries or stops is up to the agent."
	return p
}

// dependencyName is which of the box's own dependencies died, in this file's
// words rather than the producer's.
//
// An operator's first question on this type is who to call, and provider and
// policy plane are two different people. The value is matched against a closed
// set and a NAME OF OUR OWN is returned, which is why `dependency` is not in
// `dataAllowlist`: nothing a producer wrote is rendered, so there is no value
// here to shape-check, cap or escape.
//
// A dependency this build has not heard of is described as one rather than
// named. The producer may grow a third before this file learns of it, and a
// guessed name is worse than a general one, while dropping the sentence
// altogether would leave the operator with no idea what broke.
func dependencyName(e event.Event) string {
	switch d, _ := e.Data["dependency"].(string); d {
	case "provider":
		return "the provider"
	case "policy_plane":
		return "the policy plane"
	default:
		return "a dependency of this box"
	}
}

// sloBurn says WHICH objective and WHICH of the two triggers, because those are
// the two questions an operator asks on this type: which one, and is it already
// missed or about to be.
//
// The `did` line is deliberately untouched by either branch. The trigger changes
// what is TRUE about the budget and changes nothing about what was done, which
// on this type is nothing at all, and a `did` that varied with the trigger would
// be this file inventing a consequence for the louder of the two.
//
// Neither answer is the producer's bytes. The objective's name comes from
// [objectiveName], a closed set matched to this file's own words in the same way
// [dependencyName] works, and the trigger picks between two sentences written
// here. So `sli` and `trigger` are controls for the phrasing and not values for
// the operator to read, and neither belongs in `dataAllowlist`: there is nothing
// here to shape-check, cap or escape.
//
// A trigger this build does not know keeps the neutral sentences rather than
// picking one of the two, for the reason [dependencyFailed]'s default gives.
// "Already missed" and "on course to be missed" are a fact and a forecast, and
// guessing which a future producer meant is the fallback problem in miniature.
// A SLOW burn lands there too, and that is correct rather than a gap: the
// contract emits this type on "exhausted" and "fast_burn" only, so an event
// carrying a slow burn is a producer that has broken it, and this build must not
// grow a confident description for a case it has agreed never to be paged about.
func sloBurn(e event.Event, p phrasing) phrasing {
	obj := objectiveName(e)

	switch trigger, _ := e.Data["trigger"].(string); trigger {
	case "exhausted":
		p.what = "has spent its whole error budget on " + obj
		p.next = "Nothing automatic, here or anywhere else. This is a statement about runs that have already happened, so it does not improve on its own: the objective stays missed until enough good runs push the bad ones out of the window it was measured over. What to do about that is a decision for a person, and the console at this incident is where the runs behind the number are."
	case "fast_burn":
		p.what = "is spending its error budget on " + obj + " far faster than the window allows"
		p.next = "Nothing automatic. The budget is not gone yet and at this rate it will be, well before the window ends, which is the part of this there is still time to act on. The runs spending it are running now, so the console at this incident is where to see which of them are failing and whether this is one bad change or something spread across the fleet."
	default:
		p.what = "raised an error budget signal about " + obj
	}
	return p
}

// objectiveName is which service level objective the budget belongs to, in this
// file's words rather than the producer's.
//
// "Which objective" is the first question this type raises, and the four are
// read by different people: a containment budget and a cost budget say very
// different things about the same agent on the same day. The value is matched
// against a closed set and a NAME OF OUR OWN is returned, which is why `sli` is
// not in `dataAllowlist`, exactly as [dependencyName] is the reason `dependency`
// is not. One mechanism for turning a control value into prose, not two.
//
// The names are the indicator said in English and nothing more. Each one could
// carry its formula ("the share of runs that finished the job"), and that would
// be this file making a claim about verdryx's arithmetic: grepped there on
// 2026-08-26, none of the four indicators exists in that repository's code yet,
// so a definition written here would be a guess dressed as a description, which
// is what this catalog's own rule forbids. What the ratio counts is verdryx's to
// state, in its report and in the console; what the mail owes the reader is
// which of the four this one is.
//
// An objective this build has not heard of is described as one rather than
// named, for [dependencyName]'s reason: a guessed name is worse than a general
// one, and dropping the phrase would leave the operator holding a budget with no
// idea what it was a budget for.
func objectiveName(e event.Event) string {
	switch sli, _ := e.Data["sli"].(string); sli {
	case "task_success":
		return "task success"
	case "quality_floor":
		return "the quality floor"
	case "containment":
		return "containment"
	case "cost_discipline":
		return "cost discipline"
	default:
		return "an objective this build does not have a name for"
	}
}

// qualify adjusts a phrasing where the EVENT carries something that changes
// what it means, rather than letting the type alone decide.
//
// One case today, and it was a live falsehood reaching a mailbox.
// budget_threshold's sentence promises "further calls are refused with a hard
// 402", which is true of the gateway that raises it and false of costcrew,
// which maps its own guard_passed onto the same shared word. That console has
// no refusal path at all: it records that an agent went past its guard and
// does not stop it, and it stamps `enforced: false` on the event to say so.
// The field exists precisely so a reader does not have to know which producer
// sent it.
//
// This reads the field and never renders it: what may reach a mailbox is
// governed by dataAllowlist, and nothing here adds to that.
func budgetThreshold(e event.Event, p phrasing) phrasing {
	enforced, present := e.Data["enforced"].(bool)
	if !present || enforced {
		return p
	}
	p.did = "Nothing. The plane that raised this records budgets and does not enforce them."
	p.next = "Nothing is refused when the budget is gone. This is a record that a guard was passed, not a limit that will bite."
	return p
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
// a mail naming the wrong team at three in the morning. An owner that is too
// long or shaped wrong (see [sanitizeOwner]) is treated exactly the same way:
// dropped, never truncated or escaped into something that still reads like a
// real one.
//
// `around` is what else the notifier has seen recently. An alert about one
// agent is a fact without a situation, and the first thing the operator wants
// to know is whether this is one bad night or the first of several.
func Event(cfg Config, e event.Event, now time.Time, owner string, around []Around) Message {
	owner = sanitizeOwner(owner)
	subject := rule.Subject(e)
	p, known := catalog[e.Type]
	if !known {
		p = fallback
	}
	// Before `head`, deliberately: [qualify] may change `what`, and on this
	// type it is the SUBJECT LINE that has to be right first. A mailbox is read
	// as a list of subjects before any one of them is opened, and "was let
	// through with no policy applied to it" and "could not be served" are read
	// differently at three in the morning.
	p = qualify(e, p)

	head := fmt.Sprintf("[%s] %s %s", boxName(cfg), shortID(subject), p.what)
	if !known {
		// The TYPE is producer-written too, and this is the one branch that
		// puts it in the subject line, so it needs the same treatment as the
		// ids beside it. A known type never reaches a header: what is rendered
		// then is this file's own sentence for it.
		head = fmt.Sprintf("[%s] %s: %s", boxName(cfg), shortID(subject), shortID(e.Type))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s.\n", describe(e), p.what)
	if facts := factLine(e); facts != "" {
		fmt.Fprintf(&b, "%s\n", facts)
	}
	if !identifiersAreSafe(e) {
		b.WriteString(unusableIDNote)
	}
	fmt.Fprintf(&b, "\nWhat this box already did: %s\n", p.did)
	fmt.Fprintf(&b, "\nIf nobody acts: %s\n", p.next)

	if owner != "" {
		fmt.Fprintf(&b, "\nAnswerable for it: %s\n", owner)
	}

	if len(around) > 0 {
		b.WriteString("\nAround it right now:\n")
		for _, a := range around {
			// The agent id of a context row comes off the same log as the one
			// the alert is about, so it gets the same treatment. `Label` and
			// `What` do not: `internal/fleet` builds both from its own
			// constants and from allowlisted numbers, never from anything a
			// producer wrote, which its package doc states as its own rule.
			agentID, _ := safeID(a.AgentID)
			fmt.Fprintf(&b, "  %-14s  %-40s  %s\n", a.Label, agentID, a.What)
		}
	}

	// Three coordinates, never an action. The console is where a freeze or a
	// kill happens, after a sign-in and, for the destructive ones, a passkey.
	// A link that acted would be an unauthenticated capability held by whoever
	// forwards the message, and mail gateways prefetch links.
	if base := consoleBase(cfg); base != "" {
		incident, agent := Link(cfg, e), AgentLink(cfg, e.AgentID)
		// A CLAIMED subject gets no agent coordinate at all.
		//
		// The link would be well formed and would address `claimed:agent://...`,
		// which is NOT the established agent's card, so nothing here was ever
		// going to send a woken operator to the wrong agent's kill button: the
		// marker is part of the subject and the console addresses what it is
		// given. What it WOULD do is offer "(freeze, kill)" beside a card that
		// does not exist, and a coordinate that names an action nobody can take
		// is worse than no coordinate, because it is read at three in the
		// morning by somebody deciding whether to click.
		//
		// There is nothing to freeze under that name yet. Whoever the process
		// is, the operator's next move is to find out, which is what the
		// incident link above is for.
		claimedSubject := passport.IsClaimedSubject(e.AgentID)
		if claimedSubject {
			agent = ""
		}
		ownerLink := ""
		if owner != "" {
			ownerLink = OwnerLink(cfg, owner)
		}
		if incident == "" && agent == "" && ownerLink == "" {
			// A console is configured and nothing in this event can be
			// addressed in it. Saying so is not the same sentence as "no
			// console is configured", and telling an operator the wrong one of
			// those two sends them to change a setting that is already right.
			b.WriteString("\nNothing here can be opened in your console: the identifiers this event carries are not ones a link can be built from.\n")
		} else {
			b.WriteString("\nOpen in your console:\n")
			if incident != "" {
				fmt.Fprintf(&b, "  what happened   %s\n", incident)
			}
			if agent != "" {
				fmt.Fprintf(&b, "  this agent      %s   (freeze, kill)\n", agent)
			}
			if claimedSubject {
				b.WriteString("  this agent      no card: the identity here is one the process asserted about itself, not one your estate issued\n")
			}
			if ownerLink != "" {
				fmt.Fprintf(&b, "  its owner       %s   (everything they run)\n", ownerLink)
			}
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
		// Every row is `type:subject`, both halves off the wire, so a digest
		// row can inject a line into this body exactly as an alert's subject
		// could inject a header. Same check, same reason.
		key, _ := safeID(e.Key)
		fmt.Fprintf(&b, "  %6d  %s\n", e.Count, key)
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
	key := rule.Key(e)
	if base == "" || !addressable(key) {
		return ""
	}
	return base + "/i/" + escapePath(key)
}

// escapePath escapes an id for a URL path WITHOUT escaping the separators it is
// already made of.
//
// `url.PathEscape` turns every `/` into `%2F`, so
// `agent://meridian.io/finops/unit-economics-analyst` reached a mailbox as
// `agent:%2F%2Fmeridian.io%2Ffinops%2Funit-economics-analyst`: eight characters
// longer than the id and, at three in the morning, unreadable. A mail is
// text/plain, so the address IS the link text. There is no shorter label to
// show instead, and no styling to lean on: what is in the URL is what the
// operator reads.
//
// A path is a sequence of segments separated by slashes, so the slashes belong
// there. Each segment is escaped on its own and they are rejoined, which leaves
// an agent id looking like itself and still escapes anything inside a segment
// that would change the shape of the URL. The console reads everything after
// its prefix, so more segments cost it nothing.
//
// This drops sixteen characters and every `%2F`. Two things it deliberately
// does NOT do: strip the `agent://` scheme, and strip the organisation. Both
// are shorter still, and both would need the console to GUESS which of several
// id shapes it was handed. An earlier version of this did strip the scheme, and
// the guess it forced on the other side turned `run/42 a` into an agent id in
// the console's own tests.
//
// Applied to what goes in the URL only. `rule.Key` is the dedup key and the
// journal's subject, and neither may change shape.
func escapePath(id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// AgentLink opens the agent's own card, which is where freeze and kill are.
//
// It refuses an id that is not [addressable] for the same reason [OwnerLink]
// refuses an owner: percent-encoding a line break produces a well-formed URL
// pointing at nothing. Refusing the link never costs the message, which is
// built and sent either way.
func AgentLink(cfg Config, agentID string) string {
	base := consoleBase(cfg)
	if base == "" || !addressable(agentID) {
		return ""
	}
	return base + "/a/" + escapePath(agentID)
}

// OwnerLink opens who is answerable, and what else they run.
//
// The OWNER, which agent-passport SPEC.md section 4 makes a required passport
// field, and not the `on_behalf_of` principal, which says who the agent was
// acting for at that moment. Often the same human, not always, and the two are
// a different blast radius for a stop (Yurii, 2026-08-02).
//
// [sanitizeOwner] runs here too, not only in [Event]: this function is
// exported and callable on its own, so it has to be safe to call directly
// rather than relying on its one current caller having already checked.
// escapePath alone is not enough, because it makes the URL well-formed, not
// the value worth linking to: escapePath percent-encodes a control character
// rather than refusing it, so an unsanitized owner reached this link exactly
// as unsafely as it reached the body, only URL-encoded.
func OwnerLink(cfg Config, owner string) string {
	base := consoleBase(cfg)
	owner = sanitizeOwner(owner)
	if base == "" || owner == "" {
		return ""
	}
	return base + "/o/" + escapePath(owner)
}

func consoleBase(cfg Config) string { return strings.TrimRight(cfg.ConsoleURL, "/") }

// describe names the actor in the first sentence: the run when there is one,
// with its agent, else the agent alone.
// describe names the subject in the sentence that opens the mail.
//
// A CLAIMED subject gets a different sentence, and this is the only place that
// distinction can be made once for every event type. agent-passport SPEC 3.3
// says an identity read out of a process's own AGENT_PASSPORT_ID is a
// self-declaration and that an observer reporting it must make the distinction
// visible in what it reports. "Agent X did something" would be this process
// making exactly the claim the spec forbids, in the one place a human reads at
// three in the morning.
//
// Both readings stay open in the wording, because both are worth acting on: an
// agent may be doing this, or something may be using its name.
func describe(e event.Event) string {
	if passport.IsClaimedSubject(e.AgentID) {
		inner, _ := passport.ClaimedInner(e.AgentID)
		if e.RunID != "" {
			return fmt.Sprintf("Run %s (a process claiming to be agent %s)", shortID(e.RunID), shortID(inner))
		}
		return fmt.Sprintf("A process claiming to be agent %s", shortID(inner))
	}
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

// shortID renders one identifier for a human: safe first, then short. Every
// path in this file that puts an id into a sentence goes through here, so no
// caller can forget the first half.
func shortID(id string) string {
	text, _ := safeID(id)
	return shorten(text)
}

// shorten keeps a long agent URI readable in a subject line without losing the
// part a human recognises.
func shorten(id string) string {
	if len(id) <= 48 {
		return id
	}
	return id[:20] + "..." + id[len(id)-24:]
}

// identifiersAreSafe reports whether every identifier this file will render out
// of e was written in a shape it can render as-is.
func identifiersAreSafe(e event.Event) bool {
	for _, id := range []string{e.Type, e.AgentID, e.Source} {
		if _, ok := safeID(id); !ok {
			return false
		}
	}
	// The run id is optional, and an event about an agent rather than a run has
	// none. Absent is not unusable.
	if e.RunID != "" {
		if _, ok := safeID(e.RunID); !ok {
			return false
		}
	}
	return true
}

// unusableIDNote is what the mail says about its own rendering when an
// identifier had to be escaped. It is said out loud rather than left to be
// noticed, because the operator is looking at a mangled id and the two
// explanations they would reach for, this box is broken or somebody renamed an
// agent, are both wrong and both send them somewhere useless.
const unusableIDNote = "\nAn identifier in this event is not the shape this box can render as written: " +
	"identifier-like characters only, at most 255 bytes, the cap agent-passport SPEC 3.1 puts on an agent:// URI. " +
	"It is shown above escaped, and no console link is built from it, because a mangled id addresses nothing. " +
	"The alert was sent anyway: an id nobody can parse is a reason to look, not a reason to say nothing.\n"

func boxName(cfg Config) string {
	if cfg.Box == "" {
		return "agent stack"
	}
	return cfg.Box
}

// sourceName names the plane that raised the event. Producer-written like the
// ids, and rendered into the body rather than a header, so it cannot break a
// message and it CAN add lines: an "Open in your console" block of its own,
// with an address of its own, under a sentence the operator trusts. It goes
// through the same check for that reason.
func sourceName(e event.Event) string {
	if e.Source == "" {
		return "an unnamed plane"
	}
	name, _ := safeID(e.Source)
	return name
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
