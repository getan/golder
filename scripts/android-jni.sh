#!/usr/bin/env bash
# Build libgolder_mobile.so with the NDK and, when a device/emulator is
# present, run the C ABI loader. The build tier always runs.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo"

sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/Library/Android/sdk}}"
ndk="${ANDROID_NDK_HOME:-${ANDROID_NDK_ROOT:-}}"
if [[ -z "$ndk" ]]; then
  ndk="$(ls -1d "$sdk/ndk"/* 2>/dev/null | sort -V | tail -1)"
fi
if [[ -z "$ndk" || ! -d "$ndk" ]]; then
  echo "JNI build failed: no NDK (set ANDROID_NDK_HOME)" >&2
  exit 1
fi

host="darwin-x86_64"
if [[ "$(uname -s)" == "Linux" ]]; then
  host="linux-x86_64"
fi
toolchain="$ndk/toolchains/llvm/prebuilt/$host"
cc="$toolchain/bin/aarch64-linux-android21-clang"
if [[ ! -x "$cc" ]]; then
  echo "JNI build failed: missing $cc" >&2
  exit 1
fi

out="$repo/.tmp/android/libgolder_mobile.so"
loader="$repo/.tmp/android/abi-loader"
mkdir -p "$(dirname "$out")"
echo "== build libgolder_mobile.so =="
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$cc" \
  go build -buildmode=c-shared -o "$out" ./cmd/golder-mobile
"$toolchain/bin/llvm-nm" -D --defined-only "$out" | grep -q ' Java_com_getan_golder_mobile_GolderNative_open$'

echo "== build C ABI loader =="
"$cc" -O2 -o "$loader" internal/mobilebridge/testdata/abi_loader.c -ldl

adb="${ADB:-}"
if [[ -z "$adb" ]] && command -v adb >/dev/null 2>&1; then
  adb="$(command -v adb)"
fi
if [[ -z "$adb" ]]; then
  for candidate in "$sdk/platform-tools/adb" /opt/homebrew/bin/adb /usr/local/bin/adb; do
    if [[ -x "$candidate" ]]; then
      adb="$candidate"
      break
    fi
  done
fi
if [[ -z "$adb" || ! -x "$adb" ]]; then
  echo "device tier skipped: adb not found"
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

remote="/data/local/tmp/golder-jni"
"$adb" ${select_args[@]+"${select_args[@]}"} shell mkdir -p "$remote"
"$adb" ${select_args[@]+"${select_args[@]}"} push "$out" "$remote/libgolder_mobile.so"
"$adb" ${select_args[@]+"${select_args[@]}"} push "$loader" "$remote/abi-loader"
"$adb" ${select_args[@]+"${select_args[@]}"} shell chmod 755 "$remote/abi-loader"
echo "== device ABI test: $serial =="
"$adb" ${select_args[@]+"${select_args[@]}"} shell "$remote/abi-loader" "$remote/libgolder_mobile.so"
