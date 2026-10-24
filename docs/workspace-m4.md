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
