# Packaging the Golder GUI

`golder-gui` is packaged by MyGo's own CLI, not by goreleaser: goreleaser builds
single executables, while a desktop app needs a bundle, a disk image, an
installer or a system package to be double-clickable.

The packaging boundary below implements the accepted optimization target:
separate macOS architectures, no bundled full CLI or Chromium, a tag-derived
app version, and a per-architecture installed-size gate. See
[`gui-ux-optimization.md`](gui-ux-optimization.md).

## Artifacts

| Platform | Output under `gui/build/<os>-<arch>/` | How a person opens it |
|---|---|---|
| macOS | `Golder GUI.app`, `golder-gui-<version>-<arch>.dmg` | mount the DMG, drag to Applications, double-click |
| Windows | `Golder GUI.exe`, `golder-gui-setup-<version>-amd64.exe` | run the installer, then the Start-menu shortcut |
| Linux | `golder-gui`, `.desktop`, `golder-gui-<version>-linux-<arch>.tar.gz` (with `install.sh`), `.deb`, `PKGBUILD` | `install.sh`, `apt install ./…deb`, or `makepkg -si` |

The app name is `Golder GUI` and its slug is `golder-gui`, so the Linux
executable never collides with the `golder` CLI.

## Bundle boundary

The GUI runs the Agent runtime in-process. It does not need `golder workspace`,
split panes, detach/attach or any other tuios-mini service, so none of the
following may appear in a package:

```text
Resources/bin/golder
tuios-mini workspace renderer
Chromium / Chrome for Testing
```

The MyGo terminal plugin remains: it contributes approximately 2 MiB per
architecture of `libghostty-vt` and supports explicit user shells and Agent TTY
takeover. The Browser Surface talks to an already-running controlled Chromium;
it does not package a browser.

The package also carries one small first-party helper:

```text
Resources/extensions/lark/golder-lark[.exe]
Resources/extensions/lark/golder-lark[.exe].sha256
```

It is built for the exact package architecture and version. It remains inert in
the bundle until the operator clicks Install in Settings → Extensions; the
managed installer verifies the checksum before copying it under `$GOLDER_HOME`.

## Building locally

```sh
cd gui
CGO_ENABLED=1 go run github.com/egoist/mygo/cmd/mygo build -platform darwin/arm64 -sign - -skip-notarize
```

CGO is required for the macOS Computer Use backend; the GUI release workflow
enables it and checks the packaged executable's Go build information.

`-sign -` is ad-hoc signing: the app runs on the machine that built it, but
Gatekeeper refuses it on other Macs. To ship, sign with a Developer ID and
notarize; the workflow does both when its secrets exist.
An ad-hoc signature may also change the identity macOS privacy controls use
between builds, so a previously granted permission may need to be requested
again. A stable Developer ID signature helps keep that identity consistent;
it does not grant permissions without the operator's approval.

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

2. **No Apple Developer Program** — the app still runs, but each machine has to
   allow it once. The wording differs by macOS version:

   - **macOS 15 Sequoia and later**: double-click once and dismiss the warning,
     then open **System Settings → Privacy & Security**, scroll to the bottom,
     and click **Open Anyway**. Right-click → Open no longer bypasses a
     quarantined app on these versions.
   - **macOS 14 and earlier**: right-click the app → **Open** → **Open**.
   - Either way, the quarantine flag can be cleared from a terminal:

     ```sh
     xattr -dr com.apple.quarantine "/Applications/Golder GUI.app"
     ```

   This is a conscious tradeoff, not a bug: macOS ties trust to a signature it
   can verify with Apple, and the "Apple cannot check it for malicious software"
   message means exactly that the app is signed but not notarized. There is no
   packaging trick that avoids it — zip, DMG and Homebrew casks all carry the
   quarantine attribute.

## The app icon

`gui/resources/icon.png` is the app icon: the existing white panel, dark `>`
chevron and gold `-` cursor drawn as smooth high-resolution geometry. The
generator renders at 4× and downsamples to 1024×1024, producing antialiased
curves without changing the mark or background. It is generated, not hand-edited:

```sh
cd gui
go run ./tools/mkicon -o resources/icon.png
```

`mygo build` turns it into `AppIcon.icns` and references it from the bundle's
`Info.plist`. A missing `resources/icon.png` is why a build shows the generic
blank icon. The GUI release workflow regenerates it immediately before every
platform build, so all packages use the source-controlled geometry.

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
| `macos-15` | `darwin/arm64` | separate Apple Silicon DMG; signs/notarizes when secrets exist |
| `macos-15-intel` | `darwin/amd64` | separate Intel DMG; signs/notarizes when secrets exist |
| `windows-2022` | `windows/amd64` | NSIS installer; needs NSIS, which MyGo installs |
| `ubuntu-22.04` | `linux/amd64,linux/arm64` | tarball + `install.sh`, `.deb`, `PKGBUILD` |

Before upload, CI verifies that the GUI dependency graph excludes tuios-mini,
that no full CLI or Chromium exists in the bundle, and that each macOS `.app`
contains the checksummed Lark helper and remains below 50 MiB.

On a tag, the artifacts are attached to the same GitHub Release that goreleaser
creates, so a release carries both the CLI archives and the GUI packages.

## Linux notes

- The app needs GTK 3 only (`"webview": false`), not WebKitGTK.
- The terminal window additionally needs `libghostty-vt`, which `mygo build`
  places among the app's resources.
- `install.sh` installs under `~/.local`; the `.deb` and `PKGBUILD` install
  system-wide under `/opt`.
