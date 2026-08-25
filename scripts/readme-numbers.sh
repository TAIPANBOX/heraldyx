#!/usr/bin/env bash
# Every number this README states about this repository, checked against the
# repository.
#
# WHY THIS EXISTS
#
# A number on a README is a claim with no owner. It is right the day it is
# written and nothing tells anybody when it stops being right, because the
# suite grows in a commit that never opens the README.
#
# That is not hypothetical here. On 2026-08-05 the it-rat.com service pages were
# audited against the repositories they describe and FOUR OF SEVEN figures were
# stale: trailryx by 33 tests, tokenfuse by 196, engram by 42, verdryx by 75.
# None was wrong when written. The site now has a gate; this is the same idea at
# the source, where the number actually changes.
#
# WHAT "TESTS" MEANS HERE, because a number needs a definition more than it
# needs a badge
#
# `go test ./... -list '.*'` enumerates test FUNCTIONS. It does not count
# subtests created with `t.Run`, and it does not count table cases inside one
# function. So the figure is "test functions in this module", which is a real
# and checkable quantity, and it is deliberately not called "assertions" or
# "cases", both of which would be larger and neither of which anybody can
# reproduce.
#
# It also does not run them. This is a claim about how much test code exists,
# not about it passing: `go test -race ./...` in CI is what says they pass, and
# conflating the two would let a green badge mean a red suite.

set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

readme="README.md"
problems=0

note() {
	printf '%s\n' "$1"
	problems=$((problems + 1))
}

actual=$(go test ./... -list '.*' 2>/dev/null | grep -cE '^Test')
if [ "${actual:-0}" -eq 0 ]; then
	note "the module reported no test functions at all, which means this check measured nothing"
	exit 1
fi

stated=$(grep -o 'badge/tests-[0-9]*-' "$readme" | grep -o '[0-9]*' | head -1)
if [ -z "$stated" ]; then
	note "the README carries no tests badge, so this check has nothing to compare against"
	note "add: ![tests](https://img.shields.io/badge/tests-${actual}-brightgreen)"
	exit 1
fi

[ "$stated" = "$actual" ] ||
	note "the badge says $stated test functions and \`go test -list\` counts $actual"

# THE SECOND NUMBER: how many event types this build has a sentence for.
#
# It went stale the same way the badge does and nobody noticed for fifteen
# days. `identity_finding` was added to the catalog on 2026-08-10, in the
# commit that allowlisted `data.detector`, and the README's own list was not
# opened: the summary went on saying 18 while `internal/render` described 19.
#
# It is worth checking for the same reason the badge is, and for one more. The
# badge is a size. This is a claim about what an operator will be TOLD, and a
# type in the catalog that nobody documented is one they only meet at three in
# the morning, in a mail about a thing the README says this build does not know.
#
# Three quantities, not two: what the code describes, what the table lists, and
# what the sentence claims. Comparing only the first and the last would pass a
# README whose prose was updated and whose table was not, which is the shape
# this drift actually had.
described=$(awk '/^var catalog = map\[string\]phrasing\{/,/^\}$/' internal/render/render.go |
	grep -cE '^	"[a-z_]+": \{')
if [ "${described:-0}" -eq 0 ]; then
	note "internal/render's catalog reported no entries at all, so this check measured nothing"
	exit 1
fi

claimed=$(grep -oE '^<summary><b>The [0-9]+ event types' "$readme" | grep -oE '[0-9]+' | head -1)
if [ -z "$claimed" ]; then
	note "the README no longer says how many event types have a sentence, so this check has nothing to compare against"
	exit 1
fi

# Anchored on the catalog table's own header and stopped at the blank line
# after it, rather than counting every table row between <summary> and
# </details>. There is a second table in that block explaining what the mail
# says for each effect of `dependency_failed`, and a `grep` over the whole
# section would count a row of it the day one begins with a backticked word.
# A miscount here is worse than no count: it fails a correct README and gets
# the check deleted by whoever is unblocking CI.
listed=$(awk '
	/^\| type \| the mail/ { t = 1; next }
	t && /^\|---/ { next }
	t && /^\|/ { n++; next }
	t { exit }
	END { print n + 0 }' "$readme")
if [ "$listed" -eq 0 ]; then
	note "the README's table of event types has no rows this check can find, so it measured nothing"
	exit 1
fi

[ "$claimed" = "$described" ] ||
	note "the README says $claimed event types have a sentence and internal/render's catalog holds $described"
[ "$listed" = "$described" ] ||
	note "the README's table lists $listed event types and internal/render's catalog holds $described"

if [ "$problems" -gt 0 ]; then
	printf '\n%d number(s) the README states that this repository does not support.\n' "$problems"
	printf 'Update the badge in the same commit as the tests. That is the whole point:\n'
	printf 'the suite changes in a commit that never opens the README, and this is what\n'
	printf 'makes that impossible.\n'
	exit 1
fi

printf '%s test functions, and the badge says so.\n' "$actual"
printf '%s event types with a sentence, listed and counted in the README.\n' "$described"
