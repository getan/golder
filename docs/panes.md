# Panes: terminal, browser, charts and the workspace

golder drives four kinds of pane through four narrow tools. Each has its own
schema and its own permission shape; none of them is a generic verb bridge.

| Tool | Backend | What it does |
|---|---|---|
| `tuios` | tuios daemon | terminal panes: `list`, `capture`, `wait`, `run`, `send`, `press`, `screenshot`, `new`, `focus`, `move`, `resize`, `rename`, `minimize`, `close` |
| `browser` | kitty-browse control socket | page automation: `list`, `snapshot`, `find`, `get`, `is`, `wait`, `dialog`, `console`, `errors`, `network`, `screenshot`, `navigate`, `click`, `fill`, `type`, `press`, `select`, `scroll`, tabs and dialogs |
| `visual` | Go (charts), tuios (diagrams/TML) | `chart`, `diagram`, `dashboard_open`, `dashboard_patch`, `dashboard_close` |
| `workspace` | tuios + browser | `create`, `list`, `snapshot`, `focus`, `close` |

## Registration

- `tuios` and `workspace` appear only inside a real tuios pane (`TUIOS_ENV=1`,
  a socket, a session and a pane id).
- `browser` appears when `KITTY_BROWSE_SOCKET` names a control socket, or
  inside a tuios pane, where `workspace create` can start one. The socket must
  be an owner-only Unix socket (`0600`) and not live in a world-writable
  directory; otherwise the tool refuses it.
- `visual` is always present: `chart` is pure Go, and `diagram`/`dashboard`
  say what they need when it is missing.

## Capabilities: presence is fixed, content follows the backend

What a tool *advertises* changes with what the session can actually do, but the
tool list itself does not flap:

- **Presence** is decided once, when the tool set is built. A tool that is
  registered stays registered for the session.
- **Content** is recomputed per request. `browser` advertises only `list` (and a
  "start one with workspace create" description) until a socket exists, then
  grows to the full set. `visual` advertises `chart` alone until `tuios` is on
  `PATH`, then adds `diagram` and the dashboard actions.
- **No polling.** The one event that can create a capability mid-session is
  `workspace create` starting a browser, and that is where the socket is
  recorded. A background probe would flip the schema — and invalidate the
  provider's prompt cache — for a change nobody asked for.
- **Validation is wider than advertising.** The registry compiles the full
  schema once, so a `click` or `diagram` that becomes possible after the tool
  was registered still validates. Only the *advertised* enum narrows.

## Permissions

All four are host-control tools: they do not run inside the OS file sandbox, so
a `contain` verdict can only block them, never isolate them. `judge.FirstParty`
sorts their actions into fixed tiers:

| Tool | Local allow | Reviewer decides |
|---|---|---|
| `tuios` | `list`, `capture`, `wait`, an empty `new` | `run` (graded as bash), `send`, `press`, `screenshot`, `new` with argv, `focus`/`move`/`resize`/`rename`/`minimize` (confirm), `close` |
| `browser` | `list`, `snapshot`, `find`, `get`, `is`, `wait`, `dialog`, `console`, `errors`, `network`, `screenshot` | `navigate`, `click`, `dblclick`, `hover`, `focus`, `check`, `uncheck`, `fill`, `type`, `select`, `press`, `insert`, `scroll`, `scroll_into_view`, `tab_*`, `dialog_accept`/`dialog_dismiss` |
| `visual` | `chart`, `diagram` | `dashboard_open`, `dashboard_patch`, `dashboard_close` |
| `workspace` | `list`, `snapshot` | `create`, `focus`, `close` |

`browser`'s `eval` and `upload` are not in the schema at all and are denied if a
call carries them. Sub-agents get the read shape of each tool:

- `tuios`: `list`, `capture`, `wait`;
- `browser`: the read actions above;
- `visual`: `chart` only;
- `workspace`: `list`, `snapshot`.

## Charts

`visual chart` renders terminal cells and returns them in the tool result, so
the model reads the numbers immediately. Kinds: `bar`, `line`, `sparkline`,
`histogram`, `pie`, `gauge`, `heatmap`. Data is inline JSON (an array of
numbers, or an array of row objects) or a CSV/JSON file in the workspace:

```json
{"action":"chart","kind":"line","file":"data/sales.csv","x":"month","y":"revenue,cost","title":"Revenue vs cost"}
```

`out` writes the text under `.golder/artifacts/charts/`; any path outside the
workspace is refused.

## Diagrams

`visual diagram` writes the Mermaid source to the workspace and runs the
installed `tuios mermaid` on it:

```json
{"action":"diagram","source":"graph TD; A-->B","format":"svg","out":"artifacts/flow.svg"}
```

`format` is `text` (the diagram in the result), `svg` or `png`. Without tuios the
tool says so instead of failing obscurely.

## Dashboards

`dashboard_open` sends a TML document to the daemon (`target` is `pane` or
`popup`) and returns a surface id. `dashboard_patch` applies an RFC 6902 JSON
Patch to the surface's state, and `dashboard_close` closes it. TML is the live
shape: a data-analysis panel can be patched as numbers change.

## The workspace presets

| Preset | Panes |
|---|---|
| `debug-fullstack` | `backend`, `frontend`, `browser` (opens `url`, default `http://localhost:3000`) |
| `data-analysis` | `analysis` |
| `diagram` | `diagram` |
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
