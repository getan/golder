# Golder GUI

A desktop front-end for Golder, drawn with [MyGo](https://github.com/egoist/mygo)'s
native UI. It is a separate binary (`gui/cmd/golder-gui`); the `golder` CLI and
TUI do not import MyGo and are unaffected.

## What it is

The window is a *semantic* front-end, not a terminal inside a window: the
sidebar lists sessions, the transcript shows model text, tool cards, gate
verdicts and approvals, and the composer's footer exposes the model, reasoning
level, approval mode and context gauge. Selecting a model calls the same live
configuration the `/model` command mutates; it never types `/model` into a
hidden TUI.

The accepted follow-up design — shared provider settings, project-aware Session
management, an in-composer circular Send/Stop button, a native interactive
browser surface, an optional terminal, removal of GUI workspace/embedded CLI,
and the <45 MiB packaging target — is in
[`docs/gui-ux-optimization.md`](../docs/gui-ux-optimization.md).

```
gui/
├── appsession/   UI-neutral controller: session, run, approvals, actions
├── mygoui/       MyGo views and windows
└── cmd/golder-gui/
```

`appsession` imports no GUI toolkit and is covered by ordinary `go test`.
`mygoui` views are exercised headlessly with `ui.NewTester`.

## Run

```sh
go run ./gui/cmd/golder-gui -cwd /path/to/project
go run ./gui/cmd/golder-gui -resume <session-id> -approve
```

Flags mirror the CLI where the window needs them: `-cwd`, `-model`,
`-provider`, `-base-url`, `-protocol`, `-api-key`, `-resume`, `-approve`,
`-no-tools`, `-no-skills`. Defaults come from `config.toml` and then the
runtime defaults.

## Build

MyGo's CLI packages and signs the app:

```sh
cd gui
go run ./tools/mkicon -o resources/icon.png
MACOSX_DEPLOYMENT_TARGET=14.0 CGO_ENABLED=1 go run github.com/egoist/mygo/cmd/mygo build -platform darwin/arm64
MACOSX_DEPLOYMENT_TARGET=14.0 CGO_ENABLED=1 go run github.com/egoist/mygo/cmd/mygo dev     # rebuild and restart as the code changes
```

On macOS, Computer Use needs CGO plus Accessibility and Screen Recording
permissions. Move Golder GUI.app to Applications and launch that copy. Settings
→ Permissions and the Computer Use picker provide explicit request buttons;
Settings also links to each macOS Privacy & Security page. If the app is not listed,
use `+` in System Settings to select Golder GUI.app; restart after granting
access if macOS asks. macOS permissions do not grant the agent a target window.
Ad-hoc signatures can change identity between builds, so macOS may require a
new grant after an update. A stable Developer ID signature improves continuity.
The GUI release targets macOS 14.0 or later. A GUI built without CGO cannot
enable the macOS Computer Use backend merely by changing permissions.

The app is named **Golder GUI** (slug `golder-gui`), so its Linux executable
never collides with the `golder` CLI. The Agent runtime is linked into the GUI;
the bundle deliberately carries neither a second full CLI nor tuios-mini.
Artifacts, size gates, signing secrets and the Gatekeeper tradeoff are described
in [`docs/gui-packaging.md`](../docs/gui-packaging.md).

Two optional surfaces need runtime pieces:

- `libghostty-vt` for the terminal plugin, installed by `mygo build` (a
  `go run` binary downloads it once into the user's cache);
- an installed Chromium for the native interactive Browser Surface. The GUI's
  linked browserhost starts it on demand and publishes an owner-only control
  socket; Chromium itself and a second full `golder` CLI are never bundled.

## What the window does

- Navigation: the left rail keeps archive, update and settings actions available
  when the projects and chats sidebar is collapsed. Drag the divider to resize
  the sidebar. Release builds check GitHub for a newer GUI package at startup;
  the update icon turns blue when the matching platform package is published.
  Click it to download the installer, then install it and restart the GUI. This
  is not an unattended in-place update; package-manager installations should
  continue to use their package manager. Source builds cannot compare versions.
- Files: browse within the current project, show hidden files with the eye
  button or Cmd/Ctrl+Shift+Period, and edit syntax-highlighted text with Save
  (Cmd/Ctrl+S). Saves use the operator's normal filesystem permissions and
  refuse to overwrite files changed since opening; binary files and previews
  truncated at 512 KiB cannot be edited.
- sessions: project/path grouping, new/resume/rename/archive/delete/export, and
  one shared OS writer lease across GUI, TUI, REPL and headless runs (`flock` on
  Unix, `LockFileEx` on Windows); a conflicting GUI opens the transcript
  read-only and names the active writer;
- turns: streamed Markdown, thinking (collapsed), compact failure panels and
  grouped, indentation-preserving `apply_patch` diff cards with bounded output,
  compaction and provider notices, cancel;
- approvals: consent and trust questions answered inline; closing the window
  fails every pending question closed;
- configuration: a shared Settings page for provider, model, base URL, protocol
  and named credentials, plus GUI-local System/Light/Dark appearance and
  persistent font scaling, and live model/reasoning/approval controls;
- proxy: Settings → Proxy edits the same `proxy.toml` as CLI `/proxy`. The
  suggested URL is `http://127.0.0.1:7897` (a Clash Verge mixed-port address); adjust
  it to the local proxy in use. Browser and individual providers are selected
  independently, and nothing is enabled merely by saving the address. Provider
  changes apply to subsequent requests; close and reopen an existing browser to
  change its route. `GOLDER_PROXY`, when set, overrides the saved URL. Browser
  proxy URLs cannot include credentials or paths.
- extensions: a native manager for checksummed first-party package install,
  exact source review, named-secret configuration, browser OAuth, safe
  phase-coded failures, one-click helper update/rebinding, separate bundled /
  installed / active and application/user-auth status, test, enable/disable,
  reset and uninstall; installed native Lark/Project CLIs are auto-detected, and
  pinned official binaries can be downloaded and integrity-checked directly
  without requiring npm or Node; other configured sources appear there too;
- files: a custom, project-scoped Files drawer with lazy folder expansion and a
  bounded read-only preview; generated directories are hidden and symlinks are
  shown but never followed;
- changes: a Git status + unified diff drawer rendered by MyGo's diff viewer,
  opened from a compact icon-only title action;
- actions: a command palette over the existing slash registry, so skills,
  prompt templates and plugin commands stay reachable without typing;
- browser: an Agent-visible built-in tool backed by the GUI browserhost, with
  native Bitmap frames in an automatically revealed right drawer and mouse,
  keyboard, scroll, resize and committed IME input forwarded through the typed
  control socket; auto-reveal follows the exact opened socket even with several
  browsers or a short runtime path, and retries bounded discovery while a newly
  launched socket is becoming visible; title and approval actions use
  theme-colored monochrome SVGs;
- terminal: a project-scoped, resizable panel with real shell panes. Right-click
  a pane title to split, rename or close it, separately grant an active Agent
  view or control access, revoke access, or take back control. Agent-created
  panes retain their creation-time sandbox/network ceiling; full-access panes
  may be unconfined. User shells run outside the Agent sandbox, so controlling
  them requires explicit per-call review in Ask mode (or Full Access). Exited
  panes retain their output until closed. Existing Agent `bash` calls with
  `tty=true` can still be attached and detached without terminating them.

Deliberately absent: tuios workspace/pane management and a full IDE. Those
remain CLI/TUI responsibilities.
