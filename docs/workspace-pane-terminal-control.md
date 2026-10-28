# Controlled pane terminal control

## Failure inventory (before implementation)

- Agent-created tuios/workspace panes launch through ControlGuard's real OS
  sandbox; manually opened shells and execsess bash sessions are distinct paths.
- On macOS the generated deny-default Seatbelt profile has no file-ioctl
  allowance. Inherited PTY stdio does not authorize termios operations.
- The daemon allocates the pane PTY after ControlGuard generates its command;
  authorizing the launching agent's terminal would name the wrong device.
- Granting all /dev/ttys* ioctls would expose other panes and host terminals.
- A terminal capability must name the allocated slave, allow only ordinary
  termios/size/job-control requests, and leave input injection, console
  redirection and line-discipline changes denied.
- A non-TTY command must receive no terminal control capability. Failure to
  discover or bind a requested terminal must abort before running child code.
- No echo lease RPC or agent-facing secret transport is required: existing
  programs must control their own terminal through their normal interfaces.
- Password prompts must not be driven with real secrets in tests. Use only
  synthetic input, verify ECHO is clear before reading, restore the termios
  state, and prove another disposable PTY remains unchanged.

The first regression executes the exact sandboxed argv ControlGuard would
give the daemon, on a disposable controlling PTY. Before the fix it failed
with `stty: TIOCGETD: Operation not permitted`.

## Implementation

ControlGuard requests `OwnTerminal` only for sandboxed terminal helpers, not
browser exceptions or native file workers. A trusted internal CLI entry point
runs after tuios has allocated the child PTY and set its session/controlling
terminal, but before any user program executes. It does not load configuration,
credentials, grant tables, or trace destinations.

On macOS the binder resolves stdin's real slave with F_GETPATH and validates
its descriptor identity against the named character device. It then checks
that it is a fresh session/process-group leader and that both stdin and the
controlling terminal name that foreground process group. Only then can it
authorize the distinct `/dev/tty` alias used by Python getpass. Device names
come from descriptors, never tool arguments, environment variables or pane
metadata. Non-TTY launches bind neither device; invalid bindings abort before
the program runs. SDK hosts must explicitly register a bootstrap entry point
rather than accidentally re-executing an arbitrary host binary.

The generated Seatbelt rule combines exact device matches with ten numeric
ioctl requests: termios get/set (immediate/drain/flush), line-discipline query,
window-size get/set, foreground process-group get/set, and terminal flush.
Input injection, line-discipline mutation and console redirection remain
outside the deny-default whitelist. No blanket file-ioctl or writable /dev
grant is added, and file/network/daemon authority remains unchanged.

Linux retains its existing bubblewrap private /dev/devpts namespace; this
change does not add a Linux seccomp policy or assert new ioctl guarantees.
Linux runtime/container preflight remains suspended; cross-compilation alone
does not establish Linux runtime behaviour.

## Validation and credential handling

Real macOS sandbox tests cover stty, raw/canonical switching, size queries,
verified ECHO disable before synthetic input, restoration, and a second PTY
that must remain unchanged even when opened read-only. TIOCSTI and TIOCSETD
must return EPERM. A Python getpass probe treats GetPassWarning as an error,
so a plaintext fallback cannot count as success; it also verifies restoration.
No real credentials are supplied by a tool or captured during these tests.

This is terminal compatibility, not a new secret transport or echo-lease RPC.
Applications handling credentials must still verify no-echo and refuse
plaintext fallback. People enter real secrets directly in the pane, not via
agent send/press calls, screenshots or capture tools. Existing third-party
fallback behaviours are not rewritten by this change.

Rebuild/install golder and restart the agent that creates the helper pane.
Create a new automatic pane: an already-running pane's Seatbelt profile
cannot be expanded in place. Existing unrelated panes/session state need not
be deleted. No tuios-mini dependency release or pin change is required.
