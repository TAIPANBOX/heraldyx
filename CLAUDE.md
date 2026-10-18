# CLAUDE.md, working instructions for heraldyx

These instructions apply to any model working in this repo. Read this before
changing anything. Process and invariants only, no status: status goes stale,
and a stale instruction file is worse than none. For where things stand, read
`VALIDATION.md` and the git log.

## What heraldyx is

The component that mails the operator when one of their own agents is heading
somewhere they would want to know about. It reads the shared NDJSON event log
the other planes write, decides what is worth a human's attention now, and
sends mail.

**It reads a file and sends mail.** It holds no credential for any plane, has
no API of its own, opens no connection to any plane, and can take no action on
any agent. That narrowness is the product argument, not an implementation
detail: this is the one component of the box allowed to reach the outside
world, so its blast radius has to stay small enough to state in one sentence.

## The working loop

1. Branch off `main`, one logical increment per branch.
2. Run the gates below. All must pass.
3. Commit with Conventional Commits. End the message with the standard
   co-author trailer naming the model that actually did the work.
4. Push the branch, open a PR with `gh`.
5. Wait for CI to go green.
6. **Ask the user before merging.** Do not self-merge.

## Gates

```sh
gofmt -l .
go vet ./...
go test -race ./...
./scripts/one-way-out.sh
./scripts/smtp-tls-is-explicit.sh
./scripts/readme-numbers.sh
./scripts/features-are-bound.sh # invariant 18's scenarios, bound both ways
./scripts/gates-have-teeth.sh   # invariant 16; needs a clean tree
```

`readme-numbers.sh` was missing from this list until 2026-08-09 while CI ran
it, so this instruction was strictly smaller than CI's, and anybody following
it ran one gate of two believing they had run both.

CI runs these plus `staticcheck`, `govulncheck` and `gosec`.

## Hard invariants

Each carries how it is held today. Use `(gate: ...)`, `(test: ...)`,
`(partly gated: ...)` or `(not enforced)`, and use the weakest one that is
true. An invariant with no check, written as though it had one, is worse than
an absent invariant.

1. **Mail carries identifiers and numbers, never content.** An event's `data`
   is written by producers that sit next to prompts, model output and matched
   secrets, and mail leaves the perimeter through a server nobody here
   controls. `data` is rendered through an ALLOWLIST of keys whose values must
   ALSO pass a shape check (short, one line, identifier-like). A denylist is
   one new producer away from leaking.
   *(test: `TestNoContentFromDataReachesTheMail`,
   `TestAllowlistedKeyWithUnsafeValueIsDropped`; verified by removing the
   allowlist, which fails the first)*
2. **The mail carries a coordinate, never a control.** One link, into the
   operator's own console, at a view. No action, no token, no query string. A
   link that acts is an unauthenticated capability held by anyone who sees or
   forwards the message, and mail security gateways PREFETCH links, which would
   fire the action before a human read the sentence next to it. Decided by the
   user 2026-07-21 and not open for re-litigation.
   *(test: `TestEveryLinkIsAConsoleView`; verified by appending
   `?action=kill` to the link, which fails it)*

   **And a coordinate that names an action nobody can take is the same defect
   one step softer.** `@decided 2026-08-10`: a claimed subject gets no link. A CLAIMED subject
   (`claimed:agent://...`, agent-passport SPEC 3.3) gets no agent link at all,
   and the mail says why instead of leaving a gap.

   The harm was never a route to the wrong agent: the marker is part of the
   subject, so the link addressed the claim and not the established agent's
   card. What it did was offer "(freeze, kill)" beside a card that does not
   exist, to somebody at three in the morning deciding whether to click. There
   is nothing under that name to freeze; the incident link is the move.
   *(test: `TestAClaimGetsNoFreezeOrKillCoordinate`,
   `TestAnEstablishedSubjectKeepsItsCoordinate`; verified by a mutation that
   compiles, `claimedSubject := false && ...`, which puts the line back and
   fails the first)*
3. **One way out.** `net/smtp` is imported by `internal/deliver` and nothing
   else, nothing here imports `net/http` at all, and `internal/render`,
   `internal/rule` and `internal/fleet` do no I/O of any kind. The decision
   layer that can also perform I/O is the one that eventually performs it from
   inside a branch nobody tested.

   Three packages, not two. `internal/fleet` builds the "around it right now"
   lines from the same event log the rest of this process reads, so it is the
   same tier as the two that decide what to say and whether to say it, and
   README.md and `docs/assets/one-way-out.svg` had always drawn it that way.
   The gate checked only two until `scripts/one-way-out.sh` was extended on
   2026-08-05 (commit 809b18d), and this sentence went on naming two after it.
   That half is the worse one to leave: a gate that checks less than it says
   still fails safe, while a description that names less than the gate is what
   somebody reads instead of the script.
   *(gate: `scripts/one-way-out.sh`; verified by adding an `os` import to
   `internal/rule`, which fails it)*
4. **A box with no address configured runs, stays healthy, and sends nothing.**
   Not having mail is a choice an operator makes, not a broken deployment. The
   process must never refuse to start over missing mail configuration, and
   `config.Why()` must say in one sentence which setting is missing, because
   "deliberately off" and "configured wrong" look identical in a log that only
   says nothing.
   *(test: `TestNoRecipientsIsHealthyAndSilent`,
   `TestTestMailFailsWhenNothingIsConfigured`)*
5. **Both halves of the state survive a restart, AND a blink in the file set
   is not a restart.** Read offsets AND dedup counters. A container restarts on every rollout; a process that forgets
   either one turns a quiet incident into a mailbox full of the same incident,
   which is how an operator learns to filter this sender to trash.
   The second clause was added after the first was found to be technically
   true and practically worthless. The offsets were persisted, reloaded and
   correct, and the process still re-read every log from byte zero on a live
   cluster, because `SetPaths` dropped the position of any file missing from
   the set it was handed, and resolving that set comes back short for a moment
   now and then. Persisting a number nothing later honours is not state, it is
   a decoration on a file.
   A position is also only worth keeping while it belongs to the same FILE,
   so the state file keeps a digest of each file's first line beside its
   offset, and a file is read from its start only when that line changed
   (invariant 25), never because its size looked smaller.
   *(test: `TestDedupSurvivesARestart`, `TestARestartRemembersBothHalves`,
   `TestOneEventBecomesOneMessage`'s second pass,
   `TestAPathOutOfSightForOnePollKeepsItsPlace`,
   `TestAReplacedFileIsStillReadFromTheStart` for the case that makes keeping
   a position safe, and `TestARestartRemembersWhichFileItWasReading` for the
   first line surviving a restart)*
6. **Every limit is a limit on MESSAGES, never on evidence.** Dedup, the
   ceiling and the digest decide what is said, and nothing else. heraldyx never
   suppresses, edits or delays an event in the log, because it cannot: it only
   reads. Any future feature that would need to write to the event log belongs
   in another component.
   *(gate: the read-only mount of the event volume in every deployment. That
   used to read "held by invariant 3's gate as a side effect, since writing
   would need I/O in a layer that has none", and stage 4 made that false:
   `internal/record` writes a file. It writes heraldyx's OWN file, which is
   invariant 9, and what still holds THIS one is the mount.)*
7. **A message that was sent is written down, and a message that was NOT sent
   is not.** Delivery and recording sit behind ONE condition, in
   `deliver_`. They were apart once, and on a box with no address configured
   that combination sent nothing and then recorded that a message had been
   accepted. A trail claiming a notification nobody received is worse than no
   trail: it is the exact thing an operator would later hold up as proof.
   *(test: `TestNoRecipientsMeansNoRecord`, `TestASentMessageLeavesARecord`)*
