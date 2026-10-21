# tuios pane tool

golder exposes one `tuios` tool when it runs inside a tuios pane. It is a
facade over a closed set of actions, not a generic verb bridge: every field is
defined by the schema, the session is always the caller's own, and destructive
actions require a full window id.

The tool is registered only inside a real tuios pane: `TUIOS_ENV=1`,
`TUIOS_SOCKET`, `TUIOS_SESSION` and `TUIOS_PANE_ID` must all be present. A
normal terminal, or a shell where one of those variables was left behind, never
sees it.

| Action | Purpose | Class |
|---|---|---|
| `list` | every pane in the session, across workspaces | read |
| `capture` | one visible/recent snapshot | read |
| `wait` | block on a tuios event | read |
| `run` | type one shell command and return its result | control |
| `new` | create a pane | control |
| `focus` | focus an existing pane | control |
| `close` | close an existing pane | control |

The read actions are allowed under `/permissions read-only`; control actions
are not. `run` is graded by the same JEV/local/LLM path as `bash`, so it is not
a bypass. `close` is left to the contextual reviewer because it can kill a
running task, and it refuses the pane golder itself runs in.

## Event-driven, not polling

Without `expect`, `run` waits for the shell's OSC 133 command-finished mark and
returns `exit_code` and `output`:

```json
{"action":"run","window":"build","command":"go test ./..."}
```

With `expect`, it sends the command and blocks in tuios' `wait-for` event
subscription; it returns as soon as the pane output matches:

```json
{"action":"run","window":"build","command":"make","expect":"PASS|FAIL"}
```

`action=wait` exposes the same event path for `window-output`, `window-idle`,
`window-exit`, `command-finished` and `agent-state`. `capture` is only a
snapshot; it is never a poll loop.

## Existing panes

The tool is not limited to panes golder created. `list` reports every pane in
the current session, and capture/run/wait/focus/close accept any window id from
that list. `close` requires the full id and does not resolve a short name or
prefix.

## Context and sub-agents

This is one tool schema, not five, so context cost stays flat as actions are
added. The parent session gets the control schema. A task child gets the same
`tuios` name and session but a read-only schema: `list`, `capture`, `wait` only.
Control actions mutate shared pane state, so parallel children should not type
into one another's panes or steal focus by default.

## Pane creation

`action=new` creates a pane with an optional `argv` (never a shell string) and
a fixed caller session. Use it to open a shell or a specific program; use
`action=run` for commands in an already-running shell.
