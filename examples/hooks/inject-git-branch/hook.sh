#!/usr/bin/env bash
# UserPromptSubmit hook: inject the current git branch into the model context.
#
# Wiring (no matcher — UserPromptSubmit is tool-agnostic): golder pipes the
# submitted prompt as JSON on stdin. On exit 0 golder parses this hook's stdout
# as JSON; the `additionalContext` field is appended to the model input for
# this turn only.
set -euo pipefail

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "no-git")
printf '{"additionalContext": "Current git branch: %s"}\n' "$branch"
exit 0