8. **Never claim the stronger fact.** Two instances, one rule.

   The record says "accepted", never "delivered": what this process observes is
   a mail server taking the message, not a mailbox showing it.

   And `--journal` does not report "verifies" for a single record. A chain of
   one binds nothing, because the first record has no predecessor to hash, so
   editing it is undetectable there. Found by running the tool against a real
   one-record journal, editing the only line, and watching it still report a
   good chain: correct behaviour for a hash chain, and a misleading sentence. A
   check that cannot fail is worse than no check, because it is louder.
   *(test: `TestTheOutcomeIsAcceptedNotDelivered`,
   `TestASingleRecordIsNotAVerifiedChain`)*
9. **The dispatch journal is heraldyx's own file, never the shared event log.**
   The planes' log is mounted read-only and that mount is invariant 6 made
   physical. Writing the record there would mean mounting it writable, which
   hands a compromised notifier the ability to corrupt the trail it reads. The
   journal is the same envelope, the same library and the same verifier, on
   this process's own volume.
   *(gate: the read-only mount in the deployments; test:
   `TestTheJournalIsAChainAndTheVerifierAgrees`)*
10. **A partial line is never consumed.** The writer on the other side is
   appending, and half an event parsed now is an event lost forever. The read
   offset only advances past a newline, and not past a line with a zero byte
   in it until the hold window of invariant 25 has passed.
   *(test: `TestAPartialLineIsNotConsumed`,
   `TestALineWithAZeroByteIsHeldUntilItFillsIn`)*

11. **What cannot name an agent does not arrive, and must not be made to.**
   The shared envelope requires an `agent_id` (agent-passport SPEC.md section
   6.1). A signal about a whole organisation therefore has no subject to travel
   under, its producer skips it rather than inventing one, and this process
   never sees it. `spend_spike` is the live example: raised by the money plane,
   shown in the console, absent here.

   Accepted as a boundary of this plane by the user on 2026-08-03 and written
   into the README where an operator will meet it, rather than left as a
   surprise for the night something org-wide goes wrong.

   The failure this forecloses is the tempting one: a fallback subject, a
   "various" agent, an org id in the `agent_id` field. Any of those makes every
   downstream count wrong and puts a name in a subject line that did not do the
   thing. If org-wide facts are ever to be mailed, the envelope grows a subject
   kind and every product changes together; it is not a feature of the
   notifier.
   *(not enforced: nothing here can stop a future producer from fabricating a
   subject upstream. What holds it is that the exporter in tokenfuse counts
   skips instead, and its own invariant 6 forbids the fabrication)*

12. **A limit's own notice reports the true count.** The hourly ceiling's notice
   is sent at the END of a poll cycle, carrying everything held back since the
   last notice, and a count stranded by the one-per-window rate limit leaves on
   the first cycle after that window opens rather than waiting for a further
   suppression to push it out.

   Sent from inside the event loop, as it was until 2026-08-03, it reported the
   FIRST event a burst was refused and stranded the rest: `rule.Decide` has just
   counted that one event, so the counter is exactly 1 when the notice takes it,
   and taking it stamps the window that blocks everything behind it. Measured on
   30 events against a ceiling of 20: ten alerts held back, a mail saying one,
   nine left in the state file.

   A notice that understates a flood is worse than a missing one, because the
   operator reads the small number and stops looking, during the exact event the
   ceiling exists for. Invariant 8 says never claim the stronger fact; this is
   that rule inverted, and it costs the same thing.
   *(test: `TestTheSuppressionNoticeCountsTheWholeBurst`,
   `TestAStrandedSuppressionCountLeavesOnTheNextCycle`; verified by moving the
   take back inside the loop, which fails the first, and by making the take
   conditional on a suppression in the same cycle, which fails the second)*

13. **A message that went out with no record behind it is said out loud.**
   `record.Journal` counts every dispatch it could not write, and `run` reports
   the GROWTH of that count once per cycle, beside the `record:` and `state:`
   lines it already prints. Two ordinary paths reach it: a dispatch with no agent
   id to file it under, which invariant 11 forbids inventing, and a chained write
   that failed. In both the mail goes out and only the trail is short, which is
   the whole reason neither is visible from anywhere else.

   `internal/record` has called that counter "surfaced rather than hidden" since
   the day it was written, and until 2026-08-03 nothing outside its own tests
   read it. A field documented as surfaced and printed nowhere is worse than an
   absent one, because it reads as a check somebody is already doing.

   The growth, not the standing total. The counter is cumulative for the life of
   the process and this runs on every poll, so the total would put the same line
   in the log every two seconds for a gap from an hour ago, and a log that
   repeats itself is one an operator stops reading.

   `--journal` deliberately does NOT carry the number. It is a separate
   invocation that reads the journal FILE, and a record that was never written
   leaves no trace in that file. Sourcing it from `state.json` instead would put
   a number that is not in the file into `record.Status`, and would print a
   confident zero after a fresh state volume on a box whose journal really is
   short: invariant 8 again.
   *(test: `TestAMessageSentWithoutARecordIsReported`,
   `TestAGapAlreadyReportedIsNotReportedAgainEveryPoll`; verified by moving the
   report below `--once`'s return, which fails the first, and by reporting the
   standing count in place of the growth, which fails the second)*

14. **The record plane READS this journal; nothing here sends it.** The seam to
   trailryx is the file and a read-only mount, in the same direction and by the
   same mechanism as invariants 6 and 9: heraldyx writes its own volume, another
   process reads it. `trailryx-node events --file` maps this envelope into
   records, and measured 2026-08-06 it reads a journal at mode 0444 and leaves
   it byte for byte identical, so the mount can be read-only on both sides of
   this process.

   The alternative shapes were weighed and both lose to a mount. A shipper
   inside this repository puts `net/http` into `go list ./...`, so it either
   fails invariant 3's gate or forces the gate to grow an exception, and an
   exception in the check that guards this component's one privilege is the
   whole argument traded for a hop. A shipper in another repository has nothing
   better to speak: trailryx's door for THIS envelope is a file, and its only
   network door is OTLP, so a shipper would map agent-event to span to record,
   two mappings where one exists, with the agent identity rebuilt from a span
   attribute on the way.

   What this process therefore owes the door is bytes, and four of the mapper's
   refusals are decided entirely here: the schema stamped, the timestamp
   formatted, the agent identifier carried whole, and the run identifier never
   invented. Breaking any of them leaves a journal that still reads, still
   chains and still passes every other test in this package, and surfaces as a
   count of zero records in a different repository.
   *(gate: `scripts/one-way-out.sh` for the half about not speaking, verified by
   adding `net/http` to `internal/record`, which fails it; test:
   `TestEveryRecordIsReadableAtTheRecordPlanesDoor`,
   `TestTheRecordCarriesTheIdentifiersWholeAndNotShortened`,
   `TestNoRunToNameIsRecordedAsNoRunRatherThanAnInventedOne`,
   `TestARecipientNeverReachesTheMetadataPlane`, each verified by breaking the
   implementation: a schema string trailryx does not accept, an RFC 1123
   timestamp, the agent id put
   through `truncate` the way the mail shortens it, a synthesised run id, and
   the recipients written into `on_behalf_of`)*

