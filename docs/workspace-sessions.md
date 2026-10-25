# Global names, independent workspace daemons

`golder workspace` addresses a **named session**, not a project checkout. A
session is a persistent name for one independent daemon and the numbered
workspaces/panes it owns. The global catalog is a directory of records, not
another always-running manager process.

## Commands

| Command | Meaning |
| --- | --- |
| `golder workspace` / `run` | Start `default` if stopped/missing, otherwise attach |
| `golder workspace -s coding` | The same operation for `coding` (`--session` is equivalent) |
| `golder workspace new coding` | Create and attach; fail if the name already exists, even when stopped |
| `golder workspace attach coding` | Connect to a running owner only; never create records or a daemon |
| `golder workspace list [--json]` | Discover all global and legacy sessions, from any cwd |
| `golder workspace status [name]` | One JSON status document; omitted name means `default` |
| `golder workspace stop [name]` | Authenticated stop of that daemon and its panes; retain its record |
| `golder workspace rm [name]` | Remove a stopped named session's directory; refuse running/unreachable/legacy sessions |

Names match `[a-z0-9][a-z0-9_-]{0,63}`. There is no case folding or sanitising
of user names. `default` uses exactly the same paths and lifecycle as other
names. A positional name belongs to new/attach/status/stop/rm; run uses `-s`
to avoid confusing a session called `list` with the subcommand.

`--shell`, `--headless`, `--config`, `--state-dir`, `--socket` and arguments
after `--` are start options, not attach/status/stop options. A recorded
socket/config/state override must agree with the record; it cannot retarget an
existing name. Appearance flags apply to run/new/attach. Visible commands
refuse nesting in another multiplexer unless `--nested` is explicit.

## Directories are not identities

- First creation uses the invocation directory, or `-C/--cwd`, as its saved
  startup cwd and initial pane cwd.
- Attach ignores the invocation cwd and never changes a live pane's cwd.
- A running session rejects `-C`, `--shell` and agent argv, rather than
  pretending those flags changed it.
- Restarting a stopped session uses its saved cwd. `-C` explicitly changes
  that saved value; if the old directory disappeared, restart asks for `-C`
  instead of silently using home or the invocation cwd.
- List/attach/status/stop do not stat the saved project directory. A moved or
  deleted project must not prevent inspecting or stopping its live session.
- New panes follow the terminal backend's cwd inheritance policy, with the
  session startup cwd as fallback; tools can supply explicit per-pane cwd.
  Each agent keeps its own project rules, grants and sandbox root. A named
  session is neither a permissions root nor a git worktree.

## Storage and ownership

```text
$GOLDER_HOME/workspace/
  .locks/<name>.lock             stable name lease; never unlinked by rm
  sessions/<name>/
    session.json                version, name, cwd, endpoints/paths, created_at
    config.toml
    state/                      daemon state, harness and client preferences
    workspace.log
    owner-<socket-hash>.json     private proof for one live daemon instance
```

The state root defaults to `~/.golder`. Names are global **within that user and
state root**, not across other users or `GOLDER_HOME` values. Socket basenames
hash the canonical state root and name, never the project cwd, and live in a
short private per-user runtime directory. The actual socket and its managed
parent are recorded, so changing `TMPDIR` does not hide a live daemon.

Each daemon holds both the stable name lease and its state-directory lease.
Concurrent default starts converge on the one verified owner; concurrent new
requests have one winner. Removal cannot unlink the name-lock inode and allow
a second owner to slip through. Custom state-directory leases continue to
prevent two differently named sessions from sharing live daemon state.

Records are private, versioned and atomically replaced. The live owner proof
binds the session directory, name, socket and random runtime instance, not the
calling process's cwd. Status/readiness/list never expose the proof token. A
successful socket connection alone is not evidence of ownership.

`list --json` returns `{"sessions":[...]}`. Each row has `name`, `selector`,
`default`, `legacy`, and `state`; cwd, known running/pane count and errors are
included where available. State is `running`, `stopped` or `unreachable`.
Unreachable rows do **not** contain `running:false`: a timeout, invalid proof,
busy startup or broken metadata is not evidence that a session stopped.
Discovery probes at most four endpoints concurrently with bounded timeouts,
and one broken session does not hide the healthy records.

Detach leaves the daemon and PTY processes alive. Stop ends them and retains
session.json; restart starts fresh processes, not a resurrection of in-flight
work. `rm` deletes only the fixed named-session directory. External config/state
overrides are retained; project files and golder chat history are never removed.

Pane tools remain authenticated and scoped to the session identified by their
`TUIOS_*` environment. A global CLI catalog grants no cross-session pane control
to an agent.

## Legacy sessions

The old layout is `<Home>/workspace/<project-id>/sessions/<session>/`. It is
discoverable but never moved, renamed or automatically chosen as `default`.
Because two old projects can share a name, list shows the qualified selector:

```bash
golder workspace attach --legacy 123456789abc/old-session
golder workspace status --legacy 123456789abc/old-session
golder workspace stop --legacy 123456789abc/old-session
```

The old private owner record supplies its exact endpoint and original project
path; the backend still authenticates it before using it. Missing/ambiguous
owner records are reported as unreachable, not resolved by guessing a runtime
directory. Old daemons keep running during the upgrade. When convenient, detach
or stop them explicitly and create a global named session. New/rm never operate
on legacy state.
