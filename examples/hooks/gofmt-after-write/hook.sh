#!/usr/bin/env bash
# PostToolUse hook: run gofmt on a Go file after write/edit touched it.
#
# Wiring (matcher "write|edit"): golder pipes the tool input + response as JSON
# on stdin after the tool ran. This is an observing hook — it formats the file
# as a side effect and exits 0. The written path may appear under either
# `.tool_input.path` or `.tool_input.file_path` depending on the tool, so we
# try both.
set -euo pipefail

payload=$(cat)
path=$(printf '%s' "$payload" | jq -r '.tool_input.path // .tool_input.file_path // ""')

case "$path" in
  *.go)
    if [ -f "$path" ] && command -v gofmt >/dev/null 2>&1; then
      gofmt -w "$path"
    fi
    ;;
esac

exit 0
