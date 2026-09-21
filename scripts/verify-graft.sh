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
graft=""
# Clean up BOTH the temp cwd and the session this writes into Claude Code's
# own store. Without the second part every run leaves a directory behind in
# ~/.claude/projects and clutters the /resume picker.
#
# rmdir, never rm -rf: it refuses on a non-empty directory, so a computed path
# can never delete something unexpected. That conservatism earned its keep —
# the first version of this cleanup failed precisely because Claude Code had
# created a memory/ subdirectory, and rmdir surfaced that instead of quietly
# deleting it.
cleanup() {
  rm -rf "$work"
  if [ -n "$graft" ]; then
    dir=$(dirname "$graft")
    rm -f "$graft"
    # Claude Code also creates an empty memory/ subdirectory per project.
    rmdir "$dir/memory" 2>/dev/null || true
    rmdir "$dir" 2>/dev/null || true
  fi
}
trap cleanup EXIT
mkdir -p "$work/cwd"

# graftcheck prints the session id on the first line and the file it wrote on
# the second, so the cleanup above knows what to remove.
out=$(go run ./cmd/graftcheck "$src" "$node" "$work/cwd")
sid=$(printf '%s\n' "$out" | sed -n 1p)
graft=$(printf '%s\n' "$out" | sed -n 2p)
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
