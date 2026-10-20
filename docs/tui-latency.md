# TUI latency diagnostics

When keys are delayed, separate **terminal input → decoder → Update → View →
terminal write → terminal display**. A splash animation finishing at the same
time is not proof that it blocked the keyboard.

## Capture a timing trace

With a build containing this diagnostic, run on the affected machine:

```sh
trace_file=$(mktemp /tmp/golder-tui-timing.XXXXXX)
GOLDER_TUI_TRACE="$trace_file" golder
printf 'Timing log: %s\n' "$trace_file"
```

During the first few seconds, move the trust selection with ↑/↓, answer it, and
type a short dummy string in the composer. Quit without submitting. Note roughly
when each action happened. Repeat once with the mouse **outside** the terminal.

The trace is opt-in and appends to a file created with mode `0600`. It records
relative and UTC timestamps, terminal environment/capabilities, input byte
counts, event **types**, and timings for Update, View and terminal writes. It
does **not** record keys, paste contents, prompts, responses, session IDs,
working-directory paths or credentials. Use a local path such as `/tmp`; a slow
network filesystem can itself distort the measurement. Existing trace files
keep their permissions. Remove the file when finished.

Tracing adds formatting and file-I/O overhead even though operation durations
exclude the diagnostic's begin/end writes; compare with an untraced run too.
Typing rhythm and byte counts are still metadata. The variable is a **path**,
not a boolean. Child processes inherit it, so each line includes the process ID
to distinguish nested TUI runs.

Do not confuse this with upstream `TEA_TRACE`, which records the **raw input and
output**, including everything typed. Do not share a `TEA_TRACE` file without
inspecting it for private content.

### Read the trace

- Long `session begin` → `session end`: session assembly before the UI starts.
- `program begin` → first `input read`/frame: Bubble Tea initialization, including
  color detection. Its default detector can run `tmux info` when `TMUX` is set.
- Input bytes arrive promptly but `KeyPressMsg` arrives late: input decoding or
  queued events. A stream of `MouseMotionMsg` is worth checking.
- Long `update begin` → `update end`: work on the UI goroutine. In particular,
  answering the first-run trust dialog currently wires/dispatches SessionStart
  hooks synchronously; a slow hook can block this particular key.
- Long `view begin` → `view end`: building the screen content is expensive.
- Long `output begin` → `output end`: stdout/PTY back-pressure. Bubble Tea holds
  its renderer mutex during output, so this can also block the event loop.
- All stages quick but the screen changes late: investigate the terminal,
  multiplexer/SSH transport and synchronized-update handling. A successful
  write only means the OS accepted bytes, not that the terminal displayed them.

The trace starts at `tui.Run`, not process launch; earlier CLI configuration,
tool/sandbox setup and skill discovery are outside its timing window. When
stdin is redirected, Bubble Tea retains its own `/dev/tty` fallback; that input
read is not wrapped, so input byte counts are unavailable on that path.
The `io` line records which path was wrapped. `init begin` occurs after Bubble
Tea sets up its input reader. `input begin`/`input read` instrument the actual
file read, **not** the epoll/kqueue wait before it and not the physical keypress.
Thus a gap with no input lines does not by itself prove that SSH or the terminal
sent the bytes late: input polling or scheduling can also be delayed. Correlate
it with the initialization stages and the user's action times.

## Color differences are capability differences

The status bar uses filled segments at ANSI256/TrueColor, and colored text below
256 colors. This is not a Linux/macOS switch. Common examples:

| Terminal environment | Typical detection | Status bar |
| --- | --- | --- |
| `TERM=xterm`, no `COLORTERM` | ANSI (16 colors) | Colored text |
| `TERM=xterm-256color` | ANSI256 | Filled ribbon |
| `COLORTERM=truecolor` with a normal color terminal | TrueColor | Filled ribbon |
| `TERM=linux` (Linux console) | ANSI | Colored text |

SSH and container launchers can lose `COLORTERM` or substitute `TERM=xterm`.
Check the trace's environment and `color profile=` before changing styling.
Only advertise `xterm-256color`/truecolor if the actual terminal and every
intermediary support it; do not force truecolor merely because the OS is Linux.

## Investigation so far

On the local arm64 Linux container, driving the real executable through a
120×40 PTY showed key-to-output latency around 7–23ms while the logo ran. The
trust question also accepted its answer without a multi-second wait. Adding
simulated replies for background color, modes 2026/2027 and Kitty keyboard
negotiation did not reproduce a multi-second startup freeze. This does not
exercise a real terminal emulator or the affected user's host configuration.

A synthetic burst of 3000 mouse-motion events delayed a following key by about
440ms on that container: the app requests all-motion reporting and Bubble Tea
builds View after every dispatched event. Filtering motions that leave the
model unchanged reduced that probe to about 16ms (other probes in the same run
were 5–23ms). The filter preserves status-bar hover transitions, selections,
scrollbar drags, all keys and terminal capability replies. This is an observed queueing cost,
**not confirmation that mouse motion causes the reported startup stall**.

### Visible tuios and real SSH follow-up

The same workspace's right-hand pane was used for a real terminal test, not just
a headless PTY. Path: tuios → Apple container interactive PTY → Linux executable.
This reproduced an additional launch race: an early Down reached Update at
7.7ms, before the asynchronous `trustPromptMsg` at 8.4ms. The key went to the
composer and the dialog then opened on its first row, appearing to ignore it.
The dialog is now armed synchronously in `withSession`, with no deferred Init
message to reopen/reset it. A deterministic test delivers keys before executing
any Init commands. The same visible test then selected session trust correctly.

A real OpenSSH loopback server was also started **inside** the Linux test
container, bound only to `127.0.0.1:22222`, using isolated test keys. The visible
path became tuios → container PTY → OpenSSH client/server → Linux executable.
The trace confirmed `SSH_TTY_set=true` and `SSH_CONNECTION_set=true`.

- With `TERM=xterm`, SSH kept the ANSI profile and colored-text status bar.
- Bubble Tea skipped its modes 2026/2027 queries on this SSH path, unlike the
  direct container PTY. Background-color and Kitty-keyboard queries remained.
- Early input during the logo reached the model at about 7.5ms after the timing
  trace started; it did not wait for the approximately 2.9-second animation.
- The fixed trust dialog accepted an early Down at about 6.6ms, before its first
  terminal frame was written at about 22.7ms. `/status` also worked through SSH.

These are process-relative timings, **not network-latency measurements**. The
SSH handshake is before `tui.Run`. No multi-second freeze was reproduced over
this local SSH connection. Different remote CPU load, shell/hooks, tmux, actual
terminal replies and SSH client settings remain untested. Codex working on the
same connection does not rule out terminal-protocol or event-queue differences.

The affected environment still needs a timing capture. No speculative logo,
color-profile or terminal-protocol change has been made as a claimed fix.
