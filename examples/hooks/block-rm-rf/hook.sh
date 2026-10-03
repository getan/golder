#!/usr/bin/env bash
# PreToolUse hook: block dangerous `rm -rf` commands before Bash runs them.
#
# Wiring (matcher "bash"): golder pipes a single-line JSON payload on stdin and
# reads this hook's exit code:
#   exit 2  -> block the tool call; stderr is shown as the reason
#   exit 0  -> allow
# Requires `jq` on PATH.
set -euo pipefail

payload=$(cat)
cmd=$(printf '%s' "$payload" | jq -r '.tool_input.command // ""')

# Match rm with a recursive+force combo: `rm -rf`, `rm -r -f`, `rm -fr`, etc.
if printf '%s' "$cmd" | grep -Eq 'rm[[:space:]]+(-[a-zA-Z]*r[a-zA-Z]*[[:space:]]+)*-?[a-zA-Z]*f'; then
  echo "blocked: 'rm -rf' is not allowed by project policy" >&2
  exit 2
fi

exit 0
