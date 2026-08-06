# What has actually been verified

This file records what was checked, how, and when. It is not a status page and
it is not a promise: an entry here means a command was run and produced the
stated result.

## 2026-08-02, v0.1

**Every gate, locally.**

```
gofmt -l .                  clean
go vet ./...                clean
go test -race ./...         all packages ok
./scripts/one-way-out.sh    OK
```

**The tests were verified by breaking the code**, not by watching them pass.
This repo's code is new, so there is no "before the fix" to run them against;
the equivalent is to break each invariant deliberately and confirm the test
notices. Five breaks, five catches:

| what was broken | test that caught it |
|---|---|
| the `data` allowlist bypassed in `internal/render` | `TestNoContentFromDataReachesTheMail` |
| `?action=kill` appended to the console link | `TestTheOnlyLinkIsAConsoleView` |
| the dedup check disabled in `rule.Decide` | `TestDedupHolds` |
| the hourly ceiling disabled in `rule.Decide` | `TestCeilingHolds` |
| header sanitising removed from `deliver.Compose` | `TestHeaderInjectionIsRefused` |

**The gate was verified by breaking the code too.** Adding an `os` import to
`internal/rule` fails `scripts/one-way-out.sh` with the exact reason, rather
than passing quietly.

**One test found a real defect in another test.** The first version of
`TestTheOnlyLinkIsAConsoleView` blacklisted action verbs in the URL and failed
on a perfectly correct link, because the event TYPE `run_killed` contains the
word "kill". A blacklist of scary words is not the property that matters; the
test now asserts the whole URL and the absence of any query string. Worth
recording because the same mistake in the renderer rather than the test would
have been a silent hole.

## 2026-08-02, the dispatch journal

**Every message sent leaves one chained agent-event behind it**, and the chain
was checked by a tool from another repository rather than only by this one's
own tests:

```
$ agent-conform -chain sent.ndjson
OK   sent.ndjson:1 (event v0.2)
PASS sent.ndjson (chain: 0 chained, 1 head(s))
```

That output is from a real run of `make demo`, whose record reads:

```json
{"schema":"taipanbox.dev/agent-event/v0.2","ts":"2026-08-02T17:59:42Z",
 "source":"heraldyx","type":"alert_sent","agent_id":"agent://acme/biller",
 "severity":"info","run_id":"run-42","data":{"about":"budget_threshold:run-42",
 "kind":"alert","outcome":"accepted","to":["you@example.com"],"transport":"file"}}
```

**Two defects were found by writing the tests, not by reading the code.**

The first is the one worth keeping. Delivery and recording were two statements
next to each other, so on a box with **no address configured** nothing was sent
and a record was still written saying a message had been accepted. An audit
trail claiming a notification nobody received is worse than no trail, and it
would have been discovered by whoever was relying on it. Both halves now sit
behind one condition, and `TestNoRecipientsMeansNoRecord` holds it.

The second: the journal opened before anything had created its directory, so on
a path one level below the mount point it failed, logged, and left the box
sending mail it never recorded. `TestTheJournalDefaultsToBesideTheState` found
it; `record.Open` now creates the directory the way `state.Save` already did.

Gates after the change: `gofmt`, `go vet`, `staticcheck`, `go test -race ./...`,
`gosec`, `govulncheck`, `./scripts/one-way-out.sh`, all clean. The architecture
gate still passes with `internal/record` doing file I/O, because the rule it
enforces is about the decision layer and about who may speak HTTP, and neither
changed.

## 2026-08-02, the SMTP client, against a server that answers

The gap that had been first on the list below since this repository existed is
closed, and it needed no cloud and no credential: a 60-line SMTP server on
loopback, and heraldyx pointed at it with `HERALDYX_SMTP_HOST=127.0.0.1:2525`.

The session, as the server saw it:

```
S: 220 sink.example.com ESMTP ready
C: EHLO localhost
S: 250-sink.example.com
S: 250 SIZE 10485760
C: MAIL FROM:<box@example.com>
C: RCPT TO:<ops@example.com>
C: DATA
S: 354 End data with <CR><LF>.<CR><LF>
S: 250 2.0.0 Ok: queued as SINK-0001
C: QUIT
```

and the message it received, in full, headers first:

```
From: box@example.com
To: ops@example.com
Subject: [aws-test] run-77 is approaching its budget
Date: Sun, 02 Aug 2026 19:20:05 +0100
MIME-Version: 1.0
Content-Type: text/plain; charset=utf-8
Auto-Submitted: auto-generated

Run run-77 (agent agent://acme.example/biller) is approaching its budget.
Spent $1.70 of $2.00 (85%).
...
Open it in your console:
https://box.example.com/i/budget_threshold:run-77
```

So the protocol, the envelope commands, the RFC 5322 headers, the CRLF body,
the rendered numbers and the deep link are all now observed rather than
asserted. The journal recorded the same send as `transport: smtp`,
`outcome: accepted`.

What this does NOT establish is the last mile, which is why the list below
still has an entry about mail.

## 2026-08-02, the last mile

A message sent from the notifier running on a five-node Kubernetes cluster on
AWS, through the egress NetworkPolicy, through Gmail's submission service on
587, arrived in a real human mailbox and was read there. Yurii pasted it back
verbatim.

That is the whole chain end to end on real infrastructure: an event in the
shared log, the rules, the renderer, the SMTP client, a real provider, an inbox.
Nothing in it was a fixture.

Worth stating precisely, because "mail works" is the kind of sentence that grows
in the retelling: what this establishes is that ONE message, from THIS cluster,
through THAT provider, reached ONE inbox on 2026-08-02. It says nothing about
volume, about deliverability from a different address, or about what a corporate
spam filter does with the hundredth one.

## 2026-08-02, on a live Kubernetes cluster

A five-node cluster on AWS, plus a three-node cluster on GCP, plus a single box
and an external machine acting as an SMTP receiver. All torn down.

**What it established, after it found three things first.**

The headline is the last one: **three real alerts, raised by the money plane's
own detectors, rendered here, accepted by a real mail server, with the dispatch
journal chain verifying afterwards.** That is this process doing its whole job
on real infrastructure rather than on a fixture.

Getting there needed three defects fixed, and each one is worth keeping because
none was findable by reading code.

**The notifications plane had no input at all.** The cloud plane, where the
detectors live, ran with its event exporter off and wrote incidents nowhere:
they reached the console stream and the plane's own API and stopped. So heraldyx
read an empty directory and said nothing, correctly and uselessly. It was
invisible because the log file **existed**, with the right owner and zero bytes,
created by the gateway at startup. An empty journal looks exactly like a calm
fleet. Fixed in the money plane, which now writes its own file rather than
appending to the gateway's.

