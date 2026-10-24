# Integrated workspace: M3–M4 implementation plan

The delivery target is one `golder` executable containing the agent, the
workspace daemon and visible terminal UI, and the browser pane. An existing
tuios or kitty-browse installation remains usable, but neither executable is a
prerequisite for the integrated mode. Chromium remains a browser runtime
prerequisite; golder must not silently download or install it.

## Ownership and build boundary

- Golder owns command dispatch, project identity, state roots, host reporting,
  tool registration, and the lifecycle of the integrated workspace.
- The retained terminal code is exposed by the maintained tuios-mini fork as
  public `mini`, `mini/control`, and `workspace` packages. Its module must have
  the fork's own import path, not impersonate the upstream module.
- Kitty-browse exposes the existing visible pane as `browsepane` and the
  control-compatible headless host as `browserhost`. Golder's `browse` dispatch
  calls the library; tools launch that dispatch in a workspace pane.
- Release `go.mod` files pin remotely resolvable commits. No local `replace`,
  ignored `.golder/` source tree, or developer `go.work` may be required for a
  clean build or `go install`.
- The upstream MIT notices are retained. See `THIRD_PARTY_LICENSES.md`.

The forks remain source dependencies, not separately installed programs. This
keeps provenance and regression tests attached to the retained implementations
while placing all installed runtime entry points in golder. The milestone names
are bookkeeping; no stage is complete merely because a headless command exists.

## Invariants

1. A workspace never reads, cleans, restores, or overwrites a user's tuios
   state or configuration. Two workspaces may have the same session name.
2. Socket and state directories are private. Runtime socket paths fit the
   platform's Unix-domain limit; persistent state is not kept in a cache tree.
3. Workspace identity is per pane, not a process-wide environment mutation.
   Pane identity and proof tokens cannot be overridden through extra pane env.
4. Default pane grants are explicit and bounded. Own-scope proof, read-only
   restrictions, and golder's existing control-verb contract continue to hold.
5. The embedding host owns signals and logging. Library entry points return
   errors, never exit the process, and release their resources on cancellation.
6. A failed control call is not replayed: it may have performed its mutation.
   A broken connection is discarded before the next call. Framing is bounded
   while reading, not after allocating an arbitrary response.
7. Browser shutdown uses its control socket when available. A pane named
   `golder` is not sufficient evidence that it is a browser.
8. Existing external tuios/herdr reporting and external browser attachments
   remain compatible; embedded mode does not exec an absent `tuios` program.

## Work packages and acceptance

- **Core safety:** daemon-owned state/logs, explicit pane policy/env, short
  project-scoped sockets, cancellation/stop, reliable bounded JSON control.
- **Visible workspace:** importable retained TUI, explicit socket/config path,
  a self-hosted agent pane, terminal restoration, and `golder workspace`.
- **Browser:** importable retained renderer, `golder browse`, the existing
  native-CDP API, graceful quit, and self-launch from browser/workspace tools.
- **Core diet:** remove or decouple standalone-only services and split actual
  package responsibilities, with import-graph and permission-contract gates.
- **Shipping:** update help/docs/licenses, build from a clean checkout without
  local workspace files, cross-build without CGO, exercise real terminal and
  browser paths, and inspect CI results for the exact shipped commits.

Tests must distinguish successful compilation, real PTY execution, real
Chromium execution, screenshot/render validation, and race coverage. Missing
graphics or Chromium capabilities are reported as limitations, not passes.
Local Linux-container testing remains suspended until explicitly re-enabled;
Linux CI results must identify the actual run and commit.

## Implemented boundaries and current validation

The normal `golder` entry point is unchanged. `golder workspace` is explicit;
`Ctrl+b d` detaches, and a later invocation attaches to the same live daemon.
`workspace stop` ends its panes. A stop is not a promise to resurrect the old
PTY processes on the next start; chat history remains golder's normal history.
Use a persistent built/installed binary rather than `go run` for a daemon that
must outlive its launching command. Repeat custom `--cwd`, `--session` and
`--socket` identity flags when querying or stopping that workspace.

The default embedded daemon now has a **local-only runtime profile**: no link,
human-link or herdr listener, no federation/fleet/plugin/hook/push/transcript
watcher, and no SSH/display following. It reads bundled harnesses plus golder's
own generated manifest, never the person's tuios harness directory. That
manifest also resumes through the absolute path of this golder executable.
The binary protocol was physically extracted into the standard-library-only
`internal/wire`; JSON control is separately importable without the renderer.

This is not a claim that all standalone source left the binary. The retained
renderer/session implementation still links product packages; the fork's
`mini/imports_test.go` pins that graph with a negative control and
`docs/M4.md` lists the remaining coupling and logger sites. Broad source-level
pruning and further binary-size reduction remain separate from disabling
unrequested services at runtime.

Real macOS PTY tests exercised frame output, detach/re-attach, terminal reset,
literal agent argv, pane identity/proof, strict/read-only control and authenticated
stop. Real Chromium tests exercised viewport/full-page PNG capture, browserhost
actions/quit and failure cleanup. Browserhost screenshot failure is now fatal
when Chromium is available, not a logged pass. Native build/vet and CGO-free
cross-builds are also checked. These are distinct from a manual graphical
Kitty/Sixel terminal inspection, which has not been claimed here.

**Release gate still open:** the fork branches are published, but the attempted
remote Go-module resolution timed out and the narrowed download request was
denied by the authorization gate. The root dependency pins/checksums and a clean
`GOWORK=off` build must be completed before treating this branch as an M4
release. Successful developer-workspace builds do not satisfy that gate. Final
Linux CI/race results must likewise be attached to the actual final commit.
