# Controlled browser runtime on macOS

## Execution and authorization

Controlled Chromium runs outside **outer Seatbelt** on macOS. Chromium's own
sandbox remains required; the launcher explicitly prevents chromedp's automatic
`--no-sandbox` fallback, including when run as root. Normal terminal and bash
helpers keep their existing isolation.

This is a browser-specific capability, not `Direct`/unconfined bash authority.
It is produced by trusted launch classification, never a model argument:

| Gate | Browser runtime exception |
|---|---|
| Auto, reviewer allow/confirm | Granted for the browser launch; no extra human prompt |
| Auto, contain/deny/failed review | Refused |
| Ask | Explicit browser-specific approval |
| Read-only / sandbox enforce | No launch exception |

The process gets a scrubbed environment, a private HOME (including
`CFFIXED_USER_HOME`), a stable project-default profile outside the checkout and
no daemon/provider credentials. The person and project agents intentionally
share that default. Explicitly authorized named profiles provide separate login
identities.

The project folder is not `GOLDER_HOME`: the latter stores Golder's private
configuration, credentials and browser profiles (normally `~/.golder`). If the
workspace is the entire user home, `GOLDER_HOME` itself, or another ancestor of
the chosen profile, the browser currently refuses to start rather than put
saved logins under the workspace. A first GUI launch without a saved project
can fall back to the user home and encounter this restriction. Select an actual
project subfolder in the GUI, then request the controlled browser again; do not
automatically move all of `GOLDER_HOME`, relax profile isolation, or silently
substitute the person's unmanaged/system browser. Opening a URL externally is
not equivalent to giving the agent a controlled page it can inspect or operate.

The profile owner is a detached shared broker, not the GUI or the first TUI
session. Each conversation has its own automation socket and tab set. Its
owner-only management socket is separate from the model-facing API; an
authenticated per-view operator capability carries human keyboard, pointer and
browser-chrome operations. These credentials are never placed in argv, tool
results or transcripts. Automation cannot call an operator action to bypass a
verification pause. The TUI surface connects to the same broker as the GUI
Bitmap surface, so shutting down one frontend cannot close another's views.
The pinned allocator uses basic password storage and a mock macOS keychain.
That cookie-encryption choice is part of profile compatibility, not just a launch
optimization. Ordinary macOS Chromium defaults to the system Keychain, so opening
the same `user-data-dir` in a GUI process can write cookies the controlled runtime
cannot decrypt. A GUI-only verification fallback must start only after the owned
browser releases the profile, use `--password-store=basic` and
`--use-mock-keychain`, and keep the same saved browser proxy route. It must exit
completely before the controlled pane reopens. `browser status` exposes these
non-secret runtime facts and the derived authorized profile path; it never reads
or returns cookie names or values.
These precautions do **not** impose an outer filesystem deny policy on
Chromium's main process; it is a trusted browser binary, not an arbitrary shell.
Control stays on the native, owned CDP endpoint. Ordinary pane input cannot
be used to turn the exception into a terminal escape.

The allocator disables Chromium component updates (including optional speech
and on-device optimization models). Every persistent profile startup removes a
closed list of regenerable caches after taking the profile lock; login-bearing
state and downloads are not touched. kitty-browse's explicit `-private` mode
still uses an owner-marked temporary tree, but it is not Golder's default.

## TUI pane shutdown and exited-pane recovery

For a browser pane newly created for a task, call `tuios close` with its recorded
full pane ID first. It requests graceful browser shutdown, waits for process
exit, and removes the pane. Do not send `browser quit` first; inspect the close
result and confirm that the pane disappeared. Keep adopted/user-owned browsers
and browsers paused for verification untouched unless the operator requests it.

If `browser quit` was already sent prematurely, the model may make **one**
explicit `tuios close` with `force:true` solely to remove an exited empty pane,
but only after all of the following:

1. A fresh, non-truncated `browser list` shows no live instance for the recorded
   browser ID or socket. Socket disappearance alone, permission failures and
   probe timeouts are not sufficient proof.
2. A fresh `tuios list` marks the exact full pane ID `controlled=true`,
   `control_origin=agent-created`, and `own=false`.
3. `tuios wait condition=window-exit` for that ID returns `matched=true`, and
   another listing confirms no foreground process remains. A blank
   `foreground_cmd` or idle screen alone is **unknown**, not exit evidence.

