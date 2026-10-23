# Panes: terminal, browser and the workspace

golder drives three kinds of pane through three narrow tools. Each has its own
schema and its own permission shape; none of them is a generic verb bridge.

| Tool | Backend | What it does |
|---|---|---|
| `tuios` | tuios daemon | terminal panes: `list`, `capture`, `wait`, `run`, `send`, `press`, `screenshot`, `new`, `focus`, `move`, `resize`, `rename`, `minimize`, `close` |
| `browser` | kitty-browse control socket | page automation: `list`, `snapshot`, `find`, `get`, `is`, `wait`, `dialog`, `console`, `errors`, `network`, `screenshot`, `navigate`, `click`, `fill`, `type`, `press`, `select`, `scroll`, tabs and dialogs |
| `workspace` | tuios + browser | `create`, `list`, `snapshot`, `focus`, `close` |

## Registration

- `tuios` and `workspace` appear only inside a real tuios pane (`TUIOS_ENV=1`,
  a socket, a session and a pane id).
- `browser` appears when `KITTY_BROWSE_SOCKET` names a control socket, when a
  workspace browser socket is discoverable, or
  inside a tuios pane with kitty-browse on `PATH`, where `open` can start one.
  The socket must be an owner-only Unix socket (`0600`) and not live in a
  world-writable directory; otherwise the tool refuses it.

## Capabilities: presence is fixed, content follows the backend

What a tool *advertises* changes with what the session can actually do, but the
tool list itself does not flap:

- **Presence** is decided once, when the tool set is built. A tool that is
  registered stays registered for the session.
- **Content** is recomputed per request. `browser` advertises `open`, `list` and
  `attach` until a socket is selected, then
  grows to the full set.
- **No polling.** The one event that can create a capability mid-session is
  `workspace create` starting a browser, and that is where the socket is
  recorded. A background probe would flip the schema — and invalidate the
  provider's prompt cache — for a change nobody asked for.
- **Validation is wider than advertising.** The registry compiles the full
  schema once, so a `click` that becomes possible after the tool was registered
  still validates. Only the *advertised* enum narrows.

## Opening a browser

kitty-browse needs three things nobody should have to remember — a profile
directory, a control socket, and the URL. `browser open` supplies all three:

```json
{"action":"open","url":"http://localhost:3000"}
```

Inside a tuios pane it starts kitty-browse in a new pane (profile and socket
under `.golder/browser/`), waits for the socket, and adopts it for the session.
New helper panes explicitly target the workspace containing golder's own pane,
resolved by its pane id, never the user's focused workspace. They do not steal
focus. `workspace create` uses the same rule for all its helper panes. If the
agent pane cannot be located, creation fails rather than guessing. Reusing an
existing browser does not create or move a pane; with multiple unselected
instances, attach explicitly instead of guessing based on focus.
The schema then grows from `open`/`list`/`attach` to the full action set. Calling it
again while a browser is running returns the existing socket with
`started: false`.

Outside a tuios pane there is nowhere to draw a browser, so `open` says that
instead of launching something that cannot render. A browser started elsewhere
is still reachable: point `KITTY_BROWSE_SOCKET` at its socket.

### Discovering and attaching existing panes

`browser list` probes the configured endpoint plus socket files immediately in
`.golder/` and `.golder/browser/`. It returns live `instances` (socket, browser
identity and tabs), unavailable endpoints, and the current selection. Probes
retain the owner-only socket checks and have a three-second total budget. They
do not recurse into profiles, follow workspace symlinks outside the project,
or unlink an endpoint on failure. `truncated: true` means the bounded inventory
may be incomplete.

Select an existing instance with its identity, not a filesystem path:

```json
{"action":"attach","browser":"browser-id-from-instances-info"}
```

Attachment re-probes the instance, clears the previous tab choice, and overrides
the startup environment for this tool. The identity is pinned: reusing a socket
path for a different browser requires explicit reattachment. `list` is read-only
and never switches selection. `open` reuses a valid selection or a single live
candidate; with multiple candidates and no valid selection it returns the list
instead of choosing or launching another pane. Failed actions are never replayed
on another browser. A failed selected connection does not mean every visible
browser pane has exited. Sub-agents may list instances but cannot attach.

