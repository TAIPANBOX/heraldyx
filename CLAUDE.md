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
```

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
   *(test: `TestTheOnlyLinkIsAConsoleView`; verified by appending
   `?action=kill` to the link, which fails it)*
3. **One way out.** `net/smtp` is imported by `internal/deliver` and nothing
   else, nothing here imports `net/http` at all, and `internal/render` and
   `internal/rule` do no I/O of any kind. The decision layer that can also
   perform I/O is the one that eventually performs it from inside a branch
   nobody tested.
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

## Decisions that have no gate yet

This list is debt, and it is here to stay visible rather than to be tidy.

**Held by this file alone: invariant 6.**

Shipping the journal INTO the record plane (trailryx) is not done here and is
not an oversight. Its network ingest is OTLP over HTTP with a protobuf body,
and speaking it would mean an HTTP client and a protobuf encoder inside the one
process in the box with a way out, which is what invariant 3's gate exists to
prevent. This process produces a sealed, verifiable record; a component allowed
to make that hop ships it.

- Invariant 6's holder is a deployment fact rather than anything in this
  repository: the event volume is mounted read-only in the cluster manifest and
  in the compose file. A checkout of this repo alone cannot enforce it, and the
  honest reading is that somebody editing a manifest could remove it without
  any test here noticing.
- The `catalog` in `internal/render` maps an event type to what it MEANS. An
  entry that is wrong is not caught by anything: the tests check that every
  mail has the two explanatory lines, not that those lines are true of the
  type. Getting one wrong tells an operator a confident falsehood about their
  own system, which is worse than the fallback's honest "this build does not
  know". Check a new entry against the producing plane's own docs, and prefer
  the fallback to a guess.
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
