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
go run github.com/egoist/mygo/cmd/mygo build -platform darwin/universal
go run github.com/egoist/mygo/cmd/mygo dev     # rebuild and restart as the code changes
```

The app is named **Golder GUI** (slug `golder-gui`), so its Linux executable
never collides with the `golder` CLI. A packaged app carries the CLI in its
resources (`resources/<os>-<arch>/bin/golder`), which the `gui` workflow fills
before packaging. Artifacts, signing secrets and the Gatekeeper tradeoff are
described in [`docs/gui-packaging.md`](../docs/gui-packaging.md).

The terminal workspace window and the browser preview need native pieces at
runtime:

- `libghostty-vt` for the terminal plugin, installed by `mygo build` (a
  `go run` binary downloads it once into the user's cache);
- a `golder` binary on `PATH` (or `GOLDER_BIN`) for
  `golder workspace attach`;
- a running browser pane's owner-only control socket for previews.

## What the window does

- sessions: new, resume, switch, and a writer lock that refuses a second
  process on the same session;
- turns: streamed Markdown, thinking (collapsed), tool cards with bounded
  output, compaction and provider notices, cancel;
- approvals: consent and trust questions answered inline; closing the window
  fails every pending question closed;
- configuration: model picker with the provider catalog, reasoning level,
  approval mode, context gauge;
- changes: a Git status + unified diff drawer rendered by MyGo's diff viewer;
- actions: a command palette over the existing slash registry, so skills,
  prompt templates and plugin commands stay reachable without typing;
- workspace: list and open an existing `golder workspace` in a terminal
  window (the daemon, PTYs and VT remain tuios-mini's);
- browser: discover live control sockets and show a read-only PNG preview.

Deliberately absent: an embedded tuios workspace (that needs a snapshot+raw
stream protocol shared by two VT emulators), and a full IDE.