**The state volume was unwritable from the first day**: `root:root 0755` against
a process running as uid 65532. Nothing surfaced it while there was nothing to
write; the first real event turned it into `state: temp file: permission denied`
every two seconds. Left alone it would have broken invariant 5 silently, so
every rollout would re-send the same incidents, which is how an operator learns
to filter this sender to trash. Fixed with `fsGroup: 65532` in the deployment,
which the other three manifests in that repo already had.

**The startup line counted configured paths, not journals.** `watching 1 path(s)`
printed the same whether the directory held two logs or none, and that is the
one line an operator reads to check the process is attached to anything. It hid
the first defect for an hour.

**The egress matrix, twelve directions, measured with a probe wearing the
notifier's own label.** Ten behaved as designed: 443 and 80 outward denied, the
five planes inside denied, a mail port to a private address denied, the cloud
metadata address denied, 587 to a public submission service allowed. Two did
not, and both are findings rather than mistakes:

- **2525 is denied**, because the policy does not list it. That is the
  alternative submission port SendGrid, Mailgun and others offer precisely for
  networks where 25 and 587 are closed. An operator on such a provider will hit
  our policy and not know why.
- **25 is denied by AWS itself**, which blocks outbound 25 by default against
  spam. Our allowance for 25 is therefore decorative there.

## 2026-08-03, the ceiling's notice said one when ten were held

`@measured` against the unfixed binary at `d111f84`, 2026-08-03: 30 distinct
`policy_deny` events at `high` in one NDJSON file, default limits (floor `high`,
dedup 10m, ceiling 20/hour), `HERALDYX_MAIL_FILE` set,
`./bin/heraldyx --once --from-now=false`.

```
20 alerts sent
Subject: [prod-box] 1 alerts suppressed this hour
state.json:  "suppressed_since": 9
```

Ten alerts were held back and the operator was told about one. The other nine
sat in the state file.

The notice was sent from inside the event loop, on the FIRST event the ceiling
refused. `rule.Decide` had just called `NoteSuppressed`, so the counter stood at
exactly 1 at that instant: `TakeSuppressionNotice` took that 1, reset the
counter, and stamped the one-per-hour window that then blocked every later event
of the same burst. The remainder could only leave on a further suppression more
than an hour later, if one ever came.

This lands during the exact event the ceiling exists for, and it understates,
which is invariant 8's rule about never claiming the stronger fact, inverted.

`rule` itself was never wrong about it. `TestCeilingHolds` has asserted since
the first commit that 995 suppressed events produce one notice carrying 995. The
defect was entirely in the caller, which is the shape a package's own unit tests
cannot see.

**The fix.** The notice goes at the END of the cycle now, where the digest
already goes, so it carries what the whole poll held back. It is taken
unconditionally rather than only when this cycle suppressed something, because
what releases the count is the CLOCK: a remainder that can only leave on the
next suppression is a remainder nobody hears about in the ordinary ending, where
the flood stops. It now leaves on the first poll after the window opens, about
two seconds later by default. The one-per-window rate limit is untouched.

`@measured` the same input against the fixed binary, 2026-08-03:

```
20 alerts sent
Subject: [prod-box] 10 alerts suppressed this hour
state.json:  "suppressed_since": 0
```

`@measured` red before green, 2026-08-03: both new tests were run against the
unfixed code first and both failed on the count, reporting `1 alerts suppressed
this hour` where 10 and 5 were expected.

| test | what it holds |
|---|---|
| `TestTheSuppressionNoticeCountsTheWholeBurst` | the notice carries the whole burst, and `suppressed_since` is left at 0 |
| `TestAStrandedSuppressionCountLeavesOnTheNextCycle` | a count stranded by the rate limit leaves on the next cycle after the window opens, with no new event to trigger it |

`@measured` both were then verified by breaking the FIXED code, 2026-08-03.
Adding `&& heldAgent != ""` to the take, which is exactly the flush's own
condition, fails the second test and only the second: that is the half nothing
else would have covered. Moving the take back inside the loop fails the first.

`@measured` gates after the change, 2026-08-03: `gofmt -l .` clean, `go vet`
clean, `staticcheck ./...` clean, `go test -race ./...` all packages ok,
`gosec -quiet ./...` clean, `govulncheck ./...` no vulnerabilities found,
`./scripts/one-way-out.sh` OK. All three linters confirmed present at
`~/go/bin` rather than skipped by the Makefile's `command -v` guard.

`@measured` `--journal` on the reproduction run, 2026-08-03:
`records: 21 (alert 20, suppression 1)`, `outcome: 21 accepted, 0 refused`,
`chain: verifies (20 chained, 1 head(s))`, and the suppression record is
attributed to `run-29`, the last run the ceiling actually held.

`@claude` one consequence worth writing down here rather than leaving to be
found. When the flush fires with nothing held in that same cycle, there is no
agent to attribute the notice to, and this stack does not invent one (invariant
11). The mail goes and `record.Journal.Sent` counts the record it did not write,
which is what the digest already does in the same situation. `internal/record`
describes that counter as surfaced rather than hidden; `@measured` by grep,
2026-08-03, `Journal.Failures` is read by nothing outside `record_test.go`, so
the gap is counted and invisible. That is a separate defect from this one, and
it is fixed in the entry below.

## 2026-08-03, the counter that said "surfaced" and was not

`internal/record` describes `Journal.Failures` as counting records it could not
write, "surfaced rather than hidden: a journal that stopped recording is worth
an operator's attention". `@measured` by grep against `1625a31`, 2026-08-03:
nothing outside `internal/record/record_test.go` read the field, and
`cmd/heraldyx/main.go` never printed it. The counter was right and the sentence
about it was not.

Two live paths increment it, and both are ordinary rather than exotic. A
dispatch with an empty `AgentID` is not recorded, because the envelope requires
one and this stack does not invent one (invariant 11); and a chained write that
fails is counted and stepped over, because the mail has already gone. Both
reach a real cycle: a digest sent when nothing else caused a message that cycle,
and, since `1625a31`, a suppression notice flushed in a cycle that held nothing
back. In both the mail goes out, the journal stays short, and nothing said so.

`@measured` against the unfixed binary at `1625a31`, 2026-08-03: an empty event
log, `HERALDYX_MAIL_FILE` set, and a seeded state file whose digest window
opened 25 hours earlier with one condition in it,
`./bin/heraldyx --once --from-now=false`.

