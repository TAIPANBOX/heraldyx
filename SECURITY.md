# Security Policy

heraldyx watches the NDJSON event log the TAIPANBOX agent-governance stack
writes, decides which events a human should hear about now rather than
tomorrow, and mails them, and it is deliberately the one component of that
stack allowed to reach the outside world, so its blast radius has to stay
small enough to state in one sentence.

## Reporting a vulnerability

Please report security issues privately, not in public issues or pull
requests: open a GitHub private security advisory at
<https://github.com/TAIPANBOX/heraldyx/security/advisories/new>. Include the
affected version or commit, a description and a minimal reproduction. We aim
to acknowledge within a few days and to fix high-severity issues before any
public disclosure, with coordinated disclosure within 90 days of the report.
There is no bug-bounty programme; reporters are credited in the advisory
unless they prefer otherwise.

## Supported versions

Before this repository's 1.0, only `main` is supported: fixes land on `main`
and are not backported. From its 1.0 tag, the newest minor gets every fix and
the previous minor gets security-relevant fixes for 90 days after the newer
one is tagged.

## Verifying a build

Every change passes the repository's gates before merge: `gofmt -l .`,
`go vet ./...`, `go test -race ./...`, `./scripts/one-way-out.sh`,
`./scripts/readme-numbers.sh` and `./scripts/gates-have-teeth.sh`, plus
`staticcheck ./...`, `govulncheck ./...` and `gosec -quiet ./...` in CI.
Release assets are signed keyless with Sigstore and carry a provenance
attestation and an SBOM; the README's verify block shows how to check them.
