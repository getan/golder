# SSH workspace startup/input delay investigation

Status: one outer-welcome UI blocking defect has been reproduced and fixed in
released tuios-mini `v0.10.16`, now pinned by golder. Its correspondence to the
reported Linux server's minute-long wait is **not yet established**; the
updated dependency enables testing that correspondence on the server.

## Reported shape

- Linux server reached through SSH.
- Direct `golder` works normally.
- `golder workspace` cannot accept input for roughly a minute after opening;
  input becomes normal afterwards.
- Both `default` and other named sessions are affected. Deleting/recreating
  the default session is therefore not a justified remedy.
- The outer workspace welcome screen also stalls without an inner golder
  interaction. Investigation must not assume the agent is responsible.

## What the code and local experiments establish

The visible client is separate from the persistent daemon and the agent pane:

```text
workspace command -> owner probe/start -> workspace client
  -> terminal capabilities -> daemon handshake/attach
  -> restore terminal snapshots -> start Bubble Tea input/event loop
  -> encode key -> local daemon -> pane PTY -> agent
```

The embedded client currently restores terminal snapshots synchronously before
starting its input reader. The dependency's daemon round-trip budget is 30s.
Multiple waits can therefore block startup for a long time. This is a candidate
blocking path, **not evidence that the server actually timed out there**. In
particular, it cannot explain an already displayed empty welcome screen by
itself; that case also requires inspecting View/Update and input handoff.

The terminal capability probe normally has a 300ms budget. Missing terminal
replies alone did not reproduce the minute-long stall in a local PTY with SSH
environment variables set. Real SSH transport, Linux terminal handling and the
server's existing configuration have not been reproduced by that experiment.

`TestE2ESSHWorkspaceFirstInputDoesNotWaitForTerminalReplies` covers default and
another named session, shell and real golder agent panes, and fresh/reattached
clients. It leaves appearance at the defaults and supplies no terminal replies.
It checks both a first-frame/prompt budget and a first-input budget, using a
different agent draft marker after reattach so stale screen contents cannot
count as a successful key. Local observations were roughly 0.35–1.2s to a
frame/prompt and 20–45ms for the first input; these are not Linux/SSH latency
measurements.

`TestE2ESSHWorkspaceWelcomeAcceptsOuterUIKeys` starts no golder agent, switches
to the empty GOLDER/AGENT WORKSPACE welcome and opens the outer keybindings
overlay. Both simulated 16-color and 256-color SSH terminals accepted that
outer key locally in roughly 20–23ms.

## Confirmed welcome-render blocking defect

The welcome View calls `welcomePresetChip -> presetSuggestion`, which used to
stat home-directory configs and read the tmux leader on the UI goroutine.
`tmuxPrefix` opened the file *before* checking the descriptor was regular. A
FIFO without a writer therefore blocked the welcome and its input forever;
a slow home/config mount could hold that same UI path while I/O was pending.
Once the hint was cached, subsequent frames did no such probe.

`TestE2ESSHWorkspaceWelcomeDoesNotOpenPipeConfig` reproduced this using only a
private HOME and a FIFO `.tmux.conf`, with no golder agent. The released
v0.10.15 dependency failed its welcome deadline; the local patched dependency
passes. The fix schedules the optional home probe off the UI goroutine,
delivers only a suggestion message, and accepts only ordinary config files.
Unix opens are nonblocking to reject a FIFO even if a file-type check races a
replacement. Symlinked regular dotfiles and the last configured tmux leader
are preserved, and learning a hint never applies a preset.

The dependency fix is commit `771f2b9b2262ea5b731dd21a31b3caf6ddedd0a3`,
published as tuios-mini `v0.10.16`. Golder's go.mod/go.sum now pin that release;
`GOWORK=off` selects its downloaded module, not the local working copy.
The FIFO regression no longer has an extra build tag and runs by default:

```sh
GOWORK=off go test -race ./internal/workspace \
  -run '^TestE2ESSHWorkspaceWelcomeDoesNotOpenPipeConfig$' -count=1
```

Rebuild/install golder from this pin update and start a new visible workspace
client to exercise the fixed welcome code. A pre-existing client does not
hot-reload its binary. The background daemon is not where the welcome UI runs.

## Capturing the server-side wait

Run the same workspace invocation that stalls, with the opt-in trace enabled:

```sh
golder --version
trace=$(mktemp /tmp/golder-workspace-startup.XXXXXX)
GOLDER_TUI_TRACE="$trace" golder workspace
# Wait until it becomes responsive, then detach with Ctrl+B followed by d.
printf 'trace: %s\n' "$trace"
```

Do not delete session state, remove sockets, kill the daemon or send a real
prompt just to collect this trace. Detaching the visible client preserves its
panes. The log appends with 0600 permissions; processes share the destination
but each line has a PID.

The new workspace instrumentation records owner probe/start and client-run
phases, plus input-reader initialization, polling, decode and event handoff
metadata. It records no key text, paste contents or session/path arguments and
does not add a second stdin reader or change input timeouts. The existing
agent startup/TUI trace remains useful for the inner process too. Older
installed builds will not have the new `workspace`/`workspace.input` phases.

When tracing is enabled, a one-shot sample after five seconds also records
function-symbol stacks for workspace/input/UI goroutines. It omits function
arguments, source paths and input contents; an idle stack sample alone is not
proof of a stall. The sample can locate the blocking call in a live minute-long
freeze without killing the client for a raw SIGQUIT dump.

The key distinction is where the long gap appears:

- Before `workspace client.run begin`: session/owner startup.
- Between that phase and `workspace.input reader.init.begin`: embedded client
  setup, attach or restoration, before keyboard reading begins.
- Reader setup/first poll/decoding: terminal/input path.
- Long event handoff: the consumer/event loop has not accepted the event.
- Input reaches the agent promptly but no visible response: inner startup,
  model update/render or output delivery, not a dropped keyboard event.

Also note whether the workspace border and inner agent composer are already
fully visible during the stall. That observation distinguishes pre-render
startup from post-render input/event-loop blocking.

Linux runtime/container preflight remains suspended. A Linux cross-build is
only a compilation check, not confirmation of runtime behaviour on the server.