```
watching 1 file(s) under 1 path(s), floor high, dedup 10m0s, ceiling 20/hour
sending via file to 1 recipient(s)
Subject: [prod-box] daily summary: 1 conditions below the alert line
sent.ndjson: 0 records
```

A message went out, the journal was short by it, and the log has nothing
between those two lines.

**The fix.** `run` reports the counter's GROWTH once per cycle, beside the
`record:` and `state:` lines it already prints. `@measured` the same input
against the fixed binary, 2026-08-03:

```
record: 1 message(s) sent without a record just now, 1 since this process
started: no agent id to file them under, or the write failed. The mail went out
either way, and the journal is short by that many.
```

Growth rather than the standing count, because the counter is cumulative for the
life of the process and this runs on every poll. Reporting the total would put
the same line in the log every two seconds for a gap from an hour ago, and a log
that repeats itself is one an operator stops reading: the same failure this
component's dedup window exists to prevent in a mailbox.

`@claude` `--journal` was considered for this and deliberately does NOT carry
it. `--journal` is a separate invocation that reads the journal FILE, and a
record that was never written leaves no trace in that file, so the number would
have to come from somewhere else. `@measured` on the unrecorded run above,
2026-08-03: `--journal` reports `records: 0 (none)`, which is a true statement
about the file and says nothing about the message that went out. Carrying the
count there would mean persisting it in `state.json` and having `record.Status`,
which is documented as what the journal file says about itself, report a number
that is not in the file. Worse, a fresh state volume would reset it, so
`--journal` would print a confident zero on a box whose journal really is short.
That is invariant 8's rule about never claiming the stronger fact, and a deploy
check reading it would be the one holding the wrong answer.

`@measured` red before green, 2026-08-03. Both tests were run against the
unfixed code first. `TestAMessageSentWithoutARecordIsReported` failed on the log
assertion and only that one, so its two premise checks confirm the run really
did send the mail and really did leave the journal empty.
`TestAGapAlreadyReportedIsNotReportedAgainEveryPoll` failed with six lines where
two were expected, printing the spam it exists to prevent.

| test | what it holds |
|---|---|
| `TestAMessageSentWithoutARecordIsReported` | a digest sent with no agent to file it under leaves the journal empty AND says so in the log |
| `TestAGapAlreadyReportedIsNotReportedAgainEveryPoll` | one line per cycle that actually missed a record, carrying the growth and the total, not one line per poll |

`@measured` both were then verified by breaking the FIXED code, 2026-08-03, and
each break failed its own test and only its own. Moving the report below `--once`'s
`return` fails the first. Reporting `failures` in place of `failures-said` fails
the second, on the assertion that the later gap names what it added.

`@measured` gates after the change, 2026-08-03: `gofmt -l .` clean, `go vet`
clean, `staticcheck ./...` clean, `go test -race ./...` all ten packages ok,
`gosec -quiet ./...` clean, `govulncheck ./...` no vulnerabilities found,
`./scripts/one-way-out.sh` OK. All three linters confirmed present at `~/go/bin`
rather than skipped by the Makefile's `command -v` guard.

`@claude` what this does NOT do. The counter still lumps two different facts
together: a record deliberately skipped for want of a subject, which is invariant
11 working, and a write that failed, which is a fault. The log line names both
because one number cannot tell them apart. Splitting the counter is a change to
`internal/record`'s own shape and was left alone.

## 2026-08-05, a swept name that was missed

A read-only audit on 2026-08-05 found that `internal/fleet/fleet.go` still had
`case "impossible_travel": return Odd, "used from two places at once"` in its
`describe` switch, three lines below where PR #23 (commit 1d7c78e) had removed
the adjacent `case "behavior_anomaly"` for the documented reason that idryx
"has no event writer at all" and therefore cannot emit it. That reason applies
identically to `impossible_travel`.

`@measured` by grep against this repository, 2026-08-05, all four idryx-
reserved names from `heraldyx-plan.md` section 1.3 (`impossible_travel`,
`behavior_anomaly`, `excessive_privilege`, `blast_radius_change`):

| name | where it still appeared | what it is |
|---|---|---|
| `impossible_travel` | `internal/fleet/fleet.go:110` (a live `case`) | the miss: removed below |
| `behavior_anomaly` | `README.md:136`, `README.md:275`, `internal/render/render.go:214` (comment), `VALIDATION.md` (2026-08-03 entry) | honest past-tense history: "were listed here until 2026-08-03... removed because nothing raises them". Left alone. |
| `excessive_privilege` | same four places as `behavior_anomaly` | same: honest past-tense history. Left alone. |
| `blast_radius_change` | nowhere in this repository | never had code or docs here to begin with |

`heraldyx-plan.md` itself (`~/Development/heraldyx-plan.md`) also names all
four in its section 1.3 registry table, dated 2026-08-02. That file is not
part of this git repository (it lives one directory up, and `git log` here has
never touched it), so it is outside this sweep's scope; it is also a point in
time record of the shared envelope's registry, not a claim about what heraldyx
emits.

`@measured` red before green, 2026-08-05. `TestIdryxReservedNamesFallToTheDefaultBranch`
was run against the unfixed code first, a table test over all four reserved
names asserting each falls to `describe`'s default branch (`what == ""`):

```
=== RUN   TestIdryxReservedNamesFallToTheDefaultBranch
=== RUN   TestIdryxReservedNamesFallToTheDefaultBranch/impossible_travel
    fleet_test.go:79: idryx cannot emit "impossible_travel"; describe() still has a branch for it: kind=2 what="used from two places at once"
=== RUN   TestIdryxReservedNamesFallToTheDefaultBranch/behavior_anomaly
=== RUN   TestIdryxReservedNamesFallToTheDefaultBranch/excessive_privilege
=== RUN   TestIdryxReservedNamesFallToTheDefaultBranch/blast_radius_change
--- FAIL: TestIdryxReservedNamesFallToTheDefaultBranch (0.00s)
    --- FAIL: TestIdryxReservedNamesFallToTheDefaultBranch/impossible_travel (0.00s)
    --- PASS: TestIdryxReservedNamesFallToTheDefaultBranch/behavior_anomaly (0.00s)
    --- PASS: TestIdryxReservedNamesFallToTheDefaultBranch/excessive_privilege (0.00s)
    --- PASS: TestIdryxReservedNamesFallToTheDefaultBranch/blast_radius_change (0.00s)
FAIL
```

