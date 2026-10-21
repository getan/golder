# tuios pane tools

golder exposes five narrow tools when it runs inside a tuios pane. They are
registered only when `TUIOS_SOCKET` and `TUIOS_SESSION` are present; a normal
terminal never sees them. They bind to the caller's own session, so a model
cannot name another session.

| Tool | Purpose | Approval |
|---|---|---|
| `tuios_list_windows` | every pane in the session, across workspaces | local allow |
| `tuios_capture_pane` | one visible/recent snapshot | local allow |
| `tuios_wait_for` | block on a daemon event | local allow |
| `tuios_run` | type one shell command and return its result | classified as `bash` |
| `tuios_window` | new / focus / close a pane | new/close reviewer, focus local |

The first three are read-only and may run under `/permissions read-only`.
`tuios_run` is a shell command, so it is graded by the same JEV/local/LLM path
as `bash`; it is not a bypass. `tuios_window close` is left to the contextual
reviewer because it can kill a running task, and it refuses the pane golder
itself runs in.

## Event-driven, not polling

`tuios_run` has two modes:

```json
{"window":"build","command":"go test ./..."}
```

waits for the shell's OSC 133 command-finished mark and returns `exit_code` and
`output`. With `expect`, it sends the command and then blocks in tuios'
`wait-for` event subscription; it returns as soon as the pane output matches:

```json
{"window":"build","command":"make","expect":"PASS|FAIL"}
```

`tuios_wait_for` exposes the same event path directly for `window-output`,
`window-idle`, `window-exit`, `command-finished` and `agent-state`.
`tuios_capture_pane` is only a snapshot; it is not a poll loop.

## Existing panes

The tools are not limited to panes golder created. `tuios_list_windows` reports
every pane in the current session, and capture/run/wait/focus/close accept any
window id from that list. `tuios_window close` requires the full id and does
not resolve a short name or prefix.

## Context cost

golder does not have lazy tool discovery today: every registered tool's schema
is sent to the model each turn. The five schemas are fixed and compact, and
outside tuios they are not registered at all. If context pressure becomes
real, the next step is either one `tuios` action facade or a general tool
search layer, not per-session registration tricks.

Task children inherit the three read-only tools (`list`, `capture`, `wait`) but
not `tuios_run` or `tuios_window`. The reason is coordination, not sandboxing:
`tuios_run` types into a shared pane and `tuios_window` rearranges the shared
workspace, so parallel children could write into each other's panes or steal
focus. The parent session keeps those two; this default is enforced by the
child tool set, not by `--allowed-tools`, and the daemon's pane grants still
apply.

## Pane creation

`tuios_window action=new` creates a pane with an optional argv (never a shell
string) and a fixed caller session. Use it to open a shell or a specific
program; use `tuios_run` for commands in an already-running shell.
