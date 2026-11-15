#!/usr/bin/env bash
# Build, install (via the package manager, not adb install) and run the Android
# instrumentation tests on a connected device or emulator.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo"

if [[ -z "${JAVA_HOME:-}" ]]; then
  JAVA_HOME="$(/usr/libexec/java_home -v 21 2>/dev/null || true)"
fi
if [[ -z "${JAVA_HOME:-}" && -d /opt/homebrew/opt/openjdk@21 ]]; then
  JAVA_HOME=/opt/homebrew/opt/openjdk@21
fi
if [[ -z "${JAVA_HOME:-}" || ! -x "$JAVA_HOME/bin/java" ]]; then
  echo "Android app test failed: JDK 21 not found (set JAVA_HOME)" >&2
  exit 1
fi
export JAVA_HOME

sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
adb="${ADB:-}"
if [[ -z "$adb" ]] && command -v adb >/dev/null 2>&1; then
  adb="$(command -v adb)"
fi
if [[ -z "$adb" && -x "$sdk/platform-tools/adb" ]]; then
  adb="$sdk/platform-tools/adb"
fi
if [[ -z "$adb" || ! -x "$adb" ]]; then
  echo "Android app test skipped: adb not found" >&2
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
  echo "Android app test skipped: no device/emulator" >&2
  [[ "${REQUIRE_DEVICE:-0}" == "1" ]] && exit 1
  exit 0
fi

scripts/build-android-app.sh --no-install

gradle="${GRADLE:-}"
if [[ -z "$gradle" ]] && command -v gradle >/dev/null 2>&1; then
  gradle="$(command -v gradle)"
fi
if [[ -z "$gradle" && -x "$HOME/Library/Android/gradle/gradle-8.14.6/bin/gradle" ]]; then
  gradle="$HOME/Library/Android/gradle/gradle-8.14.6/bin/gradle"
fi
if [[ -z "$gradle" || ! -x "$gradle" ]]; then
  echo "Android app test failed: Gradle not found" >&2
  exit 1
fi

cd mobile/android
"$gradle" --no-daemon assembleDebugAndroidTest
app_remote=/data/local/tmp/golder-app.apk
test_remote=/data/local/tmp/golder-test.apk
"$adb" ${select_args[@]+"${select_args[@]}"} push app/build/outputs/apk/debug/app-debug.apk "$app_remote"
"$adb" ${select_args[@]+"${select_args[@]}"} push app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk "$test_remote"
for apk in "$app_remote" "$test_remote"; do
  out="$("$adb" ${select_args[@]+"${select_args[@]}"} shell pm install -r -t "$apk")"
  printf '%s\n' "$out"
  case "$out" in
    *Success*) ;;
    *) echo "package manager install failed for $apk" >&2; exit 1 ;;
  esac
done

echo "== instrumentation: $serial =="
"$adb" ${select_args[@]+"${select_args[@]}"} shell am instrument -w -r \
  -e class com.getan.golder.mobile.JniSessionTest \
  com.getan.golder.mobile.test/androidx.test.runner.AndroidJUnitRunner
