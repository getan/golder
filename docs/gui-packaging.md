# Packaging the Golder GUI

`golder-gui` is packaged by MyGo's own CLI, not by goreleaser: goreleaser builds
single executables, while a desktop app needs a bundle, a disk image, an
installer or a system package to be double-clickable.

## Artifacts

| Platform | Output under `gui/build/<os>-<arch>/` | How a person opens it |
|---|---|---|
| macOS | `Golder GUI.app`, `golder-gui-<version>-<arch>.dmg` | mount the DMG, drag to Applications, double-click |
| Windows | `Golder GUI.exe`, `golder-gui-setup-<version>-amd64.exe` | run the installer, then the Start-menu shortcut |
| Linux | `golder-gui`, `.desktop`, `golder-gui-<version>-linux-<arch>.tar.gz` (with `install.sh`), `.deb`, `PKGBUILD` | `install.sh`, `apt install ./…deb`, or `makepkg -si` |

The app name is `Golder GUI` and its slug is `golder-gui`, so the Linux
executable never collides with the `golder` CLI.

## The CLI inside the app

A window that opens a terminal workspace runs `golder workspace attach`. A
packaged app must not depend on the person's `PATH`, so the build compiles the
CLI into the bundle:

```text
gui/resources/<os>-<arch>/bin/golder
```

`gui/mygoui/terminal.go` looks there first (`mygo.App.Path(mygo.PathResources)`),
then `GOLDER_BIN`, then next to the executable, then `PATH`. For a universal
macOS app the resource is a universal binary made with `lipo`.

These directories are build inputs, not committed files (see `.gitignore`); the
`gui` workflow fills them per platform before packaging.

## Building locally

```sh
cd gui
go run github.com/egoist/mygo/cmd/mygo build -platform darwin/arm64 -sign - -skip-notarize
```

`-sign -` is ad-hoc signing: the app runs on the machine that built it, but
Gatekeeper refuses it on other Macs. To ship, sign with a Developer ID and
notarize; the workflow does both when its secrets exist.

## macOS: what makes it double-clickable elsewhere

An ad-hoc signed app is refused on other Macs with "the developer cannot be
verified". Two ways forward:

1. **Sign and notarize** (what the workflow does when configured). Set:
   - `MACOS_SIGNING_IDENTITY` — `Developer ID Application: Name (TEAMID)`
   - `MACOS_CERT_P12` (base64 of the exported `.p12`) and `MACOS_CERT_PASSWORD`
   - `APPLE_ID`, `APPLE_APP_PASSWORD`, `APPLE_TEAM_ID` for `notarytool`

   With those present the workflow imports the certificate, creates the
   `golder-notary` keychain profile that `gui/mygo.json` names, and lets
   `mygo build` sign, notarize and staple.

2. **No Apple Developer Program** — the app still runs, but the person must
   right-click → Open once, or clear the quarantine flag:

   ```sh
   xattr -d com.apple.quarantine "/Applications/Golder GUI.app"
   ```

   This is a conscious tradeoff, not a bug: macOS ties trust to a signature it
   can verify with Apple.

## Signing secrets the workflow reads

| Secret | Used for |
|---|---|
| `MACOS_SIGNING_IDENTITY` | `-sign` on macOS |
| `MACOS_CERT_P12`, `MACOS_CERT_PASSWORD` | importing the certificate into a temporary keychain |
| `APPLE_ID`, `APPLE_APP_PASSWORD`, `APPLE_TEAM_ID` | `xcrun notarytool store-credentials golder-notary` |
| `MYGO_WINDOWS_CERTIFICATE_PASSWORD` | Windows Authenticode, when `windows.certificate` is configured |

None of them are required for a local build.

## CI

`.github/workflows/gui.yml` runs on `v*` tags and on manual dispatch:

| Runner | Target | Notes |
|---|---|---|
| `macos-14` | `darwin/universal` | signs/notarizes when secrets exist, then builds the DMG |
| `windows-2022` | `windows/amd64` | NSIS installer; needs NSIS, which MyGo installs |
| `ubuntu-22.04` | `linux/amd64,linux/arm64` | tarball + `install.sh`, `.deb`, `PKGBUILD` |

On a tag, the artifacts are attached to the same GitHub Release that goreleaser
creates, so a release carries both the CLI archives and the GUI packages.

## Linux notes

- The app needs GTK 3 only (`"webview": false`), not WebKitGTK.
- The terminal window additionally needs `libghostty-vt`, which `mygo build`
  places among the app's resources.
- `install.sh` installs under `~/.local`; the `.deb` and `PKGBUILD` install
  system-wide under `/opt`.
