#!/bin/bash
set -euo pipefail

if [[ $# != 3 || -z "$3" ]]; then
  echo 'usage: verify.sh APP DMG EXPECTED_TEAM_ID' >&2
  exit 2
fi
app=$1
dmg=$2
team=$3
source "$(dirname "${BASH_SOURCE[0]}")/stapler.sh"

# These are deterministic integrity/identity checks: never retry or downgrade
# them to a warning, and never use a network failure to bypass them.
codesign --verify --deep --strict --verbose=2 "$app"
codesign --verify --verbose=2 "$dmg"
signature=$(codesign -dvv "$app" 2>&1)
printf '%s\n' "$signature"
if ! printf '%s\n' "$signature" | grep -Fx "TeamIdentifier=$team"; then
  echo '::error::The app signature does not match the expected Developer ID team.' >&2
  exit 1
fi
if ! printf '%s\n' "$signature" | grep -F 'Authority=Developer ID Application:'; then
  echo '::error::The app is not signed with a Developer ID Application certificate.' >&2
  exit 1
fi

# Even a stapled ticket can make stapler contact Apple's CloudKit service.
# Retry only diagnosed transport/service failures, not invalid signatures.
stapler_with_retry validate "$dmg"
