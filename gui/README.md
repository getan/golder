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
macOS may label screen capture as **Screen & System Audio Recording**; Computer
Use captures selected-window images, not audio. Accessibility is a separate
permission for inspecting and operating window controls. A named application
request automatically uses its unique matching window; multiple matching apps
or windows still require a local choice, not another OS permission grant.

Switching Golder conversations hides the old Computer Use view and stops its
local preview, but leaves the owning conversation's authorized control session
running. It neither transfers the target to the new conversation nor resumes an
explicitly paused session. Hiding the Computer Use panel also leaves control
running; use **Pause / take over** or **Stop** to interrupt it. On macOS, frame
input can restore focus to the same authorized window, then recheck its frame
before dispatch. Preview captures and semantic actions do not steal focus.

The GUI release targets macOS 14.0 or later. A GUI built without CGO cannot
enable the macOS Computer Use backend merely by changing permissions.

Windows uses a File → Exit menu alongside Edit and Window. File pickers are
parented and serialized; a close request cancels only the owned picker, then
continues the normal terminal/session shutdown after that picker returns.
The Windows packaging job runs native menu-Alt and quit-during-picker checks;
locally run them in a Windows desktop session with
`GOLDER_WINDOWS_GUI_E2E=1 go test ./gui/mygoui -run '^TestWindowsNativeCompatibility$' -count=1`.
These opt-in native checks are not exercised by a macOS cross-build.

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
  the sidebar. Project add buttons appear on hover or keyboard focus. Projects
  persist separately from sessions; remove a project with its context menu.
  A graphical launch from the filesystem root restores the selected available
  non-root project, or uses the home directory when none is available. An
  explicit `-cwd` is preserved. Release builds check GitHub for a newer GUI
  package at startup;
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
  compaction and provider notices, cancel. Messages entered during a run are
  queued per session and delivered after the current tool batch or response;
  edit or remove a pending message before delivery. Stop interrupts the run and
  sends any remaining queued input next. Pending inputs remain in memory until
  delivered and persisted in the conversation;
- composer history: Up/Down browse the same global prompt history as the TUI,
  preserving the current draft and normal caret movement within multiline text;
- image paste: paste a copied image into the composer with Cmd+V on macOS or
  Ctrl+V on Linux and Windows, or use the Edit menu. Each image appears as a
  removable attachment (maximum 20 MiB) and is sent as multimodal content.
  Clipboard images stay in memory while drafted; ordinary text paste is unchanged.
  User images remain visible after reopening a conversation, with a fallback
  label when a preview exceeds the transcript image safety limits;
- slash commands: enter `/command arguments` in the composer, or choose an
  action from Cmd/Ctrl+K. Commands requiring arguments are placed in an empty
  composer for editing. Finish the current run and remove attachments before
  running a slash command;
- approvals: consent and trust questions answered inline; closing the window
  fails every pending question closed;
- configuration: a shared Settings page for provider, model, base URL, protocol
  and named credentials, plus GUI-local System/Light/Dark appearance and
  persistent font scaling, and live model/reasoning/approval controls;
- proxy: Settings → Proxy edits the same `proxy.toml` as CLI `/proxy`. The
  suggested URL is `http://127.0.0.1:7897` (a Clash Verge mixed-port address); adjust
  it to the local proxy in use. Browser and individual providers are selected
  independently. GUI update checks, GUI package downloads, and official Lark/
  Meegle downloads automatically use the configured address; a broken proxy
  produces an error rather than silently switching to a direct connection.
  Without a Golder proxy, downloads retain their default/environment route. Provider
  changes apply to subsequent requests; close and reopen an existing browser to
  change its route. The Downloads card shows the effective route and whether its
  address comes from saved GUI settings or `GOLDER_PROXY`; editing the address
  alone does not change this loaded/saved status. No environment variable is
  needed for normal GUI configuration. `GOLDER_PROXY`, when set, overrides the saved URL. Browser
  proxy URLs cannot include credentials or paths.
- extensions: a native manager for checksummed first-party package install,
  exact source review, named-secret configuration, browser OAuth, safe
  phase-coded failures, one-click helper update/rebinding, separate bundled /
  installed / active and application/user-auth status, test, enable/disable,
  reset and uninstall; installed native Lark/Project CLIs are auto-detected, and
  pinned official binaries can be downloaded and integrity-checked directly
  without requiring npm or Node. Both downloads show inline byte/percentage
  progress (bytes only if the server omits the size), connection/checksum/
  extraction/install stages, cancellation, and errors beside the install
  buttons. Successful installation selects that verified CLI and auto-discovers
  the other empty path field without overwriting another tool's custom path or
  edits made during download. Review and save explicitly; installation never
  saves credentials, enables a source, or starts authorization. Other configured
  sources appear there too;
- files: a custom, project-scoped Files drawer with lazy folder expansion and a
  bounded read-only preview; generated directories are hidden and symlinks are
  shown but never followed;
- changes: a Git status + unified diff drawer rendered by MyGo's diff viewer,
  opened from a compact icon-only title action;
- actions: a command palette over the existing slash registry, so skills,
  prompt templates and plugin commands stay reachable without typing;
- browser: an Agent-visible built-in tool backed by the GUI browserhost, with
  native Bitmap frames in an automatically revealed right drawer and mouse,
  keyboard, scroll, resize and committed IME input forwarded through a private
  operator connection. Conversation-local sockets and tab sets share one
  persistent project profile without switching other GUI/TUI sessions' pages;
  the controller and Agent share one session binding, and manual GUI/TUI tab
  selection updates the host foreground used by the next untargeted Agent call.
  Explicit background operations and already prepared calls keep their exact
  targets. The drawer observes the binding, never transcript/tool-completion
  events; stale discovery cannot cross a conversation switch. See
  [browser binding](../docs/browser-binding.md). Title and approval actions use
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