15. **heraldyx's OWN journal stays v0.2, and the reason is now more than a
    default.** agent-event v0.3 exists as of 2026-08-10 and is the version an
    observer stamps when `agent_id` carries a subject a PROCESS asserted about
    itself (agent-passport SPEC 3.3, 6.4). heraldyx asserts nothing of the kind:
    every `alert_sent` it writes is about a dispatch it performed itself, under
    the subject it read off an event somebody else established. Stamping v0.3
    would tell every reader a claim is possible in a stream where it is not, and
    trailryx refuses an unknown version, so the record plane would stop
    receiving this journal entirely.

    The break test in invariant 14 above used to plant "a v0.3 schema" as its
    example of a version nothing accepts. That example stopped being imaginary,
    so it now plants a string trailryx genuinely does not accept, which is what
    the case was always testing.
    *(test: `TestEveryRecordIsReadableAtTheRecordPlanesDoor`, which pins the
    schema heraldyx stamps)*

16. **A check must be able to tell "did not fail" from "did not run", and the
    gates here have been made to fail on purpose to prove they can.**
    `readme-numbers.sh` already refuses when its subject is absent, in four
    distinct ways: no test functions at all, no badge to compare against, no
    catalog it can count, and no table of event types it can find. The first
    two were true, were established by hand once in the session that wrote it,
    and nothing re-ran them. The other two arrived with that script's second
    number on 2026-08-25 and had a case each from the day they were written.

    `one-way-out.sh` is the shape that makes this an invariant rather than a
    chore. It reads `go list` output through a `case` statement, and a case
    that stops matching does not fail: it falls through, the loop finds nothing
    to complain about, and the script prints OK. A green result and an absent
    check are the same output.
    *(gate: `scripts/gates-have-teeth.sh`, 16 cases: eight real faults, four
    non-faults, and four subjects taken away. The non-faults are the ones
    worth keeping: `internal/deliver` speaking SMTP is the design this gate
    protects, a decision package may still use the standard library for pure
    work, rewording a catalog sentence changes what a mail SAYS and changes
    no number, and a test named in a scenario's prose is prose rather than a
    binding. A gate that flagged any of them would be deleted by whoever is
    unblocking CI.)*

    **What it does not cover.** It cannot test itself. It proves each gate
    catches the faults named in it, not every fault of that kind. It found no
    hole in either.

17. **A single poll never reads more than `maxBytesPerPoll` of one file's
    growth.** `pollOne` used to read from the held offset to the file's
    current end with no limit at all: a looping or compromised producer that
    appends faster than an operator can react gets its whole growth loaded
    into one buffer, and the process that exists to say something is wrong is
    the one an out-of-memory kill silences at that exact moment. The cap is a
    package constant (`internal/watch/watch.go`, a few MiB), the offset only
    ever advances past whole lines actually consumed within the capped read,
    and what does not fit in one poll is read on the next one rather than
    lost. Two consequences are stated rather than implied. "Delayed, not
    lost" holds while a plane's sustained rate stays under one cap per poll
    interval (4 MiB per 2 s by default, 2 MiB/s per file); above it the lag
    grows for as long as the burst lasts and a rule sees each event at the
    wall-clock moment it is processed. And a single completed line longer
    than the cap can never be delivered: holding the offset before it would
    read its first cap forever and freeze the file, the OOM turned into a
    silent per-file stall, so it is skipped (the offset moves to the newline
    that ends it, read in cap-sized pieces) and counted as `Oversized`.
    `Watcher.Capped` and `Watcher.Oversized` sit beside `Malformed` and
    `Truncations`, and `cmd/heraldyx` prints their growth once per poll
    (`sayVolume`), the way `sayUnrecorded` prints the journal's, because a
    counter nobody prints is a field an operator cannot act on (invariant 13).
    *(test: `TestAFileGrowingPastTheCapIsReadInPieces`,
    `TestABurstUnderTheCapIsReadWholeInOnePoll`,
    `TestALineLongerThanTheCapIsSkippedAndTheFileKeepsFlowing`)*