Only `impossible_travel` failed, which confirms the other three already had no
branch in this switch before this change: the earlier sweep's miss was exactly
the one case named in the audit, nothing more.

**The fix.** The `impossible_travel` case is removed from `fleet.describe`,
with a comment naming what was swept and what was left, the same shape as the
comment PR #23 left in `internal/render/render.go`.

`@measured` the same test against the fixed code, 2026-08-05: all four
subtests pass.

`@measured` gates after the change, 2026-08-05: `gofmt -l .` clean, `go vet`
clean, `go build ./...` clean, `staticcheck ./...` clean,
`go test -race ./...` all ten packages ok, `gosec -quiet ./...` clean,
`govulncheck ./...` no vulnerabilities found, `./scripts/one-way-out.sh` OK.

The new table test adds one test function, so `scripts/readme-numbers.sh`
moved from `97` to `98`; the README badge is updated in the same commit.

## 2026-08-05, a stale gate citation

CLAUDE.md's invariant 2 cited `TestTheOnlyLinkIsAConsoleView` as what holds
"the mail carries a coordinate, never a control." `@measured` by grep,
2026-08-05: no function of that name exists anywhere in this repository. It
was renamed to `TestEveryLinkIsAConsoleView` (`internal/render/render_test.go`
line 83) when the mail grew from one link to three; the rename itself was
never a defect, only the cross-reference left behind by it. The invariant
still holds functionally: the renamed test asserts the exact URL of all three
links and that none carries a query string, the same property the old name
described.

This has no code path to break, so there is no red/green run against the
binary. The equivalent here is grep: it found the old name nowhere and the new
name in exactly the place CLAUDE.md now cites.

