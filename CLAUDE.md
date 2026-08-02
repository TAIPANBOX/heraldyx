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
5. **Both halves of the state survive a restart.** Read offsets AND dedup
   counters. A container restarts on every rollout; a process that forgets
   either one turns a quiet incident into a mailbox full of the same incident,
   which is how an operator learns to filter this sender to trash.
   *(test: `TestDedupSurvivesARestart`, `TestARestartRemembersBothHalves`,
   `TestOneEventBecomesOneMessage`'s second pass)*
6. **Every limit is a limit on MESSAGES, never on evidence.** Dedup, the
   ceiling and the digest decide what is said, and nothing else. heraldyx never
   suppresses, edits or delays an event in the log, because it cannot: it only
   reads. Any future feature that would need to write to the event log belongs
   in another component.
   *(not enforced; held by invariant 3's gate as a side effect, since writing
   would need I/O in a layer that has none)*
7. **A partial line is never consumed.** The writer on the other side is
   appending, and half an event parsed now is an event lost forever. The read
   offset only advances past a newline.
   *(test: `TestAPartialLineIsNotConsumed`)*

## Decisions that have no gate yet

This list is debt, and it is here to stay visible rather than to be tidy.

**Held by this file alone: invariant 6.**

- Invariant 6 is the one that would be worth a structural check the day this
  process gains any writing capability at all. Today it is held indirectly:
  the decision layer has no I/O imports, so there is nothing to write with.
  That is a side effect of another rule, and side effects are not gates.
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
