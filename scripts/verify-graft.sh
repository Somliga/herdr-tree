#!/usr/bin/env bash
# Manual end-to-end check: does Claude Code still resume a transcript we
# wrote? Costs a small Haiku call. Not for CI.
#
# Usage: scripts/verify-graft.sh <source-session.jsonl> <node-uuid> <kept-topic> <pruned-topic>
set -euo pipefail

src=${1:?source transcript}
node=${2:?node uuid to graft at}
kept=${3:?a word that should survive}
pruned=${4:?a word that should NOT survive}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/cwd"

sid=$(go run ./cmd/graftcheck "$src" "$node" "$work/cwd")
echo "grafted session: $sid"

reply=$(cd "$work/cwd" && claude -p --model claude-haiku-4-5-20251001 \
  --resume "$sid" \
  "List every topic we have discussed so far as a short bullet list. Do not use any tools." \
  < /dev/null)

echo "--- reply ---"
echo "$reply"
echo "-------------"

fail=0
grep -qi -- "$kept"   <<<"$reply" || { echo "FAIL: kept topic '$kept' missing"; fail=1; }
grep -qi -- "$pruned" <<<"$reply" && { echo "FAIL: pruned topic '$pruned' leaked"; fail=1; }
[ "$fail" = 0 ] && echo "PASS: graft truncates history correctly"
exit "$fail"
