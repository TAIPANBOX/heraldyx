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
the gap is counted and invisible. That is a separate defect from this one and is
listed below.

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
- **`Journal.Failures` is counted and never printed.** `internal/record` says
  the count of records it could not write is "surfaced rather than hidden", and
  as of 2026-08-03 nothing outside that package's own tests reads it. Every
  path that skips a record for want of an agent id (a digest, or a suppression
  notice flushed in a cycle that held nothing) is therefore invisible to an
  operator. The counter is right; the sentence claiming it is surfaced is the
  part that is not true yet.
