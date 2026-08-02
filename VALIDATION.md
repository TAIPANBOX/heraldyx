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

## What has NOT been verified

- **No mail has been sent to a real SMTP server from this code.** Everything
  proven above runs through the file sender or `deliver.Compose`. The SMTP path
  is exercised by its configuration checks only. Until a live run happens, treat
  "heraldyx sends mail" as untested, and note that the first thing an operator
  meets, `--test-mail`, is exactly the command that would surface it.
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
