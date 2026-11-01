# Development cache hygiene

The checkout is source, not a cache root. Large tool caches shared across builds
belong in the user's standard cache directories; short-lived test artifacts
belong in `t.TempDir()`/`TMPDIR` and must have an owner/cleanup path.

## Go

Use the installed Go defaults:

```sh
go env GOPATH GOMODCACHE GOCACHE
```

Do not set `GOPATH`, `GOMODCACHE` or `GOCACHE` to `.build`, `.scratch`, `.dev`, a
worktree or another path under a repository. When the OS sandbox denies the real
cache, authorize that exact external cache path; creating a second cache inside
the checkout can duplicate several gigabytes per worktree.

tuios-mini's `internal/testutil.RunIsolated` pins all three Go directories before
redirecting HOME/XDG, and `just sandbox-env` now exports those same captured
values. The test `TestGoDirsStayOutsideTheTree` guards the test-binary path.
Golder and kitty-browse do not globally redirect HOME around child `go` commands.

## Chromium

Golder does not download Chromium. Opt-in browser tests must point
`GOLDER_TEST_CHROMIUM_EXECUTABLE`/`KITTY_BROWSE_CHROME` at an installed binary or
a shared cache outside all repositories. If an explicit test runtime is fetched,
remove it after that test session; never unpack Chrome for Testing into `.build`.

Fresh controlled browsers use the stable project-shared profile at
`$GOLDER_HOME/browser-profiles/<project-key>/default.profile`. It is outside the
checkout, and Cookie/storage/history/download state intentionally survives for
the person and project agents. Human-approved named profiles live beside it.
kitty-browse's explicit private mode remains owner-marked and self-cleaning, but
Golder does not use it by default.

kitty-browse also disables Chromium component updates. Before opening any
persistent profile it removes only regenerable component, HTTP/code/GPU and
shader caches;
cookies, Local Storage, IndexedDB, history and downloads remain. Legacy workspace
profiles from older Golder versions are pruned after 14 days only when no live
control socket still names them.

## Periodic inspection

Preview before deleting:

```sh
git status --short
git clean -nd
git clean -ndX
du -sh . .build .scratch .golder 2>/dev/null
```

`git clean -ffdX` deletes every ignored file, including local configuration and
diagnostic artifacts, so use it only after reading the preview. It is not part of
normal startup: product code cleans only paths it owns and can classify safely.
