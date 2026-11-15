#!/usr/bin/env bash
# Build the Android app (native JNI library + Kotlin/Compose) and optionally
# install/launch it on the connected device.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo"

install=1
if [[ "${1:-}" == "--no-install" ]]; then
  install=0
fi

if [[ -z "${JAVA_HOME:-}" ]]; then
  JAVA_HOME="$(/usr/libexec/java_home -v 21 2>/dev/null || true)"
fi
if [[ -z "${JAVA_HOME:-}" && -d /opt/homebrew/opt/openjdk@21 ]]; then
  JAVA_HOME=/opt/homebrew/opt/openjdk@21
fi
if [[ -z "${JAVA_HOME:-}" || ! -x "$JAVA_HOME/bin/java" ]]; then
  echo "Android app build failed: JDK 21 not found (set JAVA_HOME)" >&2
  exit 1
fi
export JAVA_HOME

gradle="${GRADLE:-}"
if [[ -z "$gradle" ]] && command -v gradle >/dev/null 2>&1; then
  gradle="$(command -v gradle)"
fi
if [[ -z "$gradle" && -x /opt/homebrew/opt/gradle@8/bin/gradle ]]; then
  gradle=/opt/homebrew/opt/gradle@8/bin/gradle
fi
if [[ -z "$gradle" && -x "$HOME/Library/Android/gradle/gradle-8.14.6/bin/gradle" ]]; then
  gradle="$HOME/Library/Android/gradle/gradle-8.14.6/bin/gradle"
fi
if [[ -z "$gradle" || ! -x "$gradle" ]]; then
  echo "Android app build failed: Gradle not found (set GRADLE=/path/to/gradle)" >&2
  exit 1
fi

sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
ndk="${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}"
if [[ -z "$ndk" ]]; then
  ndk="$(ls -1d "$sdk/ndk"/* 2>/dev/null | sort -V | tail -1)"
fi
if [[ -z "$ndk" || ! -d "$ndk" ]]; then
  echo "Android app build failed: no NDK (set ANDROID_NDK_HOME)" >&2
  exit 1
fi
host="darwin-x86_64"
if [[ "$(uname -s)" == "Linux" ]]; then
  host="linux-x86_64"
fi
cc="$ndk/toolchains/llvm/prebuilt/$host/bin/aarch64-linux-android21-clang"
if [[ ! -x "$cc" ]]; then
  echo "Android app build failed: missing $cc" >&2
  exit 1
fi

jni="$repo/mobile/android/app/src/main/jniLibs/arm64-v8a"
mkdir -p "$jni"
echo "== build libgolder_mobile.so =="
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$cc" \
  go build -buildmode=c-shared -o "$jni/libgolder_mobile.so" ./cmd/golder-mobile
rm -f "$jni/libgolder_mobile.h"

printf 'sdk.dir=%s\n' "$sdk" > mobile/android/local.properties

echo "== Gradle assembleDebug =="
gradle_args=(--no-daemon)
if [[ -n "${GOLDER_PROXY:-}" ]]; then
  proxy="${GOLDER_PROXY#*://}"
  proxy_host="${proxy%%:*}"
  proxy_port="${proxy##*:}"
  gradle_args+=(
    "-Dhttp.proxyHost=$proxy_host" "-Dhttp.proxyPort=$proxy_port"
    "-Dhttps.proxyHost=$proxy_host" "-Dhttps.proxyPort=$proxy_port"
    "-Dhttp.nonProxyHosts=localhost|127.0.0.1"
  )
fi
(cd mobile/android && "$gradle" "${gradle_args[@]}" assembleDebug)
apk="$repo/mobile/android/app/build/outputs/apk/debug/app-debug.apk"
if [[ ! -f "$apk" ]]; then
  echo "Android app build failed: $apk not produced" >&2
  exit 1
fi
echo "APK: $apk"

if [[ "$install" == "0" ]]; then
  exit 0
fi

adb="${ADB:-}"
if [[ -z "$adb" ]] && command -v adb >/dev/null 2>&1; then
  adb="$(command -v adb)"
fi
if [[ -z "$adb" && -x "$sdk/platform-tools/adb" ]]; then
  adb="$sdk/platform-tools/adb"
fi
if [[ -z "$adb" || ! -x "$adb" ]]; then
  echo "APK built; device install skipped: adb not found" >&2
  exit 0
fi
select_args=()
if [[ -n "${ANDROID_SERIAL:-}" ]]; then
  select_args=(-s "$ANDROID_SERIAL")
fi
devices="$("$adb" ${select_args[@]+"${select_args[@]}"} devices 2>/dev/null || true)"
serial="$(printf '%s\n' "$devices" | awk 'NR > 1 && $2 == "device" { print $1; exit }')"
if [[ -z "$serial" ]]; then
  echo "APK built; device install skipped: no device/emulator" >&2
  exit 0
fi
echo "== install on $serial =="
remote="/data/local/tmp/golder-mobile.apk"
"$adb" ${select_args[@]+"${select_args[@]}"} push "$apk" "$remote"
install_out="$("$adb" ${select_args[@]+"${select_args[@]}"} shell pm install -r -t "$remote")"
printf '%s\n' "$install_out"
case "$install_out" in
  *Success*) ;;
  *) echo "package manager install failed" >&2; exit 1 ;;
esac
"$adb" ${select_args[@]+"${select_args[@]}"} shell am force-stop com.getan.golder.mobile
"$adb" ${select_args[@]+"${select_args[@]}"} shell am start -n com.getan.golder.mobile/.MainActivity
sleep 2
"$adb" ${select_args[@]+"${select_args[@]}"} logcat -d -s GolderJNI:I AndroidRuntime:E | tail -40