18. **The ceiling never goes quiet, and a `critical` goes through it.** The
    ceiling is a limit on how much mail an hour carries, and it stays exactly
    that: `HERALDYX_MAX_PER_HOUR` messages, dedup first, unchanged. What it
    must never become is silence. While it holds alerts back, a summary goes
    out every ten minutes naming the true count since the previous summary,
    when the first of them was held, and the worst severity among them;
    sooner once fifty have been held since the previous one, and never
    inside a minute of it, so the summary cannot become the flood it warns
    about (`rule.DefaultCadence`, a promise of this process rather than a
    setting). A `critical` is not held at all: it is sent at once, one
    message per condition, with the dedup window still applying, and it still
    counts toward the hour. And the log line at the ceiling says all of this,
    once per summary period rather than once per held event.

    Measured the other way on an appliance run on 2026-09-17 (issue #71): the
    ceiling was reached at 12:11Z with one summary saying 8 were held, and
    over the next 28 minutes a `budget_exhausted` at critical, ten highs and
    about twenty mediums were read, held, and never mentioned, because the
    summary was rate limited to one an hour, the same width as the ceiling,
    and the critical was held with the rest. Reproduced against the unfixed
    binary on 2026-09-18 with 25 distinct highs and then the issue's second
    poll: 21 records, `suppressed_since: 31`, the critical among the 31, and
    nothing in the log.

    The consequence stated rather than implied: a fleet raising DISTINCT
    criticals at volume is bounded by dedup alone, since a critical bypasses
    the only other limit. That is the ask, and it is the right trade for a
    severity whose meaning is "now"; a producer that raises `critical` for
    something that is not is the defect in that case, and it is not one this
    process can tell from a real one.
    *(test: `TestACriticalIsSentThroughTheCeiling`,
    `TestHeldAlertsAreSummarisedAgainWithinABoundedInterval`,
    `TestTheSummaryNamesTheWorstSeverityHeld`,
    `TestALargeBurstIsSummarisedSoonerThanTheInterval`,
    `TestTheLogSaysWhatHappensNextAtTheCeiling`,
    `TestACriticalBypassesTheCeilingAndDedupStillHolds`,
    `TestTheSummaryCadenceHasBothBoundsAndAFloor`; every one run red against
    the unfixed code first, and each verified against the fixed code by the
    mutation that puts the defect back, recorded in `VALIDATION.md`;
    scenarios: `features/ceiling.feature`, each bound to a named test both
    ways, held by `scripts/features-are-bound.sh` with four cases in
    `gates-have-teeth.sh`)*

19. **Run-stalled rendering reports observations and validates numeric timing.**

@codex 2026-09-19: `run_stalled` describes an observation, never a diagnosed
node or provider failure. `last_call_millis` and `silence_ms` come from
TokenFuse Cloud's numeric call history (`store.rs::tick_stalls`); only
non-negative integer numbers are rendered, as UTC time and seconds. Text,
non-finite, fractional and overflowing values are omitted. These keys are
excluded from generic raw data rendering.
(gate: `./scripts/features-are-bound.sh && go test ./internal/render -run TestRunStalled`; tests: `TestRunStalledNamesTheObservationWithoutDiagnosingTheCause`,
`TestRunStalledRejectsInvalidNumbersWithoutRenderingContent`.)

20. **A failed call is not evidence of a refund.** @codex 2026-09-19:
    `dependency_failed` at send or an unknown stage does not establish whether
    the provider accepted the request. Its wording directs the operator to the
    run's charge and reservation. A buffered body failure states the successful
    response's usage-or-estimate rule and the refusal's usage-only rule.
    (gate: `./scripts/features-are-bound.sh && go test ./internal/render`;
    tests: `TestAFailedCallDoesNotPromiseARefund`,
    `TestABufferedBodyFailureNamesThePossibleCharge`.)

21. **typryx's four event types are described honestly, a refusal names
    typryx's own reason in plain words, and a calibration drift is reported
    as a measurement and never as an enforcement.** typryx is an optional
    add-on (agent-passport SPEC.md 6.2); `@decided 2026-09-26`: its
    journal moves onto the shared bus in all three launchers, so
    `typed_answer` (info), `typed_unanswered` (medium), `typed_refused`
    (high) and `calibration_drift` (high) can now reach this floor.
    Severities are typryx's own and this file does not choose or change
    them. `typed_refused` branches on `data.reason` (typryx's own codes:
    `over_hourly_cap`, `over_daily_spend_cap`, `unknown_template`,
    `freeform_disabled`, `bad_state`, `state_too_large`, `bad_question`; a
    caller's `bad_run_id` is refused at typryx's own API/MCP boundary and
    never reaches this bus), because a cap the operator configured on
    purpose and a caller sending a malformed question want opposite
    responses; a reason this build has not learned yet keeps the neutral
    base sentence rather than guessing, the same fallback `dependency_failed`
    and `slo_burn` already use. `calibration_drift` names the template, its
    version, the backend, the model and which of Brier score or ECE crossed
    its bound, off the event's own `data.bounds_crossed`, and says plainly
    that nothing about the deployment changed: typryx's own invariant is that
    a calibration verdict is reported and never turned into a cap change or a
    refusal.
    *(test: `TestEveryTypryxTypeIsDescribed`, `TestTypedAnswerIsDescribedAndNamesItsTemplateAndBackend`,
    `TestTypedUnansweredIsDescribed`, `TestTypedRefusedNamesEachReasonPlainly`,
    `TestTypedRefusedUnknownReasonStaysNeutral`,
    `TestCalibrationDriftNamesTheGroupAndTheMetricThatCrossed`,
    `TestCalibrationDriftNamesBothMetricsWhenBothCross`,
    `TestCalibrationDriftRejectsHostileBoundsCrossed`,
    `TestTypedAnswerUnsafeTemplateOrBackendIsDropped`,
    `TestTypryxSeveritiesAreNotChangedByThisFile`, all in `internal/render`;
    scenarios: `features/typryx-catalog.feature`, each bound to a named test
    both ways, held by `scripts/features-are-bound.sh`)*

22. **An event is processed as the source it claims only when the file it was
    read from may carry that source.** `@decided 2026-10-04`: on the shared
    event bus each writer writes only its own stream and everyone else reads,
    plus a verifier on the box; this is the reader half of that decision for
    heraldyx. `@claude 2026-10-04, delegated by the owner`: the shape below.

    The bus is one directory with one file per writer, and every plane can
    append to every file, so the `source` inside a line was the one place a
    co-tenant could speak in another plane's name: a line claiming
    `source: wardryx` inside `tokenfuse.ndjson` was rendered and mailed as a
    wardryx event. `watch.Watcher` now checks each parsed line against the file
    it came from (`internal/stream`, a pure table and a pure decision) before
    anything else sees it. A refused line is never returned by `Poll`, so the
    rule, the renderer, the fleet picture and the digest cannot process it as
    the source it named. It is counted as `Watcher.ForeignSource` beside
    `Malformed`, printed once per growth (`sayForeign`), and raised as ONE alert
    per (file, claimed source) pair, `high`, through `rule.DecideKey`, which is
    the same floor, dedup window and ceiling every event passes (below the
    floor it lands in the digest, not in the void). The pair is remembered in
    the state file (`Snapshot.Raised`, bounded at 256, oldest out), so a
    restart does not mail it again. The alert names the file, the claim, the
    count and what the file may carry, and carries nothing the refused lines
    said: the claimed source is producer-written text and is bounded and
    escaped like every other id here.

    What a file may carry is the convention (`<source>.ndjson` carries
    `<source>`, for the fifteen sources agent-passport SPEC 6.2 registers,
    `agent-conform` among them), plus a small table of the measured exceptions
    in `internal/stream`, plus
    what the operator declares in `HERALDYX_STREAMS` (`stem=source|source`,
    comma-separated; widens and never narrows; a malformed entry refuses
    startup, because the rule fails closed and an ignored entry would read as a
    plane that stopped being heard). The table, `@claude 2026-10-04`, read from
    the producers' writer code, estate-gates C4's producer table and the three
    launchers: `tokenfuse-cloud` and `tokenfuse-mcp` (the control plane and the
    MCP broker append to their own files and stamp `tokenfuse`, through the one
    crate that builds the envelope), and `tokenfuse-openai` (`@claude
    2026-10-13`: a second gateway process on the OpenAI wire, from tokenfuse
    v1.11.0's `TOKENFUSE_WIRE` code, which runs beside the first and so keeps
    its own file; the same crate stamps `tokenfuse`). `demo` is deliberately NOT in the table
    (`@claude 2026-10-04`, from review): `taipan demo` writes one file
    attributed to six planes, but until each writer owns its own file the
    events directory is writable by every co-tenant, and a built-in row would
    let any of them create `demo.ndjson` and speak as any of the six. It is
    opt-in, declared with `HERALDYX_STREAMS=demo=tokenfuse|wardryx|engram|qryx|verdryx|mockryx`
    by the operator who runs `taipan demo` against this box (taipan starts no
    heraldyx, and starts idryx with an empty environment, so nothing there sets
    it); undeclared it is an unknown stream where only `source: demo` is read.
    The own-volume journals (`events.ndjson` for
    scopyx in two launchers, `sent.ndjson` for heraldyx) are not on the shared
    bus and are deliberately not in the table.

    A stream nothing declares is not trusted in silence and not dropped: a line
    whose source is the stream's own name is still read (a plane new to the box
    must not go deaf the day it ships), counted as `UnknownStream`, and raised
    once per file as a `high` alert saying the box does not know it; a line in
    it claiming any other source is refused like any other. A pair or a file
    raised is never raised twice, but the counters keep counting.

    Neither alert is filed under an agent, because it is about a file and this
    process never invents a subject (invariant 11), so `record.Journal` counts
    the missing record and `sayUnrecorded` says so, as for any agent-less
    dispatch. The mail goes either way.

    **What it does not cover.** A writer that is compromised in its own uid can
    still forge its own stream; only signing closes that. A process that
    creates `wardryx.ndjson` before wardryx does, and writes `source: wardryx`
    into it, passes this check, because the file name and the claim agree: that
    is closed by who may create files in the directory (launcher ownership) and
    by checking the owning uid of each stream file, neither of which is done
    here. The renamed-file table is a copy that heraldyx and idryx each carry
    and nothing holds them equal.
    *(test: `TestAForeignSourceIsNeverMailedAsThatSource`,
    `TestTheForeignSourceAlertNamesTheFileTheClaimAndTheCount`,
    `TestAForeignSourceIsRaisedOncePerFileAndClaimedSourceAcrossPolls`,
    `TestAForeignSourceIsNotRaisedAgainAfterARestart`,
    `TestADeclaredMultiSourceFileIsProcessedWholeAndRaisesNothing`,
    `TestARenamedFileOfTheSameProducerIsProcessed`,
    `TestACoTenantsDemoFileIsRefusedByDefault`,
    `TestAnUnknownStreamIsReadAndSaidOnceNotTrustedInSilence`,
    `TestHostileLinesAreUnchangedAndAHostileClaimCannotBreakTheMail`, all in
    `cmd/heraldyx/source_test.go`, and `TestAForeignSourceIsNeverReturnedAndIsCounted`
    in `internal/watch`; scenarios: `features/source-must-match-its-stream.feature`,
    each bound to a named test both ways, held by `scripts/features-are-bound.sh`;
    every test run red against the unfixed code first, and each verified by the
    mutation that puts the defect back, recorded in the pull request)*

