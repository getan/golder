#!/bin/bash
# Finalize the already-signed MyGo bundle without re-signing its nested code.
set -euo pipefail

if [[ $# != 3 ]]; then
  echo "usage: finalize.sh APP DMG SIGNING_IDENTITY" >&2
  exit 2
fi
app=$1
dmg=$2
identity=$3
helper="$app/Contents/Resources/extensions/lark/golder-lark"
if [[ ! -d "$app" || ! -f "$dmg" || ! -f "$helper" || -z "$identity" ]]; then
  echo 'Missing app, disk image, helper, or signing identity.' >&2
  exit 1
fi

# MyGo signs nested executables after copying them. Hash the final helper,
# then seal the updated resource in the outer app without --deep: re-signing
# the helper here would invalidate the checksum again.
codesign --verify --verbose=2 "$helper"
shasum -a 256 "$helper" > "$helper.sha256"
args=(--force --sign "$identity" --preserve-metadata=entitlements)
if [[ "$identity" != '-' ]]; then
  args+=(--options runtime --timestamp)
fi
codesign "${args[@]}" "$app"
codesign --verify --deep --strict --verbose=2 "$app"
expected=$(awk 'NR == 1 {print $1}' "$helper.sha256")
actual=$(shasum -a 256 "$helper" | awk '{print $1}')
if [[ "$expected" != "$actual" ]]; then
  echo 'Signing the app changed the bundled helper.' >&2
  exit 1
fi

# Keep MyGo's volume layout and icon. Replace only its app in a temporary
# writable copy, then create a lighter-compressed image for notarization.
work=$(mktemp -d "${TMPDIR:-/tmp}/golder-finalize.XXXXXX")
mount="$work/mount"
mounted=0
cleanup() {
  if [[ "$mounted" == 1 ]]; then
    if ! hdiutil detach "$mount"; then
      echo "Could not detach $mount; leaving temporary image at $work" >&2
      return
    fi
  fi
  rm -rf "$work"
}
trap cleanup EXIT
mkdir "$mount"
hdiutil convert "$dmg" -format UDRW -o "$work/writable.dmg"
hdiutil attach "$work/writable.dmg" -readwrite -noverify -noautoopen -nobrowse -mountpoint "$mount"
mounted=1
image_app="$mount/$(basename "$app")"
if [[ ! -d "$image_app" || -L "$image_app" ]]; then
  echo 'The disk image does not contain the expected app.' >&2
  exit 1
fi
rm -rf "$image_app"
ditto "$app" "$image_app"
image_helper="$image_app/Contents/Resources/extensions/lark/golder-lark"
cmp "$helper" "$image_helper"
cmp "$helper.sha256" "$image_helper.sha256"
codesign --verify --deep --strict --verbose=2 "$image_app"
hdiutil detach "$mount"
mounted=0
hdiutil convert "$work/writable.dmg" -format UDZO -o "$work/final.dmg"
if [[ "$identity" != '-' ]]; then
  codesign --force --timestamp --sign "$identity" "$work/final.dmg"
fi
mv "$work/final.dmg" "$dmg"

if [[ "$identity" != '-' ]]; then
  # JSON keeps the submission ID and final verdict available in the log.
  # Do not retry a submission automatically after an unknown network result.
  result=$(xcrun notarytool submit "$dmg" --keychain-profile golder-notary --wait --output-format json)
  printf '%s\n' "$result"
  if ! printf '%s\n' "$result" | jq -e '.status == "Accepted"' >/dev/null; then
    echo 'Apple did not accept the notarization submission.' >&2
    exit 1
  fi
  source "$(dirname "${BASH_SOURCE[0]}")/stapler.sh"
  stapler_with_retry staple "$dmg"
  stapler_with_retry staple "$app"
fi
