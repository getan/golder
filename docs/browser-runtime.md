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
`CFFIXED_USER_HOME`), a dedicated profile and no daemon/provider credentials.
The pinned allocator uses basic password storage and a mock macOS keychain.
These precautions do **not** impose an outer filesystem deny policy on
Chromium's main process; it is a trusted browser binary, not an arbitrary shell.
Control stays on the native, owned CDP endpoint. Ordinary pane input cannot
be used to turn the exception into a terminal escape.

## Startup diagnostics

The child writes an owner-only, atomic startup report beside its dedicated
profile. Raw startup output is bounded to 16 KiB, redacted before persistence,
and stopped/cleared once the about:blank anchor connects to CDP, before the
first user-page navigation. Normal page, console and keyboard traffic is not a
startup log.

The parent watches both startup status and the actual pane-exit event. It
preserves failed-pane output until diagnosis, then closes the failed owned
pane. Errors distinguish missing executables, runtime permission failures,
browser crashes, and terminal/renderer initialization. A readiness timeout is
not presented as proof that Chromium is missing or still running.

CDP readiness, terminal initialization and rendering are different milestones.
On this development Mac, installed Chromium 150 successfully connected to CDP,
read about:blank DOM and navigated to a synthetic local page outside Seatbelt.
Its `Page.captureScreenshot` still timed out after 10 seconds, with no matching
Seatbelt denial observed. This **post-CDP screenshot acceptance remains open**;
the opt-in `TestBrowserRuntimeChromium` retains the check rather than treating
HTTP 500 as success. No disabling of Chromium's sandbox is a workaround.

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