23. **agent-conform's two event types name the stream and the line, a break
    is called evidence and never a verdict, and an unchained stream is not
    called a break.** agent-conform is the on-box hash-chain verifier
    (`agent-conform watch-dir`, agent-stack-go `cmd/agent-conform/watchdir.go`
    at v1.1.0), an optional add-on registered in agent-passport SPEC.md 6.2;
    `@claude 2026-10-04`, asked for by the estate audit plan of that day: its
    two types, `chain_broken` (high) and `chain_unchained` (low), are
    described here instead of mailing as the generic fallback, which named no
    stream and no line. Severities are the
    verifier's own and this file does not choose them. `chain_broken` names the
    stream file (in the subject and the body), the first broken line, how many
    breaks the file holds and the kind, and says what a break is evidence of:
    the stream was edited after it was written, or two writers interleaved in
    it, and the verifier does not say which or who; it sends the operator to the
    verifier's own stream and to the pod or service log of what runs it.
    `chain_unchained` names the stream and the event count, says it is not a
    break (`prev_hash` is optional) and says what it costs: an edit to that
    stream would not be detected. The shape below is `@claude 2026-10-04`.

    The four data keys it prints, `file`, `line`, `breaks` and `events`, are
    deliberately NOT in `dataAllowlist`: `events` is a key verdryx's `slo_burn`
    carries and `TestTheSLOFieldsAreNotMailedRaw` holds it out, and a `file` or
    `line` another plane carries must stay unrendered. `conformFacts` in
    `internal/render` reads them for these two types only, each through its own
    check (`streamFile` for the name, `wholeCount` for the numbers), so one
    hostile field costs only itself; `kind` goes through the existing
    `safeString`. The two clipped hash strings (`expected`, `found`) and the
    rest of the verifier's fields are never read. A file name that is present
    and not printable is said out loud, never left as a gap.

    **What it does not cover.** Every alert the verifier writes shares one
    subject (its own agent id, no run id) and the dedup key is the type and the
    subject, so a second broken stream reported inside one dedup window is
    dropped, not mailed (@measured `rule.Decide`, two `chain_broken` events for
    different files 30 seconds apart: `notify`, then `drop`, 2026-10-04). That
    is a property of the key and is not changed here. Nothing here can check
    that the wording is true of the verifier beyond reading its code and SPEC
    6.2.
    *(test: `TestEveryAgentConformTypeIsDescribed`,
    `TestChainBrokenNamesTheStreamTheFirstBrokenLineAndTheBreakCount`,
    `TestChainBrokenCountsASingleBreakInTheSingular`,
    `TestChainBrokenSaysWhatItIsEvidenceOfAndWhatToCheck`,
    `TestChainBrokenWithHostileValuesStaysSafe`,
    `TestChainBrokenSaysWhenTheFileNameCannotBePrinted`,
    `TestChainBrokenKindIsRenderedOnlyThroughTheShapeCheck`,
    `TestChainUnchainedNamesTheStreamAndTheEventCountAndSaysItIsNotABreak`,
    `TestChainUnchainedWithHostileValuesStaysSafe`,
    `TestFileLineBreaksAndEventsAreRenderedOnlyForAgentConformTypes`,
    `TestAgentConformKeysAreNotInTheGenericAllowlist`,
    `TestAgentConformSeveritiesAreNotChangedByThisFile`, all in
    `internal/render/conform_test.go`; scenarios:
    `features/agent-conform-catalog.feature`, 12 scenarios, each bound to a
    named test both ways, held by `scripts/features-are-bound.sh`; every test
    run red against the unfixed code first, and each verified by the mutation
    that puts the defect back, recorded in the pull request)*

24. **tokenfuse's two retained types say what is held and who must act, a
    closure says what was settled and where the person is recorded, and a
    closure's evidence reference never reaches a mail, in any form.** tokenfuse retains a call it handed to the provider
    when the call ended before any answer came back: the money stays reserved,
    neither settled nor released, until a person closes it with evidence
    (tokenfuse invariants 50, 88, 89 and 90; agent-passport SPEC.md 6.2 as
    agent-passport#74 registers the two types). `@decided 2026-10-12`: both
    events go on the bus so a person is told; `call_retained` (medium) asks a
    person to act soon, `retained_closed` (high) is high because it lifts a
    money control. Severities are tokenfuse's own and this file does not choose
    them. The wording is `@claude 2026-10-12`, read against those three
    invariants and `crates/gateway/src/closure.rs` at tokenfuse `cb15dc9`.

    tokenfuse emits both types from v1.10.0 (commit `46cf8a5`, tokenfuse
    invariant 90, its D10). `@claude 2026-10-14`, read at tokenfuse origin/main
    `dc45172` with `git grep -n 'RetainedClosed\|CallRetained' -- crates/gateway/src`:
    `call_retained` is written by `report_retained` in
    `crates/gateway/src/settle.rs`, once per reservation the guard retains, and
    `retained_closed` by `closure::apply` in `crates/gateway/src/closure.rs`,
    once per closure applied, a replay excluded. Both go through `events.emit`
    with source `tokenfuse` (`crates/core/src/agent_event.rs:42`). On this side
    the two types are accepted from any stream file that may carry `tokenfuse`
    (`internal/stream`) and have catalog entries (`internal/render/render.go`,
    `call_retained` and `retained_closed`).

    `call_retained` puts the amount held into the subject (`usdAmount`: an
    amount above zero and under a cent is written to the micro-dollar, never
    `$0.00`) and names the reservation and the model; `journaled` and `listed`
    choose a sentence (a restart keeps it or forgets it; past the list's cap it
    cannot be closed from the console) only when each is a boolean, and neither
    is ever printed. `retained_closed` names the reservation, the evidence kind
    in this file's own words (`observed`, `no charge`; any other kind keeps the
    neutral sentence and is never printed) and the amount settled, and says
    that who closed it is not in the event and is in the console's own
    `console.close_retained` record, joined by the reservation id.

    `@decided 2026-10-12`: of a retained reservation's data, the mail may show
    only the three numeric keys, `reservation_id`, `amount_microusd` and
    `settled_microusd`, and the allowlist grows by those three and nothing
    else. `evidence_ref` is never mailed, whatever it holds: it is free text
    typed in the console, and tokenfuse's closure rule lets a model propose a
    closure together with its evidence for a person to accept, so even a
    reference shaped like a plain invoice number may be text a model wrote.
    Sanitising or cutting it would still be mailing it. A closure's mail says
    instead, in one fixed sentence that reads nothing from the event
    (`evidenceWhere`), that the reference is in the operator console with the
    record of the `console.close_retained` command. The three numbers are in
    `typeSpecificOnly`, so the generic per-key line never prints them for any
    other type, and the retained path reads them through `allowlisted`, so
    taking a key out of the allowlist takes its value out of the mail.
    `evidence_ref`, `journaled`, `listed`, `evidence_kind`, `revision`,
    `operation_id` and `unit` stay out of the allowlist. The model, which on a
    retained call is the one the agent asked for, and a string reservation id
    are held to `tokenShape` (no space) and `linkFree` (no scheme, no `www.`,
    invariant 2); a numeric id or amount must be a whole number from 0 to 2^53
    (`wholeCount`).

    **What it does not cover.** The dedup key is the type and the run, so a
    second retained call or a second closure on the same run inside one dedup
    window is dropped, not mailed (@measured `./bin/heraldyx --once` over two
    `call_retained` and two `retained_closed` events on `run-42`, 2026-10-12:
    two mails, the first of each); that is a property of the key and is not
    changed here. A model name that is a bare host and path, with no scheme and
    no `www.`, is shown, and some clients turn it into a link. No event a
    real tokenfuse wrote has been rendered, because tokenfuse does not write
    either type yet.
    *(test: `TestEveryRetainedTypeIsDescribed`,
    `TestCallRetainedSaysTheCallWasSentAndNeverAnsweredAndWhatStaysReserved`,
    `TestCallRetainedNotJournaledSaysARestartForgetsIt`,
    `TestCallRetainedNotListedSaysItCannotBeClosedFromTheConsole`,
    `TestCallRetainedWithoutJournaledOrListedClaimsNeither`,
    `TestASubCentReservationIsWrittenOutExactly`,
    `TestRetainedClosedObservedNamesTheReservationTheEvidenceAndTheAmount`,
    `TestRetainedClosedNoChargeSaysNothingWasChargedAndTheHoldWasReleased`,
    `TestRetainedClosedUnknownEvidenceKindStaysNeutral`,
    `TestTheEvidenceRefIsNeverMailed`,
    `TestHostileModelAndReservationIDNeverReachTheMail`,
    `TestHostileAmountsAreNeverRendered`,
    `TestRetainedKeysAreRenderedOnlyForTheRetainedTypes`,
    `TestOnlyTheRetainedKeysTheSentencesPrintJoinTheAllowlist`,
    `TestRetainedValuesReachTheMailOnlyThroughTheAllowlist`,
    `TestTheCatalogDescribesSixtyTypes`, all in
    `internal/render/retained_test.go`; scenarios:
    `features/tokenfuse-retained-catalog.feature`, 16 scenarios, each bound to
    a named test both ways, held by `scripts/features-are-bound.sh`; three
    cases in `scripts/gates-have-teeth.sh`; every test but one run red against
    the unfixed code first, the one being the guard
    `TestRetainedKeysAreRenderedOnlyForTheRetainedTypes`, and
    `TestTheEvidenceRefIsNeverMailed` run red against the earlier version of
    this change that printed a sanitised reference; fifteen mutants each
    caught by a named test, recorded in the pull request)*