If the pane is already absent, no force call is needed. Verify removal after
the one cleanup attempt; do not loop force calls or repeat quit. Any uncertain
proof, new foreground process, adopted/claimed/user-owned pane, agent/approval
pane, or live/paused browser means stop and report. This is a narrow TUI prompt
recovery rule, not an automatic backend force fallback or a permission bypass.
GUI browser views are not tuios panes, and removal of a TUI pane does not prove
the shared Chromium process or profile has exited.

## Startup diagnostics

The child writes an owner-only, atomic startup report in the workspace's small
browser state directory and Golder removes it after startup resolves. Raw output
is bounded to 16 KiB, redacted before persistence,
and stopped/cleared once the about:blank anchor connects to CDP, before the
first user-page navigation. Normal page, console and keyboard traffic is not a
startup log.

The parent watches both startup status and the actual pane-exit event. It
preserves failed-pane output until diagnosis, then closes the failed owned
pane. Errors distinguish missing executables, runtime permission failures,
browser crashes, and terminal/renderer initialization. A readiness timeout is
not presented as proof that Chromium is missing or still running.

CDP readiness, terminal initialization and rendering are different milestones.
The follow-up comparison on this development Mac used fresh profiles and
the same scrubbed environment, outside Seatbelt but with Chromium's own sandbox
required in every arm:

| Runtime | CLI / bare CDP screenshot | Controlled host |
|---|---|---|
| Installed Chromium 150.0.7854.0 | Times out, including stock-feature repeats | DOM/navigation work; capture times out |
| Official full Chrome for Testing 150.0.7871.46 | Times out; stock-feature runs sometimes succeed but are not reliable | Not accepted as a reliable capture runtime |
| Official chrome-headless-shell 150.0.7871.46 | CLI and bare CDP decode valid PNGs | Three fresh sessions pass viewport, full-page, repeated viewport and normal quit |

The successful host check decodes actual PNGs, verifies 1280x800 / 1280x1600
dimensions and the fixture's opaque `#115599` pixel. It does not accept an empty
image or a blank fallback. Focus/layout preparation and `CDPScreenshotNewSurface`
did not make full Chrome reliable. An initial successful stock-feature run
suggested `MacAppCodeSignClone`, but repetition disproved it as a sufficient
fix. **No default feature or sandbox flags were weakened based on that result.**
The exact upstream compositor defect remains unproven; the failing native CLI
and bare CDP arms show that the failure does not require golder's control API.
Related upstream reports: [agent-browser #1437](https://github.com/vercel-labs/agent-browser/issues/1437)
and [the proposed runtime workaround #1189](https://github.com/vercel-labs/agent-browser/pull/1189).

### Explicit runtime selection

For an affected machine, a dedicated headless runtime can be selected using the
existing host setting, without replacing `/Applications/Chromium.app`:

```sh
KITTY_BROWSE_CHROME="/absolute/path/to/chrome-headless-shell" /path/to/new/golder
```

The controlled launcher resolves this setting to one literal `-chrome` argument
before scrubbing the child environment. Previously the setting was silently
lost, so a requested engine could fall back to the problematic full browser.
An invalid explicit setting now fails rather than silently choosing another
engine. Neither ambient credentials nor arbitrary tool-supplied argv are
forwarded. The default browser selection remains unchanged; there is no
automatic download, installation or runtime switch.

Tests accept `GOLDER_TEST_CHROMIUM_EXECUTABLE` to compare an explicit binary.
`GOLDER_TEST_CHROMIUM_RUNTIME=1` runs full capture/quit acceptance;
`GOLDER_TEST_SCREENSHOT_LAYERS=1` runs CLI/bare-CDP comparison, with optional
`GOLDER_TEST_SCREENSHOT_MODES=cdp-stock,cdp-no-clone` for the feature A/B test.
The default installed-browser failure remains reproducible; configuring the
verified headless runtime is an operational workaround, not a claim that full
Chrome itself has been fixed. No Chromium sandbox disablement is a workaround.

## Separate Toggle admin / descriptor issue

The live tuios client reached 10,000 descriptors while its daemon used about
16. macOS fsnotify/kqueue opens files under watched directories, so a directory
count limit did not bound descriptor use. The macOS Git watcher now uses
recursive FSEvents with constant descriptor usage and explicit teardown. Its
regression test lowers the process limit to 128, creates 1,500 files and repeats
watch/edit/close cycles. Linux retains inotify; unsupported kqueue targets use
timed reconciliation rather than an unbounded fallback.

Admin toggling now stops if reading current/default grants fails; it never
guesses grants after an IPC error. A running old client must detach and
reattach after updating to release its existing kqueue descriptors. Restarting
the shared daemon or increasing the descriptor limit is not the fix.
