#!/usr/bin/env bash
# Manual end-to-end check for the v2 timeline. Spends real API budget.
# Never run this in CI.
#
# Usage: scripts/verify-timeline.sh <source-session.jsonl> <from-uuid> <to-uuid>
#
# Verifies the two things v2 cannot prove with unit tests:
#   1. that claude.Summarise produces a usable summary of a turn range
#   2. that a seeded graft resumes carrying both its grafted history AND
#      the seeded summary
#
# It does NOT verify `herdr agent prompt` delivering a multi-paragraph
# message reliably (check 3 below) — that needs a live agent pane and is
# a separate manual step. See the checklist this script prints at the end.
set -euo pipefail

if [ $# -lt 3 ]; then
  echo "usage: $0 <source-session.jsonl> <from-uuid> <to-uuid>" >&2
  exit 2
fi

src=${1:?source transcript}
from=${2:?range start uuid}
to=${3:?range end uuid}

if [ ! -f "$src" ]; then
  echo "FAIL: source transcript not found: $src" >&2
  exit 1
fi

work=""
graft=""
# cleanup must be safe even if it fires before $work or $graft are set (e.g.
# mktemp itself fails). Both branches are guarded on the variable being
# non-empty, so an early failure can never turn into rm -rf "" or similar.
#
# rmdir, never rm -rf, for the grafted session's directory: rmdir refuses on
# a non-empty directory, so a computed path can never delete something this
# script didn't create. Same reasoning as scripts/verify-graft.sh.
cleanup() {
  if [ -n "$work" ]; then
    rm -rf "$work"
  fi
  if [ -n "$graft" ]; then
    dir=$(dirname "$graft")
    rm -f "$graft"
    rmdir "$dir/memory" 2>/dev/null || true
    rmdir "$dir" 2>/dev/null || true
  fi
}
trap cleanup EXIT

work=$(mktemp -d)
mkdir -p "$work/cwd"

echo "== 1. summarise the range =="
summary=$(go run ./cmd/timelinecheck summarise "$src" "$from" "$to" "$work/cwd")
echo "$summary" | head -20
[ -n "$summary" ] || { echo "FAIL: empty summary"; exit 1; }

echo
echo "== 2. seed a graft with it and resume =="
out=$(go run ./cmd/timelinecheck seed "$src" "$from" "$work/cwd" "$summary")
sid=$(printf '%s\n' "$out" | sed -n 1p)
graft=$(printf '%s\n' "$out" | sed -n 2p)

reply=$(cd "$work/cwd" && claude -p --model claude-haiku-4-5-20251001 \
  --resume "$sid" \
  "In one line each: what is the earliest thing we discussed, and what does the summary note you were just given say was rejected?" \
  < /dev/null)
echo "--- reply ---"; echo "$reply"; echo "-------------"

echo
echo "== Checklist: read the output above yourself. These are human judgement, =="
echo "== not assertions this script can make.                                  =="
echo
echo "[1] Seed fields vs. a real resume (Task 4, Minor 6):"
echo "    Did the session above resume at all, without Claude Code complaining"
echo "    about the grafted file? The reply must show BOTH the grafted history"
echo "    (the earliest thing discussed) AND the seeded summary (what was"
echo "    rejected). If Claude Code rejected the file or the reply is missing"
echo "    either half, the seed's fields (version, timestamp granularity, or"
echo "    something else) matter more than assumed."
echo
echo "[2] Summary prompt quality (Task 5, Minor d):"
echo "    Read the summary printed in step 1. Does it describe ONLY the"
echo "    requested range (turn $from through $to), not the whole transcript?"
echo "    Does it contain a 'rejected, and why' section with specifics, not a"
echo "    vague non-answer? A summary that just restates the range's topic"
echo "    without saying what was rejected has failed even though this script"
echo "    exits 0."
echo
echo "[3] herdr agent prompt with a multi-paragraph message (Task 6):"
echo "    NOT covered by this script — it needs a live agent pane. Separately,"
echo "    in a scratch pane: start an agent, then from another pane run"
echo "    'herdr agent prompt <name> \"<three paragraphs>\" --wait --timeout 120000'"
echo "    and read the agent's pane (not just the exit code) to confirm the"
echo "    WHOLE message arrived, newlines intact, and the agent began a turn."
echo "    Herdr's own docs warn a clean exit does not prove the agent started."
echo
echo "[4] Range marker alignment (Task 8, observation):"
echo "    Not exercised by this script (it's a rendering concern in the TUI,"
echo "    not the CLI path this script drives). When looking at a real tree"
echo "    with mixed depths in the plugin, check whether the '┃' range marker"
echo "    is still legible or has started to look like it steps in and out."
echo
echo "[5] Exactly one ⤶ marker on a real injected summary (Task 7):"
echo "    Look at the summary text printed in step 1, and at the grafted file"
echo "    left behind briefly at: $graft"
echo "    (it will be removed when this script exits). Count the ⤶ markers at"
echo "    the start of the seeded entry's text — there must be exactly one,"
echo "    never '⤶ ⤶ summary of ...'."
echo
echo "PASS if [1] and [2] hold. [3], [4], [5] are separate manual checks;"
echo "record their results in the plan's notes."