25. **A read position is given up only when the file is shown to be a
    different file, and a span a writer's bytes have not reached yet is
    waited for, not parsed.** On Kubernetes the event log sits on a Longhorn
    RWX volume, which is NFS, and on a GCP cluster on 2026-10-14 a reader saw
    stale file sizes for about two minutes (stack-k8s GOTCHAS 24 and 70 are
    the same property). Until this invariant, a size smaller than the read
    position was taken as a truncation and the whole file was read again,
    which mails every alert in it a second time (bounded only by the dedup
    window and the hourly ceiling); a restart onto a node whose view is behind
    is the ordinary way to get there. And a line of zero bytes, which is what
    a reader sees where an earlier write has not reached the server while a
    later one has, was counted malformed and its events were lost for good.
    The shape below is `@claude 2026-10-14`, asked for by that run's findings.

    Which file a position belongs to is a digest of the file's first line
    (newline included, bounded at 4 KiB), kept in the state file beside the
    offset (`Snapshot.Heads`) and learned again on the next read when absent.
    Not the inode and device: the device number differs per mount, so a pod
    that moves node would see every file as new, and an inode survives a
    truncate-and-rewrite in place and is reused after a delete. The first
    line is read through the data path, which on NFS is what the server holds,
    and every event line carries its own timestamp and identifiers. A first
    line that is incomplete or has a zero byte in it says nothing, and is
    never taken as a change. A first line that changed is a different file,
    read from its start at once, even when it is already longer than the old
    position (`Truncations`). A size smaller than the position is checked
    against the data: if the byte just before the position is there, the size
    was old and reading goes on from the position; if not, the position is
    held (`Shorter`, once per stretch) until the file reaches it again. A
    stretch that lasts `watch.HoldWindow` (five minutes) is given up on and
    the file read again from its start (`ShorterExpired`): its writer may have
    lost the tail this process read and carried on below it, and waiting
    longer would stop reading that file in silence.

    A line with a zero byte in it (no event can hold one raw) is never parsed:
    reading stops before it, keeping the events before it, and looks again
    each poll (`ZeroHeld`, once per line held), including inside a line longer
    than the per-poll cap. A line still zero after `HoldWindow`, counted from
    when that line was first held, is skipped and counted (`ZeroSkipped`).
    `cmd/heraldyx` prints the growth of all five counters once per poll
    (`sayHolds`), so a hold reads as a hold and not as a plane gone quiet.

    **What it does not cover.** The window is measured in memory, so a
    process restarted during a hold starts it again (no launcher runs this
    with `--once`). A file cut back to a length that keeps its first line is
    indistinguishable from a lagging one until the window ends. A co-tenant of
    the bus that can append to a stream can delay that stream by up to the
    window per zero byte line it writes; it could already destroy events by
    interleaving partial lines, and invariant 22 records why each writer
    should own its file. A skipped zero line is said in the log, not mailed.
    Nothing here was run against a real NFS volume: the stale size and the
    zero span are reproduced with a stat seam, truncation and zero bytes on a
    local file system.
    *(test: `TestAFileThatLooksShorterButIsTheSameFileIsNotReadAgain`,
    `TestReadingResumesWhereItStoppedWhenTheFileCatchesUp`,
    `TestAStaleSizeWithTheDataThereIsReadThroughAtOnce`,
    `TestAQuietFileWithAStaleSizeIsNeverReadAgain`,
    `TestAFileReplacedByADifferentOneIsReadFromItsStart`,
    `TestAFileThatStaysShorterPastTheWindowIsReadAgainFromTheStart`,
    `TestEachStretchOfLookingShorterGetsItsOwnWindow`,
    `TestAWatcherRestoredWithItsHeadsTellsAReplacedFileAtOnce`,
    `TestSkipToEndRemembersEachFilesFirstLine`,
    `TestAZeroSpanOverTheFirstLineIsNotADifferentFile`,
    `TestALineWithAZeroByteIsHeldUntilItFillsIn`,
    `TestAZeroLineStillThereAfterTheWindowIsSkippedAndCounted`,
    `TestEachHeldLineGetsItsOwnWindow`,
    `TestAZeroSpanInALineLongerThanTheCapIsHeldNotSkipped`, all in
    `internal/watch`; `TestARestartOnALaggingVolumeDoesNotMailOldAlertsAgain`,
    `TestARestartRemembersWhichFileItWasReading`,
    `TestHoldsAndSkipsAreSaidOncePerGrowth` in `cmd/heraldyx`; scenarios:
    `features/a-lagging-volume.feature`, 12 scenarios, each bound to a named
    test both ways, held by `scripts/features-are-bound.sh`; every test but
    the guards run red against the unfixed code first, and each mutant that
    puts a defect back caught by a named test, recorded in the pull request)*

26. **A quiet box still says once a day that the notifier is alive, and the
    pulse never claims delivery.** `@decided 2026-10-15`: the box sends one
    pulse per `HERALDYX_PULSE_HOURS` (default 24, `0` turns it off), with the
    numbers this process saw, unless the digest went out in that period and
    carried it. Mail sent in between, alerts included, does not replace the
    pulse. A digest that carries the pulse is one mail, not two, and it
    restarts the period. The pulse says the message was handed to the mail
    transport, never that it was delivered (invariant 8). The counters live in
    `rule.State`, so they survive a restart (invariant 5). Its absence is the
    signal an operator is meant to notice, which is why a quiet fleet and a
    dead notifier must not look the same. The wording is `@claude 2026-10-15`.
    *(test: `TestAQuietBoxStillSaysItIsAliveOnceADay`,
    `TestThePulseRidesOnTheDigestRatherThanASecondMail`,
    `TestTheDigestRestartsThePulsePeriod`, `TestThePulseCanBeTurnedOff`,
    `TestThePulseSaysAliveInNumbersOnly`,
    `TestPulseIsDueAfterThePeriodAndCarriesTheCounts`,
    `TestPulseCountersSurviveARestart`; scenarios: `features/pulse.feature`,
    4 scenarios, each bound to a named test both ways, held by
    `scripts/features-are-bound.sh`)*

