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
./scripts/readme-numbers.sh
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
   one step softer.** `@yurii 2026-08-10`, "прибери підпис". A CLAIMED subject
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
   *(test: `TestDedupSurvivesARestart`, `TestARestartRemembersBothHalves`,
   `TestOneEventBecomesOneMessage`'s second pass,
   `TestAPathOutOfSightForOnePollKeepsItsPlace`, and
   `TestAReplacedFileIsStillReadFromTheStart` for the case that makes keeping
   a position safe)*
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
   offset only advances past a newline.
   *(test: `TestAPartialLineIsNotConsumed`)*

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

16. **A check must be able to tell "did not fail" from "did not run", and both
    gates here have been made to fail on purpose to prove they can.**
    `readme-numbers.sh` already refuses when its subject is absent, in two
    distinct ways: no test functions at all, and no badge to compare against.
    Both sentences were true, both were established by hand once in the session
    that wrote it, and nothing re-ran them.

    `one-way-out.sh` is the shape that makes this an invariant rather than a
    chore. It reads `go list` output through a `case` statement, and a case
    that stops matching does not fail: it falls through, the loop finds nothing
    to complain about, and the script prints OK. A green result and an absent
    check are the same output.
    *(gate: `scripts/gates-have-teeth.sh`, 7 cases: four real faults, two
    non-faults, and one subject taken away. The non-faults are the ones worth
    keeping: `internal/deliver` speaking SMTP is the design this gate protects,
    and a decision package may still use the standard library for pure work. A
    gate that flagged either would be deleted by whoever is unblocking CI.)*

    **What it does not cover.** It cannot test itself. It proves each gate
    catches the faults named in it, not every fault of that kind. It found no
    hole in either.

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
- Quiet hours are designed (see `heraldyx-plan.md` section 5) and not built.
  Do not half-build them: a quiet window that silences a `critical` is a bug
  with a friendly name.

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
