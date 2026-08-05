#!/usr/bin/env bash
#
# One way out.
#
# heraldyx is the component of the box that is allowed to open a connection to
# something outside it. That privilege is why it is a separate process with its
# own network policy, and the whole argument only holds while the privilege
# stays in ONE package.
#
# This gate fails when:
#   - anything other than internal/deliver imports net/smtp;
#   - anything at all imports net/http (this process calls no API, ever: it
#     reads a file and sends mail, and an HTTP client here would be the first
#     step toward it becoming a client of the planes it watches);
#   - internal/render, internal/rule or internal/fleet reach for the network
#     or the filesystem. The first two decide WHAT to say and WHETHER to say
#     it; internal/fleet builds the "around it right now" context from the
#     same event log, and README.md and docs/assets/one-way-out.svg have
#     always described it as part of this same no-I/O tier. A layer that
#     builds what goes in a message and can also perform I/O is a layer that
#     will eventually perform it from inside a branch nobody tested.
#
# Imports of test files are not considered: a test may open whatever it needs.
set -euo pipefail

cd "$(dirname "$0")/.."

fail=0

banned_for_decision=(os os/exec net net/smtp net/http net/mail io/ioutil)

while IFS= read -r line; do
  pkg="${line%%::*}"
  imports="${line#*::}"

  for imp in $imports; do
    if [ "$imp" = "net/smtp" ] && [ "$pkg" != "github.com/TAIPANBOX/heraldyx/internal/deliver" ]; then
      echo "FAIL: $pkg imports net/smtp; only internal/deliver may speak SMTP"
      fail=1
    fi
    if [ "$imp" = "net/http" ]; then
      echo "FAIL: $pkg imports net/http; this process is not a client of anything"
      fail=1
    fi
    case "$pkg" in
      github.com/TAIPANBOX/heraldyx/internal/render|github.com/TAIPANBOX/heraldyx/internal/rule|github.com/TAIPANBOX/heraldyx/internal/fleet)
        for b in "${banned_for_decision[@]}"; do
          if [ "$imp" = "$b" ]; then
            echo "FAIL: $pkg imports $b; rule, render and fleet do no I/O"
            fail=1
          fi
        done
        ;;
    esac
  done
done < <(go list -f '{{.ImportPath}}::{{join .Imports " "}}' ./...)

if [ "$fail" -ne 0 ]; then
  exit 1
fi

echo "OK: SMTP lives in internal/deliver only, nothing speaks HTTP, rule, render and fleet do no I/O."
