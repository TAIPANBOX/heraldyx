#!/usr/bin/env bash
#
# Every SMTP conversation goes through the one dialler in internal/deliver, and
# that dialler takes a TLS mode.
#
# The mode is what stands between an alert's content and the network in the
# clear, so this gate fails when:
#   - anything outside a comment calls smtp.SendMail or smtp.Dial, which open a
#     connection without the mode;
#   - anything outside a comment turns certificate verification off;
#   - internal/deliver stops requiring the STARTTLS offer before it sends.
# A comment that names one of those is prose, and is not counted.
#
# It refuses when its subject is gone. If the dialler is renamed or moved, the
# checks above would find nothing and report OK, so the gate says it measured
# nothing instead.
set -uo pipefail
cd "$(dirname "$0")/.."

src=internal/deliver/deliver.go
if [ ! -f "$src" ] || ! grep -q 'func (s \*SMTP) connect(' "$src"; then
	echo "FAIL: no SMTP dialler (func (s *SMTP) connect) in $src, so this gate measured nothing" >&2
	exit 1
fi

fail=0

# Code lines only: a line whose first non-blank characters are // is a comment.
code() { grep -vE '^[[:space:]]*//' "$1"; }

while IFS= read -r f; do
	if code "$f" | grep -qE 'smtp\.SendMail\(|smtp\.Dial\('; then
		echo "FAIL: $f calls smtp.SendMail or smtp.Dial, which bypass the TLS mode" >&2
		fail=1
	fi
	if code "$f" | grep -q 'InsecureSkipVerify'; then
		echo "FAIL: $f sets InsecureSkipVerify, so certificate verification is off" >&2
		fail=1
	fi
done < <(find internal cmd -name '*.go' ! -name '*_test.go' | sort)

if ! code "$src" | grep -q 'Extension("STARTTLS")'; then
	echo "FAIL: $src no longer checks the STARTTLS offer, so STARTTLS is no longer required" >&2
	fail=1
fi

[ "$fail" -eq 0 ] || exit 1
echo "OK: SMTP is dialled only through the TLS mode; no SendMail or plain Dial; certificates verified; STARTTLS required."
