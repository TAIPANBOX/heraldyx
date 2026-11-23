#!/usr/bin/env bash
# Every scenario in features/ names a test that exists, and every scenario names
# one at all.
#
# WHY BOTH DIRECTIONS
#
# A scenario bound to nothing is a paragraph describing what somebody wanted,
# and it proves nothing about what the code does. A binding pointing at a test
# that has been renamed or deleted is worse than none: it reads as held, and a
# reader has no way to tell without grepping. Two different lies, and only one
# of them is visible from either side alone.
#
# WHY NOT A BDD RUNNER
#
# The ask the scenarios serve is readability: a person reads Given / When /
# Then instead of a diff. A runner would add a step-definition style to this
# repository for that, and the binding gate delivers the readable half at a
# fraction of the surface. The same decision agent-stack-go, tokenfuse and
# vouchryx made; this script is agent-stack-go's.
#
# WHAT THIS DOES NOT DO
#
# It does not check that a test ASSERTS what its scenario says, and nothing
# mechanical can: the steps are prose and the binding is a pointer. What it
# catches is the pointer breaking, which is the failure that happens on its own
# while nobody is looking.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

if [ ! -d features ]; then
	echo "FAIL: features/ is gone, so this gate measured nothing" >&2
	exit 1
fi

scenarios=$(grep -ch '^  Scenario:' features/*.feature 2>/dev/null | awk '{s += $1} END {print s + 0}')
bindings=$(grep -ch '^  # @test:' features/*.feature 2>/dev/null | awk '{s += $1} END {print s + 0}')

if [ "$scenarios" -eq 0 ]; then
	# A subject that has gone away must say so rather than report OK on
	# nothing. This is the property CLAUDE.md invariant 16 is about.
	echo "FAIL: no scenarios found in features/, so this gate measured nothing" >&2
	exit 1
fi

broken=0
while read -r name; do
	[ -n "$name" ] || continue
	if ! grep -rq "func ${name}(" --include='*_test.go' .; then
		echo "FAIL: features name ${name} and no such test exists" >&2
		broken=$((broken + 1))
	fi
done < <(grep -h '^  # @test:' features/*.feature | sed 's/.*@test://')

# WHERE THE BINDING SITS
#
# A binding is the line right after its scenario's last step, and nowhere else.
# Counting alone let both conventions live side by side (above the scenario in
# three files, below it in thirteen), and a reader could not tell which
# scenario a pointer belonged to: a binding above scenario B is also a binding
# below scenario A. Two missing bindings and two misplaced ones still count
# equal. So each scenario must end in its binding, and a binding anywhere else
# (above a scenario, after a blank line, after another binding) is attached to
# nothing. One position, below the steps, the one most files already used
# (2026-11-23).
while IFS= read -r problem; do
	[ -n "$problem" ] || continue
	echo "FAIL: ${problem}" >&2
	broken=$((broken + 1))
done < <(awk '
	function close_scenario() {
		if (open && !bound) printf "%s:%d: scenario has no binding on the line after its last step\n", file, start
		open = 0
	}
	FNR == 1 { if (NR > 1) close_scenario(); prev = "" }
	/^  Scenario:/ { close_scenario(); open = 1; bound = 0; steps = 0; start = FNR; file = FILENAME; prev = "scenario"; next }
	/^    (Given|When|Then|And|But) / {
		if (open && !bound) { steps++; prev = "step"; next }
		printf "%s:%d: a step outside any scenario\n", FILENAME, FNR
		prev = "other"; next
	}
	/^  # @test:/ {
		if (open && !bound && steps > 0 && prev == "step") { bound = 1; open = 0; prev = "binding"; next }
		printf "%s:%d: binding is not attached to a scenario (it must be the line right after the last step)\n", FILENAME, FNR
		prev = "other"; next
	}
	{ if (open && steps > 0) close_scenario(); prev = "other" }
	END { close_scenario() }
' features/*.feature)

if [ "$scenarios" != "$bindings" ]; then
	echo "FAIL: ${scenarios} scenario(s) and ${bindings} binding(s)" >&2
	broken=$((broken + 1))
fi

[ "$broken" -eq 0 ] || exit 1
echo "features: ${scenarios} scenarios, ${bindings} bindings, 0 broken"