27. **Nothing holds a `critical`, and every alert an acknowledgement held is
    reported when the acknowledgement ends.** `@decided 2026-10-15`: an
    operator can acknowledge one condition, by its key, for a bounded time
    (four hours by default, seven days at most, longer refused). Alerts below
    critical about that key are held and counted rather than mailed. A
    `critical` about an acknowledged key is sent at once, with one line saying
    it was acknowledged. When the acknowledgement ends, or is removed, one mail
    says how many were held, the worst severity and when the first was held.
    The hold is a limit on messages, not on evidence (invariant 6): every event
    stays in the log. The hold does not count toward the hourly ceiling. Quiet
    hours and maintenance windows hold by the same rule, below critical only and
    reported when they end; invariant 29 covers a window's close. The wording is
    `@claude 2026-10-15`.
    *(test: `TestAnAckHoldsItsAlertsAndTheirCountIsMailedWhenItEnds`,
    `TestACriticalIsSentWhileItsKeyIsAcknowledged`,
    `TestAnAckNeverHoldsACritical`, `TestAnExpiredAckHoldsNothing`,
    `TestAnAckHoldsOnlyItsOwnKey`, `TestAckHeldAlertsDoNotCountTowardTheCeiling`,
    `TestTakeAckReleasesReportsHeldCountsWhenTheAckEnds`,
    `TestAckHeldCountsSurviveARestart`,
    `TestAnAckWithANegativeStartIsRefused` (a hand-edited start at or before
    zero, which lets the span overflow), `TestAHandEditedAckWithAFarFutureEndIsNotHonoured`
    (a hand-edited end more than seven days out),
    `TestValidateAckRefusesWhatMustNotBeHonoured`,
    `TestAnAckLongerThanSevenDaysIsRefused`, `TestAnUnackEndsTheHoldAtOnce`,
    and for quiet hours and maintenance windows (`@claude 2026-10-15`, from the
    same design) `TestQuietHoursHoldAndReportWhenTheyEnd`,
    `TestACriticalIsSentDuringMaintenance`,
    `TestAWindowHoldsBelowCriticalAndNeverACritical`,
    `TestAQuietHoldIsReleasedWhenTheWindowEnds`,
    `TestWindowHeldAlertsDoNotCountTowardTheCeiling`,
    `TestAScopedMaintenanceHoldsOnlyItsSource`;
    scenarios: `features/ack.feature`, five scenarios, and
    `features/hold-windows.feature`, twelve, bound both ways by
    `scripts/features-are-bound.sh`; the mutants that put each defect back are
    recorded in the pull request)*

28. **`control.json` has one writer, the command line, and `state.json` has one
    writer, the loop.** `@decided 2026-10-15`: the loop reads the
    acknowledgements file on every poll and never writes it. The loop rewrites
    `state.json` on every poll, so a command that wrote there would be erased
    on the next poll, which is why acknowledgements live in their own file.
    The wording is `@claude 2026-10-15`.
    *(partly gated: `TestTheLoopNeverWritesTheControlFile` holds the loop's
    half. Nothing checks that the command line never writes `state.json`, and
    nothing checks that only the loop does.)*

29. **A maintenance window says when it opens and always says when it closes,
    even with nothing held.** `@decided 2026-10-15`: a window the operator opens
    from the box is announced once when it opens, with what it holds and until
    when, and announced again when it closes, with the count it held. The close
    is sent whatever the count, because that mail is how the operator learns
    that alerting is back. The announcement is kept in the state file, so a
    restart inside a window does not announce it again, and a close owed across
    a restart is still sent. The wording is `@claude 2026-10-15`.
    *(test: `TestAMaintenanceWindowSaysWhenItOpensAndWhenItCloses`,
    `TestAMaintenanceWithNothingHeldStillSaysItClosed`,
    `TestAMaintenanceWindowOpensOnceAndAlwaysCloses`,
    `TestAMaintenanceCloseIsSentEvenWithNothingHeld`,
    `TestAMaintenanceOpenSaysWhatIsHeldAndThatACriticalIsNot`; scenarios:
    `features/hold-windows.feature`, bound both ways by
    `scripts/features-are-bound.sh`; the mutants that remove the open and the
    close are recorded in the pull request)*

30. **Mail leaves the box only over an encrypted connection, or in plaintext the operator named for the loopback interface.**
    `@decided 2026-10-17`: the transport requires the server to offer STARTTLS by default and verifies its certificate. Plaintext is used only when
    `HERALDYX_SMTP_TLS=plain` is set and the host is on the loopback interface; any other host refuses at startup. Every connection has a dial
    deadline and a session deadline, so a silent server cannot hold the notifier. The rule that port 465 means implicit TLS is `@claude 2026-10-17`,
    my reading of the port convention, not something the decision named.
    *(test: `TestAPlainRelayThatOffersNoSTARTTLSIsRefused`, `TestAStarttlsRelayIsReachedOnlyThroughAVerifiedCertificate`,
    `TestAStarttlsRelayReceivesTheMailOverVerifiedTLS`, `TestAnImplicitTLSRelayWithAnUntrustedCertificateGetsNoMail`,
    `TestPlaintextToANonLoopbackHostIsRefusedAtStartup`, `TestAStalledSessionIsCutOffAtTheDeadline`,
    `TestAHostThatNeverAcceptsIsGivenUpOnAtTheDialDeadline`, `TestPort465IsImplicitTLSByDefault`; gate: `scripts/smtp-tls-is-explicit.sh`, five cases
    in `scripts/gates-have-teeth.sh`; scenarios: `features/smtp-delivery.feature`, nine, each bound to a named test both ways)*

31. **keyryx's nine event types and agent-conform's `chain_restarted` are described, and keyryx's digests and free text never reach a mail.** `@decided 2026-10-17` is the wave's decision (the findings closed on the hybrid architecture, with keyryx as the custody and witness service); the wording is `@claude 2026-10-18`. keyryx writes `keyryx.ndjson` with source `keyryx` (agent-passport SPEC 6.2, reserved until its first release, `@decided 2026-10-18`). Its nine types, and agent-conform's `chain_restarted`, are in the catalog; the severities are the producers' own and are not chosen here. Their keys (`stream`, `tenant`, `lines`, `bytes`, `segments`, `name`, `kid`, `previous_kid`, `alg`, `file`, `line`) are read only by their own facts, through shape checks (`streamFile` for a stream, a strict identifier shape for the rest), and never through the generic allowlist. A witness's clipped digests are never read. *(test: `TestEveryKeyryxTypeIsDescribed`, `TestWitnessRewriteNamesTheStreamAndCountsNotHashes`, `TestKeyryxHostileValuesNeverReachTheMail`, `TestKeyryxFreeTextIsNeverMailed`, `TestKeyryxKeysAreNotInTheGenericAllowlist`, `TestChainRestartedNamesTheStreamAndLine`, `TestKeyRevokedNamesTheKeyAndWhatToCheck`, all in `internal/render/keyryx_test.go`; `TestKeyryxStreamMayCarryKeyryxOnly` in `cmd/heraldyx/checkpoint_test.go`; scenarios: `features/keyryx-catalog.feature`, bound both ways by `scripts/features-are-bound.sh`)*

