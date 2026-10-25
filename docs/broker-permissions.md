# Terminal and browser broker permissions

`tuios` and `browser` are agent tools, not privileged exemptions. `workspace`
is their composite launch path and follows the same policy. The human-facing
`golder browse` entry point remains available; agent-created browsers go
through the browser tool and the controlled launch path.

## Approval modes

- **auto:** the reviewer settles the entire request, including outside paths,
  network and an explicit external-host-control request. No human approval
  dialog is added. Denials and unavailable/unsatisfiable reviews fail closed.
- **ask:** safe reads remain cheap. Additional capabilities and non-routine
  effects are shown together in the existing consent dialog.
- **read-only:** terminal control and browser mutations remain forbidden.
- **full-access:** the explicit unrestricted mode still skips ordinary review;
  the static floor and prohibition on typing into agent approval UIs remain.

Initial project trust is separate from per-call permission review, as it is for
ordinary shell tools.

## Shared protections

The static floor reads the *effects* of a facade: `run.command`, `new.argv`,
potential shell input in `send.text`, and both `backend` and `frontend` commands.
Argv is shell-quoted rather than joined with spaces. The original tool, target,
cwd and capability fields remain visible to the reviewer.

Path extraction includes facade commands and a requested launch directory.
Browser input/navigation and browser-containing workspace presets request
network automatically. A screenshot is a file write and is reviewed as such.
All URL-bearing browser entry points reject local-file, script, browser-internal
and opaque schemes; ordinary absolute HTTP(S) and `about:blank` are supported.

## Controlled resources

One agent registry shares an in-memory ownership table across the three tools.
A pane title, filesystem metadata record, attachment, or matching process name
does not establish ownership.

New helpers launch with the actual platform sandbox argv, a scrubbed environment
and a private temporary home. Missing/failed sandbox creation is not retried bare.
Network is off unless granted. Helpers also receive `grants: [none]` from the
multiplexer: a helper must not use raw daemon calls to acquire pane-control
authority. An old daemon that cannot enforce that launch parameter fails closed.

A terminal pane keeps its launch-time capability ceiling. Input to a
network-enabled pane needs a network grant on that call. One-call outside-path
grants do not make a shell reusable, and changing standing path grants invalidates
an existing sandbox for further input. Recreate the controlled pane when its
ceiling is insufficient. Exited helpers are reaped before further launches.

Browsers get a fresh profile, not a discovered personal/shared login profile.
`open` reuses an owned instance or creates a new controlled instance;
`new_instance:true` explicitly requests a fresh one. Browser/tab identities and
page URLs are pinned before review and checked before an action; a changed
identity/page is refused rather than silently retargeted.

## Explicit external control

`host_control:true` requests one-call authority over an existing, non-owned
terminal or browser. The argument alone grants nothing. Auto still uses the
reviewer, never a human dialog; ask uses the consent dialog. Attaching to a
browser does not grant future control. An external process cannot be retrofitted
into an OS sandbox, so containment/enforce cannot authorize that external path.

Terminal input into this agent's own pane, another golder agent, or a pane known
to be waiting for approval is refused, even with a host-control grant. Browser
panes must be driven through browser actions, not the terminal-input facade.

This protects the agent-tool broker paths; it is not a security boundary against
arbitrary same-user host processes or a per-origin network firewall. Explicit
host control is intentionally powerful and must be reviewed as host authority,
not as a routine sandboxed command.
