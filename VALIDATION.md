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

## What has NOT been verified

- **Deliverability at volume, and what a filter does with these.** One message
  reached one inbox (see below). Whether a hundred a day from a cloud address
  keep landing there, and what a corporate filter makes of them, is not
  something a single send establishes.
- **Nothing has run on Kubernetes.** Since 2026-08-02 the manifest, the
  single-pod egress NetworkPolicy and the compose service all exist (in
  `stack-k8s`, `stack-single` and `stack-up`, since this repo ships no
  deployment of its own), and none of them has been applied to a live cluster
  or a real box. The sentence that used to be here said no manifest existed at
  all, which stopped being true the day one was written: what is untested is
  the RUN, not the existence.
- **The `catalog` in `internal/render` has not been checked against the
  producing planes' own docs.** Each entry claims what a type MEANS and what
  the box already did about it. The tests assert those lines exist, never that
  they are true.
- **The journal has never been shipped into trailryx.** heraldyx produces the
  record and stops there, deliberately (see CLAUDE.md). Nothing has yet carried
  one of these files into the record plane, so "the notification is in the
  operator's audit store" is not a claim this repository can make today.
- **Volume under a real fleet is unmeasured.** The dedup window, the ceiling
  and the digest period are reasoned defaults carried over from the money
  plane's own alert pipeline, not numbers anyone has watched an operator live
  with.
