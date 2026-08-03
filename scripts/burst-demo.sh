#!/usr/bin/env bash
#
# One burst of events through the real rules, with no mail server involved.
#
# This exists because the three limits (a severity floor, a dedup window, an
# hourly ceiling) are easy to describe and hard to believe. It builds a burst,
# runs the actual binary over it in file mode, and counts what came out.
#
# What it is: a SCENARIO. The severities in it are this script's choice, so the
# result shows what the rules do to a stream, not what a real fleet produces.
# Volume under a real fleet is unmeasured, and VALIDATION.md says so.
set -euo pipefail

cd "$(dirname "$0")/.."

WORK="${TMPDIR:-/tmp}/heraldyx-burst"
rm -rf "$WORK"
mkdir -p "$WORK"

go build -o bin/heraldyx ./cmd/heraldyx

python3 - "$WORK/events.ndjson" <<'PY'
import json
import sys

TS = "2026-08-03T02:14:00Z"
rows = []


def ev(typ, agent, run, sev, source, data):
    e = {"schema": "taipanbox.dev/agent-event/v0.2", "ts": TS, "source": source,
         "type": typ, "agent_id": agent, "severity": sev, "data": data}
    if run:
        e["run_id"] = run
    rows.append(json.dumps(e, separators=(",", ":")))


# One runaway run trips the same condition over and over. This is what the
# dedup window is for: one condition about one subject is one message.
for i in range(30):
    ev("sustained_loop", "agent://acme/crawler", "run-501", "high", "tokenfuse",
       {"count": 3 + i, "org": "acme"})

# Distinct conditions about distinct subjects, all at the default floor.
for i in range(25):
    ev("policy_deny", "agent://acme/writer-%02d" % i, "run-6%02d" % i, "high",
       "wardryx", {"decision": "deny", "org": "acme"})

# Real signal below the floor: nothing is wrong yet.
for i in range(62):
    ev("budget_threshold", "agent://acme/biller-%02d" % i, "run-7%02d" % i,
       "medium", "tokenfuse",
       {"org": "acme", "budget_micros": 2000000, "spent_micros": 1600000})

# A severity this build has never heard of. Not escalated, not dropped.
for i in range(3):
    ev("quality_drift", "agent://acme/summariser-%d" % i, "run-8%02d" % i,
       "catastrophic", "verdryx", {"org": "acme"})

with open(sys.argv[1], "w") as fh:
    fh.write("\n".join(rows) + "\n")
print("events in: %d" % len(rows))
PY

HERALDYX_EVENTS="$WORK/events.ndjson" \
HERALDYX_TO=ops@example.com \
HERALDYX_MAIL_FILE="$WORK/mail.txt" \
HERALDYX_STATE="$WORK/state.json" \
HERALDYX_CONSOLE_URL=https://box.example.com \
HERALDYX_BOX=prod-box \
  ./bin/heraldyx --once --from-now=false

echo
echo "messages written: $(grep -c '^Subject:' "$WORK/mail.txt" || true)"
echo
grep '^Subject:' "$WORK/mail.txt" | sed 's/^Subject: /  /' | sort | uniq -c | sort -rn
echo
echo "the mail is in $WORK/mail.txt, the dispatch journal beside $WORK/state.json"
echo "read the journal with: ./bin/heraldyx --journal --state $WORK/state.json"
