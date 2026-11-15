#!/usr/bin/env bash
# Android regression in tiers. The host and cross-build tiers always run; the
# device tier runs only when adb can see a device or emulator. Set
# REQUIRE_DEVICE=1 to turn "no device" into a failure (CI), or ANDROID_SERIAL /
# ADB to select a specific target.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo"

arch="${ANDROID_ARCH:-arm64}"
out="$repo/.tmp/android/mobileagent.test"
test_timeout="${ANDROID_TEST_TIMEOUT:-180s}"

echo "== host tier: platform-neutral tests =="
go test ./internal/mobileagent ./internal/androidagent

echo "== Android tier: cross-compile and typecheck =="
GOOS=android GOARCH="$arch" CGO_ENABLED=0 go vet ./internal/mobileagent ./internal/androidagent
mkdir -p "$(dirname "$out")"
GOOS=android GOARCH="$arch" CGO_ENABLED=0 go test -c -o "$out" ./internal/mobileagent

adb="${ADB:-}"
if [[ -z "$adb" ]] && command -v adb >/dev/null 2>&1; then
  adb="$(command -v adb)"
fi
if [[ -z "$adb" ]]; then
  for candidate in "$HOME/Library/Android/sdk/platform-tools/adb" /opt/homebrew/bin/adb /usr/local/bin/adb; do
    if [[ -x "$candidate" ]]; then
      adb="$candidate"
      break
    fi
  done
fi
if [[ -n "$adb" && ! -x "$adb" ]]; then
  echo "device tier skipped: ADB is not executable: $adb"
  adb=""
fi
if [[ -z "$adb" ]]; then
  echo "device tier skipped: adb not found (set ADB=/path/to/adb)"
  [[ "${REQUIRE_DEVICE:-0}" == "1" ]] && exit 1
  exit 0
fi

select_args=()
if [[ -n "${ANDROID_SERIAL:-}" ]]; then
  select_args=(-s "$ANDROID_SERIAL")
fi
devices="$("$adb" ${select_args[@]+"${select_args[@]}"} devices 2>/dev/null || true)"
serial="$(printf '%s\n' "$devices" | awk 'NR > 1 && $2 == "device" { print $1; exit }')"
if [[ -z "$serial" ]]; then
  echo "device tier skipped: no device/emulator in the 'device' state"
  [[ "${REQUIRE_DEVICE:-0}" == "1" ]] && exit 1
  exit 0
fi

echo "== device tier: $serial =="
remote="/data/local/tmp/golder-mobileagent-${arch}.test"
"$adb" ${select_args[@]+"${select_args[@]}"} push "$out" "$remote"
"$adb" ${select_args[@]+"${select_args[@]}"} shell chmod 755 "$remote"
"$adb" ${select_args[@]+"${select_args[@]}"} shell "$remote" -test.v -test.timeout="$test_timeout"