32. **The checkpoint gate (F-018) is off unless switched on, and while it is on a stream is read only while a signed checkpoint covers it, with every refusal said in one mail per file.** `@decided 2026-10-17` is the wave's decision, which closes the finding that a bus reader cannot tell who wrote a line; the switch, its default and the stream-level coverage are `@claude 2026-10-18`. With `HERALDYX_CHECKPOINT_GATE=on`, `internal/checkpoint` verifies `<stream>.ckpt.ndjson` in the order agent-passport SPEC 6.5.1 gives: canonical form and listed members (step 1), the `prev` chain (step 3), the ES256 signature under a key pinned in `HERALDYX_CHECKPOINT_KEYS` (step 2), then the stream's first `count` lines against `prefix` and `head` (step 4). The newest valid signed record decides. A refused stream's lines are never returned by `Poll`, are counted in `NotCovered`, and raise one `high` notice per file (`uncovered_stream`) naming the file and the reason. The gate refuses to start without a checkpoint directory or with no key that loads. **Not checked here:** whether a pinned key was valid at `at` or has been revoked (keyryx's key statements), SPEC 6.5.1 step 5 for earlier records, and any line after the newest checkpoint's `count`; the witness holds those. Closing a co-tenant that can write both the checkpoint and the stream files is the launcher's directory ownership, outside this repository. *(test: `TestTheCheckpointGateIsOffUnlessSwitchedOn`, `TestAGateWithNoCheckpointDirRefusesStartup`, in `internal/config`; `TestAValidSignedCheckpointCoversItsStream`, `TestACheckpointFromAnUnpinnedKeyDoesNotCover`, `TestAForgedSignatureDoesNotCover`, `TestARewrittenStreamIsNotCoveredByItsCheckpoint`, `TestACheckpointForAnotherStreamDoesNotCover`, `TestAStreamShorterThanItsCheckpointIsNotCovered`, `TestATornFinalCheckpointLineIsIgnored`, `TestAnAlienCheckpointLineRefusesTheFile`, `TestACheckpointChainBreakRefusesTheFile`, `TestAnUnsignedRecordNeverCovers`, `TestCoverageIsRecomputedOnlyWhenTheCheckpointChanges`, `TestLoadKeysReadsAPinnedKey`, `TestAGateWithNoUsableKeysRefusesStartup`, in `internal/checkpoint`; `TestGateOffLeavesAnUncheckedStreamAsItWas`, `TestAStreamWithNoCheckpointIsRefusedAndSaidInAMail`, `TestARefusalIsRaisedOncePerFile`, in `cmd/heraldyx`; `TestAnUncoveredFileIsNeverReturnedAndIsCounted`, in `internal/watch`; scenarios: `features/checkpoint-gate.feature`, bound both ways by `scripts/features-are-bound.sh`)*

## Decisions that have no gate yet

This list is debt, and it is here to stay visible rather than to be tidy.

**Held by this file alone: invariant 6.**

The journal has still not been ingested by the record plane, and what blocks it
has changed. It is no longer transport. Until 2026-08-06 the recorded reason was
that trailryx's ingest is OTLP over HTTP with a protobuf body; that day trailryx
grew `trailryx-node events --file`, which reads this exact envelope from a file,
and invariant 14 is the seam that follows from it. ONE thing blocks it now, a
decision in trailryx rather than work here, measured against the real binary on
2026-08-06 (see `VALIDATION.md`).

RESOLVED 2026-08-06, trailryx PR #27: `alert_sent` now maps to
`EventType::NotificationDispatched`, an eleventh type appended at wire code 11.
It was NOT a format version under trailryx invariant 7: that invariant forbids
redefining a field in place, and an appended discriminant redefines nothing.
The same reading was already in that repo, where `SigAlg::Es384` sits at code 4,
appended after `SlhDsa` at 3, with the format version unchanged. Measured after
the change: two journal lines in, two `notification_dispatched` records written
and sealed, read back by a separate process with proof Full, and no recipient
address in any sealed file.

What remains:

- **`trailryx-node events` keeps no cursor.** Measured: the same file imported
  three times into one data directory produced three copies, its `duplicates`
  counter never firing. So the seam is a one-shot import today, and a scheduled
  one would duplicate the whole trail on every run, which is worse than not
  shipping it: a duplicated audit trail is one whose counts are wrong. A cursor
  belongs to the reader.

Do not work around it from this side. Trimming the journal so a re-import stays
small would make this process the thing that edits its own evidence.

- Invariant 6's holder is a deployment fact rather than anything in this
  repository: the event volume is mounted read-only in the cluster manifest and
  in the compose file. A checkout of this repo alone cannot enforce it, and the
  honest reading is that somebody editing a manifest could remove it without
  any test here noticing.
- The `catalog` in `internal/render` maps an event type to what it MEANS, and
  nothing can check that an entry is TRUE. The tests check that every mail has
  the two explanatory lines, not that those lines describe what the producing
  plane does.

  Audited against the producing code on 2026-08-03, for the first time. Four of
  seventeen entries were wrong, and each was wrong in a way that would send an
  operator somewhere useless:

  - `taint_block` said the call was blocked before it left the perimeter. The
    firewall evaluates the RESPONSE: the provider call went out and was paid
    for, and the answer was withheld from the agent.
  - `approval_requested` said a hold eventually times out. Nothing expires a
    hold in the policy plane.
  - `approval_timeout` said somebody waited for a decision that never came. It
    fires when an agent redeems an approval that HAD been granted and had
    expired.
  - `sim_finding` said production was not touched, which is a claim about the
    operator's setup that the event does not carry.

  `TestTheCatalogDoesNotRepeatTheFourClaimsThatWereFalse` pins those four
  phrases. It cannot pin truth. Read a new entry against the producing plane's
  own CODE, not its README, and prefer the fallback to a guess: "this build
  does not know" is honest, and a confident falsehood about somebody's own
  system is not.

  Since 2026-08-25 the catalog's SIZE is gated even though its TRUTH is not:
  `readme-numbers.sh` fails when the number of entries, the number the README
  claims and the number its table lists stop agreeing. That closes a smaller
  hole than it sounds like, and the distinction is worth keeping straight.
  `identity_finding` had been described in the code and documented nowhere
  since 2026-08-10, so an operator meeting it in a mail could read in the
  README that this build had no sentence for it. A gate can catch an entry
  nobody wrote down. Nothing can catch one that is written down and wrong.
  (`@claude`, the reading. `@measured` 2026-08-25: `./scripts/readme-numbers.sh`
  run with `README.md` and `internal/render/render.go` checked out at
  `origin/main` printed "the README says 18 event types have a sentence and
  internal/render's catalog holds 19" and exited 1.)
- The acknowledgement itself is logged by the loop (once per change in what
  is in force) and listed in the pulse, but it is NOT written to the dispatch
  journal. A journal record of an acknowledgement would be a new event type
  that trailryx refuses until it maps one, so the record waits for that
  mapping: a follow-up with trailryx, not a change here. The mail that reports
  what an acknowledgement held goes the way the other notices about a condition
  go: it names no agent, so the journal counts a gap and writes no record.

## Standing rule

An approved architecture decision is **not finished** until it is two things: a
numbered invariant in this file, and a gate in a script or a test if it can be
checked structurally. Until then it is a document, and documents do not stop
code.

## Escalate, do not push through

Stop and tell the user, then wait:

- Anything that would let this process take an action on an agent, or hold a
  credential for a plane. That is the boundary the whole design rests on.
- Adding a key to the `data` allowlist in `internal/render`. The question to
  answer first is not "is this useful" but "can this key ever hold text a model
  wrote".
- Any change to what the link in the mail points at.
- Enabling a hosted mail provider. Own or corporate SMTP costs nothing; SES,
  Postmark, Resend and friends are metered.

## Conventions

- **No long dashes** anywhere: not in code comments, docs, commit messages or
  PR bodies. Use a comma, a colon, parentheses, or a short hyphen.
- Nothing paid or metered gets enabled without telling the user first and
  getting agreement.
- Do not delete or revoke keys, tokens or certificates on your own initiative.