While there, every OTHER test name CLAUDE.md cites was checked the same way,
extracted programmatically (`grep -oE 'Test[A-Za-z]+' CLAUDE.md | sort -u`, 21
names, including one outside the 13 numbered invariants:
`TestTheCatalogDoesNotRepeatTheFourClaimsThatWereFalse` in "Decisions that
have no gate yet"), each checked against `func <name>(` across the module.
`@measured` 2026-08-05: `TestTheOnlyLinkIsAConsoleView` was the only miss. The
other 20 all resolve to a real function.

## 2026-08-05, a gate claim wider than the gate

README.md (around lines 30-32) and the aria-label of
`docs/assets/one-way-out.svg` both say `scripts/one-way-out.sh` fails the
build if any of three rules is broken, one of them being that
`internal/rule`, `internal/render` **and** `internal/fleet` touch no I/O at
all. `@measured` by reading the script, 2026-08-05: its case statement (then
lines 42-51) named only `internal/render` and `internal/rule`.
`internal/fleet` was never checked, so that third of the claim was held by
inspection, not by CI.

`@measured` red, 2026-08-05: a temporary `os` import (plus `var _ = os.Args`
to keep the package compiling) was added to `internal/fleet/fleet.go`, and
the UNCHANGED script run against it:

```
OK: SMTP lives in internal/deliver only, nothing speaks HTTP, the decision layer does no I/O.
```

Exit 0. The gate passed with an I/O import sitting in the exact package the
published claim says it covers, which is the defect stated as a reproduction
rather than an inference. (An earlier attempt used `net/http` for this and was
the wrong choice: `net/http` is already banned repository-wide by a separate,
unconditional rule, so it correctly failed even against the unfixed script and
proved nothing about the `internal/fleet`-specific gap. `os` is the import
CLAUDE.md's own invariant 3 and this file's first entry already use for the
same kind of proof, and it is caught only by the per-package
`banned_for_decision` rule.) The temporary import was reverted
(`git diff` empty) before touching the script.

**The fix.** `internal/fleet` is added to the script's case statement, the
same branch `internal/render` and `internal/rule` are already in, plus the
header comment and the two echoed messages, so the script's own words match
what it checks. The package is genuinely pure today (it builds its "around it
right now" phrases from an event's type and numeric fields only, the same
constraint the mail body itself is under, per its own package doc comment),
so this makes the claim true by extending the gate to match code that was
already correct, rather than by narrowing the README's claim to match a
weaker gate.

`@measured` green, 2026-08-05: the same temporary `os` import, against the
EXTENDED script:

```
FAIL: github.com/TAIPANBOX/heraldyx/internal/fleet imports os; rule, render and fleet do no I/O
```

Exit 1, naming `internal/fleet` by path. `@measured` immediately after,
2026-08-05: the temporary import reverted, `git diff internal/fleet/fleet.go`
empty, and the extended script run again on the clean tree:

```
OK: SMTP lives in internal/deliver only, nothing speaks HTTP, rule, render and fleet do no I/O.
```

`@measured` `git status` and `git diff --stat` immediately before staging this
commit, 2026-08-05: only `scripts/one-way-out.sh` modified, confirming the
temporary import left no trace in what was committed.

`@measured` gates after the change, 2026-08-05: `gofmt -l .` clean, `go vet`
clean, `go build ./...` clean, `go test -race ./...` all ten packages ok,
`staticcheck ./...` clean, `gosec -quiet ./...` clean, `govulncheck ./...` no
vulnerabilities found, `./scripts/one-way-out.sh` OK,
`./scripts/readme-numbers.sh` unchanged at 98 (this defect added no test
function, so the badge did not move).

`@claude` what this does NOT do. `docs/assets/one-way-out.svg` also draws the
same undercount visually ("FAIL if rule or render reach", without fleet) a
few lines below its own aria-label, which already says all three. That is a
second, narrower echo of the same defect, inside a diagram this task did not
scope in and that a text edit alone cannot safely fix without touching the
SVG's layout. Left alone and reported rather than fixed. `CLAUDE.md`'s
invariant 3 has the identical gap in its own prose ("`internal/render` and
`internal/rule` do no I/O of any kind", no `internal/fleet`), also outside
what this defect named (README.md and the SVG aria-label) and also left
alone.

## 2026-08-05, the one unsurfaced write-adjacent failure

`cmd/heraldyx/main.go` had `defer journal.Close()` at the end of `run()`, its
return value discarded. `@claude`: this was the only write-adjacent failure
mode in the codebase that was neither counted (unlike a per-dispatch write
failure, which increments `record.Journal.Failures`) nor logged (unlike every
other error path in `run()`: `record.Open` and `state.Load` both log and
continue), in a repo whose invariant 13 is about surfacing exactly this class
of gap.

**The fix.** The close is wrapped in a small `closeJournal(journal, path)`
helper, in the same spirit `cycle` was already split out of `run()` "so a
test can drive it with a fixed clock." It logs a failed close with
`log.Printf("record: close %s: %v", path, err)`, matching the voice of the
`record.Open` error a few lines above it in the same function and of
`state.Load`'s error a few lines below.

`@claude` on NOT folding this into `Journal.Failures`, considered and
rejected. `Failures` counts per-dispatch write failures, and its growth is
reported by `sayUnrecorded` once per poll cycle, called from inside the loop
in `run()`. The deferred close runs exactly once, after that loop has already
returned (through `--once` or the stop signal), so an increment there would
never reach `sayUnrecorded` and would be exactly the kind of counter this
defect is about: real, and never read. A close failure is also a different
fact from a per-message write failure: it is about the journal file as a
whole, at the moment this process is shutting down, not about one dispatch.
Logging it directly says the true thing without stretching a counter to cover
a case its own reporting mechanism cannot reach.

`@measured` red before green, 2026-08-05, in two steps because the seam did
not exist on unfixed code. `TestAJournalCloseFailureIsLogged` opens a
journal, closes it once (asserting that first close succeeds, as the
premise), then calls `closeJournal` on the already-closed journal and asserts
the log contains `record: close`. Run against the literally unfixed code (a
bare `defer journal.Close()`, no helper), it fails to build:

```
cmd/heraldyx/main_test.go:547:2: undefined: closeJournal
```

A compile-time red, because `closeJournal` is the seam this fix adds; there
is nothing running yet to assert against. Adding the helper WITHOUT the
`log.Printf` call (an intermediate step, not committed on its own) makes it
build and fail on the assertion instead:

```
=== RUN   TestAJournalCloseFailureIsLogged
    main_test.go:550: a journal close failure must be logged, got:
--- FAIL: TestAJournalCloseFailureIsLogged (0.00s)
FAIL
```

`@measured` the same test against the finished fix, 2026-08-05: passes. The
actual line it now asserts on, captured separately with a throwaway probe
(not part of the committed suite):

```
record: close /.../sent.ndjson: close /.../sent.ndjson: file already closed
```

The path appears twice because `os.File.Close()`'s own error already reads
"close <path>: <reason>", the same shape `record.Open`'s existing errors have
("record: open %s: %w" wrapping an `*os.PathError` that already says "open
<path>: ..."), so `log.Printf("record: %v", err)` a few lines above this
change already double-prefixes "record:" for exactly the same reason. Not
polished away, because it was not introduced by this change either.

The double-close used to produce that error is a real, deterministic way to
make `Close` fail without an interface seam or a platform-specific fd trick;
forcing `record.Open`'s own file handle to fail on close from outside the
package it wraps is not practical, which is why `closeJournal` is tested
directly rather than through `run()`.

`@measured` gates after the change, 2026-08-05: `gofmt -l .` clean, `go vet`
clean, `go build ./...` clean, `go test -race ./...` all ten packages ok, zero
failures, `staticcheck ./...` clean, `gosec -quiet ./...` clean,
`govulncheck ./...` no vulnerabilities found, `./scripts/one-way-out.sh` OK.
The new test function moved `scripts/readme-numbers.sh` from 98 to 99; the
badge is updated in this commit.

## 2026-08-05, an owner from a passport file reached the mail with no shape check

A read-only audit on 2026-08-05 found that `internal/passport/passport.go`
(around lines 103-114) parses `id` and `owner` from each passport JSON file
with no validation of the owner string's shape or length. That value then
reached `internal/render/render.go` twice: the body line
`fmt.Fprintf(&b, "\nAnswerable for it: %s\n", owner)` (around line 271) with
no escaping at all, and `OwnerLink` (around line 438), which URL-path-escapes
the value but never shape-filtered it first. Every other string this file
renders passes a strict gate before that: the `data` allowlist plus
`safeString`'s 64-character identifier-shaped regex, which is invariant 1.
`owner` reached the mail through a separate parameter that mechanism never
sees.

`@claude` on severity, honestly, because the provenance is not the same for
the two. `data` is written by producers that sit next to prompts, model
output and matched secrets, invariant 1's own words; `owner` comes from a
file the operator wrote or generated. That makes this a lower-severity defect
than an unvalidated producer field, and it does not make it acceptable: a
passport directory can be large, machine-generated, or synced from an
inventory system, and this process has no way to tell a hand-checked file
from a stale export. A multi-line owner value reached a plain-text mail body
through an `Fprintf` call with no escaping.

`@measured` red before green, 2026-08-05. Five tests were written first and
run against the unfixed code:

```
$ go test ./internal/render/... -run 'TestAnOversizedOwnerDoesNotReachTheMail|TestAnOwnerWithAControlCharacterDoesNotReachTheMailOrTheLink|TestARealisticOwnerStillReachesTheMailUnchanged|TestOwnerLengthCapIsExact|TestOwnerLinkRefusesAnUnsafeOwnerOnItsOwn' -v
```

The one that matters most, verbatim, because it is the injection itself
rather than an inference about it:

```
=== RUN   TestAnOwnerWithAControlCharacterDoesNotReachTheMailOrTheLink
    render_test.go:309: owner "team\nBcc: attacker@example.com" injected a line into the mail body:
        Run run-42 (agent agent://acme.example/biller) was killed.
        
        What this box already did: The run is stopped. Gateways refuse its calls.
        
        If nobody acts: Nothing. This is a final state until a new run is started.
        
        Answerable for it: team
        Bcc: attacker@example.com
        
        Open in your console:
          what happened   https://box.example.com/i/run_killed:run-42
          this agent      https://box.example.com/a/agent://acme.example/biller   (freeze, kill)
          its owner       https://box.example.com/o/team%0ABcc:%20attacker@example.com   (everything they run)
        
        Raised by tokenfuse at 2026-08-02 14:03:00 UTC. This mail carries identifiers and numbers only, never the content of a call.
--- FAIL: TestAnOwnerWithAControlCharacterDoesNotReachTheMailOrTheLink (0.00s)
```

The owner value became a second line inside the body a human reads as plain
text, immediately after "Answerable for it: team". The other four,
summarized, full output reproducible with the command above:
`TestAnOversizedOwnerDoesNotReachTheMail` failed with a 500-character owner
rendered whole into the body and the console link; `TestOwnerLengthCapIsExact`
failed on its over-cap half, "a 65-character owner was accepted";
`TestOwnerLinkRefusesAnUnsafeOwnerOnItsOwn` failed with `OwnerLink` returning
`https://box.example.com/o/team%0ABcc:%20attacker@example.com` for the same
control-character owner, called directly rather than through `Event`;
`TestARealisticOwnerStillReachesTheMailUnchanged` passed even against the
unfixed code, which is expected and not a defect: nothing was ever wrong with
the legitimate path, and that test exists to hold it unchanged by the fix
rather than to catch a break in it.

**The fix, and where it lives.** Validation runs in `internal/render`, at the
point every other string this package renders is already held to a shape
rule, rather than in `internal/passport` at parse time. `internal/render`'s
own package doc comment already states its charter: it is the one gate for
what may reach a mail, and the `data` allowlist and `safeString` live here for
exactly that reason. Putting owner's rule anywhere else would leave that
charter false for one of the five sources the anatomy diagram names. It also
costs nothing in imports: `regexp` and `fmt` were already imported in this
file, so `scripts/one-way-out.sh`, which forbids `internal/render` from doing
any I/O, was never at risk. `@measured` immediately after the fix, 2026-08-05:
`./scripts/one-way-out.sh` still reports `OK: SMTP lives in internal/deliver
only, nothing speaks HTTP, rule, render and fleet do no I/O.`

The alternative, refusing a bad owner at parse time in `internal/passport`,
was considered and not chosen. Its one real advantage is visibility:
`Directory.Malformed` already counts passport files that did not parse, and a
shape violation could have joined that count, giving an operator an aggregate
signal, "N passport files have unusable owners", that the render-time fix does
not provide; a render-time refusal is silent per message, with nothing counted
anywhere. That cost is real and is written down here rather than only in the
PR body. What choosing it would also have cost: `Malformed` is documented and
tested (`TestAMalformedPassportIsCountedNotFatal`) as counting files that did
not PARSE, and a passport with a present but oversized or oddly-shaped owner
does parse; folding a shape violation into that counter, or adding a second
one, is a change to `passport.Directory`'s own contract that the render-time
fix avoids entirely. `internal/passport` also stays exactly as narrow as its
own package doc already describes it: a file reader that holds an opinion
about nothing but presence.

Two functions needed the check, not one, because the defect named both.
`sanitizeOwner` is called once at the top of `Event`, which covers the body
line directly and, since `Event`'s own `if owner != ""` guards are unchanged,
the console-link line the same way `owner == ""` already worked. `OwnerLink`
is exported and was not, before this fix, reachable only through `Event`, so
it calls `sanitizeOwner` again on its own argument: it is the second of the
two reach points the audit named, and it has to be safe to call directly, not
merely safe because its one current caller happens to pre-check.

`maxOwnerLength` is `64`, a named constant with its own comment in
`internal/render/render.go`. It mirrors the cap `safeString` already holds
every other short identifier-shaped string reaching this mail to, so this
file has one number for "short enough for a mail line and a console link",
not two that could drift apart. `ownerShape` is `^[A-Za-z0-9_:@./\- ]+$`,
deliberately the same character class `safeString` uses: letters, digits,
`_ : @ . / -` and space. A team name, an email address and a Slack handle all
fit it, tested against `"platform-team"`, `"sre@example.com"`, `"@jane"`,
`"w.zhang"` and `"team-finance@acme.example"`, all of which reach the mail
unchanged both before and after the fix. A newline, a carriage return, a NUL
byte and a tab do not, and each was tested individually reaching neither the
body nor the link. A value that fails either check is treated exactly as
`Event` already treats an agent with no passport: dropped, never truncated
and never escaped into something that still reads like a real owner, which is
the behaviour `TestNoOwnerMeansNoOwnerLine` already held before this change
and, re-run, still holds after it.

One pre-existing test collided with this fix and needed to move, not because
it was wrong, but because what it was testing moved out from under it.
`TestWhatIsInsideASegmentIsStillEscaped` (`internal/render/link_test.go`)
called `OwnerLink(cfg, "team a/b?c#d")` to prove that `escapePath` still
escapes `?`, `#` and a space inside a path segment. `@measured` by running it
against this fix in isolation, 2026-08-05: it failed, `unexpected escaping:
""`, because `"team a/b?c#d"` is not a realistic owner, it contains `?` and
`#`, neither in `ownerShape`, so `OwnerLink` now refuses it before
`escapePath` ever runs. `@measured` against the unmodified `main` branch,
2026-08-05, confirming this is not a pre-existing failure: the same test, run
alone via `git stash`, passes. The test now calls `AgentLink` instead, which
carries no shape rule and was always the other function in this file built on
`escapePath`; the assertion is otherwise unchanged, same probe string, same
expected escaping, prefix `/a/` instead of `/o/`.

`@measured` gates after the change, 2026-08-05: `gofmt -l .` clean,
`go vet ./...` clean, `go build ./...` clean, `go test -race ./...` all ten
packages ok, `staticcheck ./...` clean, `gosec -quiet ./...` clean,
`govulncheck ./...` no vulnerabilities found, `./scripts/one-way-out.sh` OK,
`make gates` clean end to end. Five new test functions moved
`scripts/readme-numbers.sh` from `99` to `104`; the README badge is updated in
the same commit.

## 2026-08-05, an agent could mute its own alerts by choosing its own name

The same read-only audit found the mirror image of the owner defect above, on
the field that cannot be dropped. `agent_id` and `run_id` come off the wire and
reach `shortID`, `describe`, `Link` and `AgentLink` in
`internal/render/render.go` with no independent check, and `rule.Subject` puts
one of them straight into the mail SUBJECT.

The header injection itself was already stopped, and correctly:
`internal/deliver/deliver.go`'s `Compose` calls `checkHeaderSafe("subject", ...)`
and refuses any value containing a carriage return or a line feed. The
CONSEQUENCE is what the audit found. `Compose` returns an error, `deliver_`
never sends, and the alert is gone. In tokenfuse `agent_id` is taken from the
caller-written `x-fuse-agent-id` header, so the agent an alert is ABOUT chooses
the value that decides whether the alert can be built at all. An agent that
puts a line break in its own name makes itself invisible to the notification
plane.

That is worse than the injection it was refusing. Every other limit in this
repo is a limit on MESSAGES chosen by the operator (invariant 6), and this was
a limit on messages chosen by the thing being watched.

To be exact about what "silent" means here, because the file it is written in
is about evidence: the loss is not silent INSIDE the process. `run` logs
`delivery failed (smtp): ...` and the dispatch journal files an
`"outcome":"refused"` record carrying the error, which is invariant 7 working
as designed. It is silent in the only place that matters at three in the
morning, the operator's mailbox, and both of the traces it does leave are read
after an incident rather than during one.

`@measured` red before green, 2026-08-06 (the audit that found it is dated
2026-08-05, the day before). Ten test functions were written first and run
against the unfixed code. The headline one, verbatim:

```
$ go test ./internal/render/ -run TestAnAgentIDWithALineBreakStillReachesTheOperator -v
=== RUN   TestAnAgentIDWithALineBreakStillReachesTheOperator
    render_test.go:388: the subject carries a line break, so deliver.Compose refuses the whole message and nothing is sent: "[prod-box] agent://x/y\nBcc: attacker@example.com was refused by the breaker"
--- FAIL: TestAnAgentIDWithALineBreakStillReachesTheOperator (0.00s)
```

And the one that states the property rather than the symptom, from the side
that refuses, verbatim:

```
$ go test ./internal/deliver/ -run TestNoProducerSuppliedFieldCanStopDelivery
    --- FAIL: TestNoProducerSuppliedFieldCanStopDelivery/with_no_run_id,_agent_id_with_a_line_feed (0.00s)
        deliver_test.go:179: a producer stopped its own alert by writing a line feed into agent_id: deliver: subject contains a line break, refusing to build a message from it
    --- FAIL: TestNoProducerSuppliedFieldCanStopDelivery/with_no_run_id,_agent_id_with_a_crlf (0.00s)
        deliver_test.go:179: a producer stopped its own alert by writing a crlf into agent_id: deliver: subject contains a line break, refusing to build a message from it
    --- FAIL: TestNoProducerSuppliedFieldCanStopDelivery/with_a_run_id,_run_id_with_a_line_feed (0.00s)
        deliver_test.go:179: a producer stopped its own alert by writing a line feed into run_id: deliver: subject contains a line break, refusing to build a message from it
    --- FAIL: TestNoProducerSuppliedFieldCanStopDelivery/with_a_run_id,_type_with_a_line_feed (0.00s)
        deliver_test.go:179: a producer stopped its own alert by writing a line feed into type: deliver: subject contains a line break, refusing to build a message from it
```

That test runs each hostile value through an event with a run id and through
one without, on purpose: `rule.Subject` prefers the run id, so an event that
has one never puts the agent id in the subject. Written without that split,
the agent id case passes against the unfixed code and the whole test is
theatre. The event that carries the defect is the ordinary one about an agent
rather than a run, which is exactly what an agent-scoped alert is.

`@measured` end to end, 2026-08-06, because a unit test asserting on a struct
is not the claim being made. The binary was built twice, once from `main` and
once from this branch, pointed at a one-line event log whose `agent_id` is
`agent://x/y\nBcc: attacker@example.com`, and sent through a loopback SMTP sink
(a 45-line Python script on 127.0.0.1, no credentials, nothing metered, nothing
leaving the machine). From `main`:

```
2026/08/06 16:13:36 sending via smtp to 1 recipient(s)
2026/08/06 16:13:36 delivery failed (smtp): deliver: subject contains a line break, refusing to build a message from it
SINK: nothing ever connected
--- what the mail server received: ---
[       0 bytes]
```

From this branch, same log, same sink:

```
Subject: [prod-box] "agent://x/y\nBcc: attacker@example.com" was refused by the breaker

Agent "agent://x/y\nBcc: attacker@example.com" was refused by the breaker.

An identifier in this event is not the shape this box can render as written: identifier-like characters only, at most 255 bytes, the cap agent-passport SPEC 3.1 puts on an agent:// URI. It is shown above escaped, and no console link is built from it, because a mangled id addresses nothing. The alert was sent anyway: an id nobody can parse is a reason to look, not a reason to say nothing.

What this box already did: The call was refused with a hard 402 before it reached the provider.

If nobody acts: The agent sees an error. Whether it retries or stops is up to the agent.

Nothing here can be opened in your console: the identifiers this event carries are not ones a link can be built from.

Raised by tokenfuse at 2026-08-05 09:00:00 UTC. This mail carries identifiers and numbers only, never the content of a call.
[    1176 bytes]
```

The mail server received nothing before and 1176 bytes after, and `Compose`'s
guard is untouched in both: what changed is that nothing rendered can reach it
any more.

**The fix, and where it lives.** In `internal/render`, for the reason the owner
fix is there and one more. `internal/render`'s package doc states its charter
as the one gate for what may reach a mail, and the `data` allowlist,
`safeString` and `sanitizeOwner` are all here already; a fourth rule for the
same question belongs beside them rather than in a fourth place. The one more
is that `internal/deliver` is the wrong layer by construction: it sees a
`render.Message`, two opaque strings, with no way to tell an id from a
sentence, so the only thing it can do about a bad value is refuse the whole
message, which IS the defect. A guard that can only say no cannot be the layer
that decides. `Compose`'s check stays exactly as it was and is now the last
line rather than the only one, which is what
`TestNoProducerSuppliedFieldCanStopDelivery` pins.

**The consequence is escape and bound, never drop and never refuse.** An owner
that fails its check is dropped, because the mail is still about something
without it. An identifier IS what the mail is about, so the same treatment
would produce an alert naming nothing, and refusing the message is how this
defect worked in the first place. `safeID` returns the id as written when it is
written in a shape this file can render, and otherwise returns
`strconv.Quote(id[:255])`. `Quote` was chosen for one property above
readability: whatever the input, its output contains no line break, no control
character and no invalid byte, so a value that reaches `Compose` through it
cannot make `Compose` refuse. It also keeps bytes a human still recognises,
which a placeholder would not: the mail above still names `agent://x/y`.

**The agent-passport URI grammar was considered as the rule here and is not
it.** `^agent://[a-z0-9.-]+/[a-z0-9._/-]+$` (agent-stack-go's
`passport.ValidateAgentURI`) is the right rule for whether an id is
well-formed and the wrong rule for whether a mail can carry it, on three
counts. It is agent-specific, and `run_id`, `type` and `source` reach the same
subject line with no grammar of their own, so a grammar-shaped gate would have
left the identical hole open one field over; `run_id` is the one the subject
usually carries. It rejects ids this box renders correctly today, uppercase
among them, and a mail that mangles a working id to satisfy a grammar is a
worse mail than the one it replaces. And a grammar violation is not what breaks
anything: a line break and an unbounded length are, and those are what the rule
checks. What the grammar contributes instead is its length cap, 255 bytes from
agent-passport SPEC 3.1, reused here as `maxIDBytes` so that nothing
well-formed is anywhere near the bound.

**A link is refused where a sentence is escaped.** `Link` and `AgentLink` now
return "" for an id that is not renderable as written, which is the answer
`OwnerLink` already gives for an owner: `escapePath` percent-encodes a line
break into a well-formed URL that addresses nothing, and a coordinate that
addresses nothing is worse than an absent one. Refusing a link never costs the
message. When a console IS configured and no link can be built, the body says
that in its own words rather than reusing the "no console address is
configured" sentence, because telling an operator the wrong one of those two
sends them to change a setting that is already right.

**Three more fields went through the same sweep**, found while fixing the
first and each a reachable path to the same outcome:

- `type` reaches the subject verbatim in the branch for a type the catalog does
  not know (`head = ... e.Type`), which is a second way to a refused `Compose`.
- `source` is rendered into the BODY, where it cannot break a header and can
  add LINES. `TestTheSourceCannotAddLinesToTheBody` failed against the unfixed
  code with a body carrying two "Open in your console:" blocks, the second one
  the producer's own, with the producer's own address under it. That is
  phishing through the notification plane, from a value nothing checked.
- the digest's rows and the context rows are `type:subject` and an agent id
  from the same log, printed into the body with the same absence of a check.

**And the size of the message is no longer the producer's to choose.** An id
was bounded in the subject by `shortID` and bounded nowhere in the links, so
`TestAnOversizedIdentifierCannotSetTheSizeOfTheMail` failed with
`one identifier grew the message to 200668 bytes` from a single 100 kB agent
id. A message a mail server refuses on size is the same silence reached the
long way round, which is why that test is in this fix rather than filed as a
separate nicety.

**One pre-existing test collided with the fix and moved, for the second time
and the same reason.** `TestWhatIsInsideASegmentIsStillEscaped`
(`internal/render/link_test.go`) proves `escapePath` still escapes `?`, `#`
and a space inside a path segment. It ran through `OwnerLink` until the owner
fix on 2026-08-05, when `sanitizeOwner` began refusing its probe, and moved to
`AgentLink`, which then had no shape rule. `AgentLink` has one now, and
`@measured` against this fix, 2026-08-06, it failed with
`link_test.go:56: unexpected escaping: ""`. The three properties now sit where
each one lives: a space, which an id may be written with and which still has to
be escaped, exercises `AgentLink`; the `?#` probe exercises the refusal; and
`escapePath` is called directly for what it does inside a segment, asserting
the same expected output as before, `team%20a/b%3Fc%23d`. `@measured` against
the unfixed `render.go` by stashing it, 2026-08-06, confirming the revised test
is red before the fix and not merely rewritten to agree with it:
`link_test.go:68: an id shaped nothing like an id was still turned into a link: "https://box/a/team%20a/b%3Fc%23d"`.

`@measured` gates after the change, 2026-08-06: `gofmt -l .` clean,
`go vet ./...` clean, `go test -race ./...` all ten packages ok, `staticcheck
./...` clean, `gosec -quiet ./...` clean, `govulncheck ./...` no vulnerabilities
found, `./scripts/one-way-out.sh` OK (`strconv` is neither I/O nor network, so
the no-I/O tier is untouched), `make gates` clean end to end. Ten new test
functions moved `scripts/readme-numbers.sh` from `104` to `114`; the README
badge is updated in the same commit.

## 2026-08-05, two artifacts still describing a gate as it was before it was widened

`scripts/one-way-out.sh` was extended on 2026-08-05 (commit 809b18d) to hold
the no-I/O rule over `internal/fleet` as well as `internal/render` and
`internal/rule`, which is what README.md and `docs/assets/one-way-out.svg` had
described all along. Two artifacts went on describing the old two-package
version:

- the drawn text inside `docs/assets/one-way-out.svg` read `FAIL if rule or
  render reach` / `os, net, io or exec`, in the third of the three gate boxes.
  The same file's `aria-label` and the README's `alt` text already said three,
  so the picture disagreed with its own description.
- CLAUDE.md invariant 3's prose named `internal/render` and `internal/rule`
  only.

Both now say what the script does. `@measured` against the script itself rather
than against memory, 2026-08-06: `scripts/one-way-out.sh` line 46 matches
`internal/render|internal/rule|internal/fleet`, and running it prints
`OK: SMTP lives in internal/deliver only, nothing speaks HTTP, rule, render and
fleet do no I/O.`

This is the low-severity half of the pair, and it is the half that decays
quietly. A gate that checks less than its description claims fails safe; a
description that names less than the gate is what somebody reads INSTEAD of the
script, which is exactly how a contributor concludes that a package is outside
a rule it is inside.

## What has NOT been verified

- **Deliverability at volume, and what a filter does with these.** A handful of
  messages reached real inboxes. Whether a hundred a day from a cloud address
  keep landing there, and what a corporate filter makes of them, is not
  something those sends establish.
- **Most producing planes are not wired to the event log in most deployments.**
  Measured 2026-08-03 across `stack-k8s`, `stack-single` and `stack-up`: the
  money plane's gateway path is set in all three, its cloud path in two, the
  policy plane's in one, and the quality and drill planes' in none. Every one of
  those planes CAN emit; their emitters are opt-in on an environment variable,
  and nothing turns most of them on.

  So on a real box this process can only alert on money. Policy alerts arrive in
  one deployment out of three, quality and drill alerts in none. This is the
  same defect the cluster run found for the cloud plane, which was fixed for
  that one plane and never swept for the others, and it is open.
- **The identity plane raises nothing this build can describe**, because it
  raises nothing at all. `behavior_anomaly` and `excessive_privilege` left the
  catalog on 2026-08-03 for that reason, and idryx has no event writer to give
  them one. Its findings live in its own API and its graph. If that changes,
  the entries come back from reading its code.
- **The rest of the producing planes' triggers have not been read against their
  own names.** One was: `budget_exhausted` fired on any three blocks from a set
  that includes loop detection and policy violations, so a run with no budget at
  all could receive a High incident titled "budget exhausted", and did. It was
  narrowed at the source. Finding one wrong is not evidence the others are
  right.
- **The journal has never been shipped into trailryx.** heraldyx produces the
  record and stops there, deliberately (see CLAUDE.md). Nothing has yet carried
  one of these files into the record plane, so "the notification is in the
  operator's audit store" is not a claim this repository can make today.
- **Volume under a real fleet is unmeasured.** The dedup window, the ceiling
  and the digest period are reasoned defaults carried over from the money
  plane's own alert pipeline, not numbers anyone has watched an operator live
  with.
