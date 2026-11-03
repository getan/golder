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
go run github.com/egoist/mygo/cmd/mygo build -platform darwin/arm64
go run github.com/egoist/mygo/cmd/mygo dev     # rebuild and restart as the code changes
```

The app is named **Golder GUI** (slug `golder-gui`), so its Linux executable
never collides with the `golder` CLI. The Agent runtime is linked into the GUI;
the bundle deliberately carries neither a second full CLI nor tuios-mini.
Artifacts, size gates, signing secrets and the Gatekeeper tradeoff are described
in [`docs/gui-packaging.md`](../docs/gui-packaging.md).

Two optional surfaces need runtime pieces:

- `libghostty-vt` for the terminal plugin, installed by `mygo build` (a
  `go run` binary downloads it once into the user's cache);
- a running kitty-browse/browserhost owner-only control socket for the native
  interactive Browser Surface. Chromium itself is never bundled.

## What the window does

- sessions: project/path grouping, new/resume/rename/archive/delete/export, and
  a writer lock that refuses a second process on the same session;
- turns: streamed Markdown, thinking (collapsed), tool cards with bounded
  output, compaction and provider notices, cancel;
- approvals: consent and trust questions answered inline; closing the window
  fails every pending question closed;
- configuration: a shared Settings page for provider, model, base URL, protocol
  and named credentials, plus live model/reasoning/approval controls;
- extensions: a native manager for checksummed first-party package install,
  exact source review, named-secret configuration, browser OAuth, test,
  enable/disable, reset and uninstall; other configured sources appear there too;
- changes: a Git status + unified diff drawer rendered by MyGo's diff viewer;
- actions: a command palette over the existing slash registry, so skills,
  prompt templates and plugin commands stay reachable without typing;
- browser: native Bitmap frames with mouse, keyboard, scroll, resize and
  committed IME input forwarded through the typed control socket;
- terminal: an explicit user shell and detachable takeover of Agent bash calls
  that ran with `tty=true`.

Deliberately absent: tuios workspace/pane management and a full IDE. Those
remain CLI/TUI responsibilities.
