// Package appsession is the UI-neutral controller behind the Golder desktop
// app. It owns one conversation at a time: the session file, the live run
// configuration, the agent loop, the approval channel and the transcript the
// view renders.
//
// Nothing in this package imports a GUI toolkit. The MyGo view under gui/mygoui
// consumes the Controller through Snapshot, Commands and Events, which keeps the
// orchestration testable without a window and lets the CLI/TUI keep their own
// front-ends unchanged.
package appsession