### Closing a browser pane

For a pane whose foreground program is `kitty-browse`, `tuios close` first
requests normal browser exit instead of immediately invoking tuios' hard
`close-window`. `workspace close` uses the same path for its browser panes,
including when the daemon omits the foreground label. A changed foreground
program is not sent browser shortcuts based on the old workspace record.

Golder sends Escape keys to leave browser input modes, then Ctrl+Q in a
separate write after the terminal's Escape ambiguity window. It waits for
the daemon's `window-exit` event (10 seconds by default,
adjustable with `timeout_ms`). Only after process exit does it close any
remaining pane; an automatically removed pane needs no second close. The
result reports `close_mode` and the exit code when known, without claiming
that a nonzero exit was graceful. Other terminal panes retain ordinary close
behavior. Titles and the selected browser socket never choose a close target.

If browser exit cannot be confirmed, the tool returns an error without a hard
close. An explicit `force:true` permits the fallback, reported as
`close_mode: forced`. Cancellation never triggers that fallback. This policy
applies only to closes through golder, not tuios' own close key, daemon crashes
or machine shutdown. It neither deletes persistent profiles nor prevents
Chromium from downloading components into them.

## Permissions

All four are host-control tools: they do not run inside the OS file sandbox, so
a `contain` verdict can only block them, never isolate them. `judge.FirstParty`
sorts their actions into fixed tiers:

| Tool | Local allow | Reviewer decides |
|---|---|---|
| `tuios` | `list`, `capture`, `wait`, an empty `new` | `run` (graded as bash), `send`, `press`, `screenshot`, `new` with argv, `focus`/`move`/`resize`/`rename`/`minimize` (confirm), `close` |
| `browser` | `list`, `snapshot`, `find`, `get`, `is`, `wait`, `dialog`, `console`, `errors`, `network`, `screenshot` | `open`, `attach`, `navigate`, `click`, `dblclick`, `hover`, `focus`, `check`, `uncheck`, `fill`, `type`, `select`, `press`, `insert`, `scroll`, `scroll_into_view`, `tab_*`, `dialog_accept`/`dialog_dismiss` |
| `workspace` | `list`, `snapshot` | `create`, `focus`, `close` |

`browser`'s `eval` and `upload` are not in the schema at all and are denied if a
call carries them. Sub-agents get the read shape of each tool:

- `tuios`: `list`, `capture`, `wait`;
- `browser`: the read actions above;
- `workspace`: `list`, `snapshot`.

## Why there is no `visual` tool

`visual` shipped with three action families and two of them could not work
against any released tuios: `diagram` shelled out to `tuios mermaid`, and
`dashboard_open`/`patch`/`close` called the daemon's `tml-open`/`tml-patch`/
`tml-close` verbs. Both landed on tuios `main` after v0.10.1, and v0.10.1 is the
latest release, so `hasTuiosBinary()` passed on a binary that had neither. The
schema advertised the actions and every call failed at run time with
`unknown command "mermaid"` or `unknown_verb`.

`chart` was golder's own code and worked, but a chart-only tool did not earn a
slot in the tool list while its siblings were dead weight. The whole tool is
removed until a stable tuios ships both. When it is restored, probe the
subcommand and the verb, not the binary's presence: a `LookPath` hit is not a
capability.

## The workspace presets

| Preset | Panes |
|---|---|
| `debug-fullstack` | `backend`, `frontend`, `browser` (opens `url`, default `http://localhost:3000`) |
| `data-analysis` | `analysis` |
| `review` | `tests`, `browser` |

The browser pane runs kitty-browse with `-profile-dir` and `-control-socket`
under `.golder/browser/<name>.sock`, so `browser` and `workspace snapshot` find
it without extra configuration.

`workspace snapshot` returns one block: each terminal pane's tail (marked
untrusted) and each browser pane's URL/title, interactive accessibility
snapshot, console errors and recent requests. That is the full-stack debugging
loop:

1. `workspace create debug-fullstack url=http://localhost:5173`;
2. reproduce the bug;
3. `workspace snapshot` → backend log tail + frontend console/network + page tree;
4. `browser click` / `browser snapshot` to verify;
5. `tuios run go test` / `browser wait` to re-check;
6. snapshot again.
