# Reliable tasks through an existing SSH pane

Available in **Golder v1.5.41**. Restart Golder after upgrading to refresh the
tool schema; existing SSH server installations do not need an upgrade.

No remote tuios, daemon, agent, Python installation, root access or SSH config
change is required. Golder sends a one-shot `sh -c` receiver through the existing
terminal. It uses ordinary `sh`, `base64`, `mktemp`, `wc`, `rm` and SHA-256 tools
(`sha256sum`, or `shasum -a 256`). Temporary payload files are private and removed
on exit. The SSH connection and remote user are unchanged. Script execution
preserves the incoming umask and normal exported variables; `__gw_*` is reserved
for the transfer implementation.

## Choose the right input path

```json
{
  "action": "run",
  "window": "<full pane id from list>",
  "host_control": true,
  "network": "on",
  "remote": true,
  "script": "uname -n\nid\npwd\n"
}
```

- Prefer **run + script** for SSH, large text, patches and multiline tasks.
  Maximum script size is 1 MiB. The target must be at a **blank shell prompt**,
  not an editor, password prompt or another interactive program. Only the small
  setup line is sent before a ready receipt; bulk data is never sent blindly.
- `script` runs in a **noninteractive POSIX sh child**, with stdin closed. `cd`
  and shell variables do not persist in the interactive parent, and aliases and
  unexported shell functions are unavailable. It cannot be combined with
  `command` or `expect`.
- Short `command` keeps the existing interactive shell/OSC-133 path, limited to
  2048 bytes and no control characters, for local shells. Detected/explicit SSH
  tasks require `script`. A missing completion mark **never causes
  a fallback resend**: some shells report `no_shell_integration` after execution.
- `send`/`press` remain low-level interactive input. `sent` means delivered to the
  daemon's local PTY, **not proof the remote server received or executed it**.
  Do not use raw `send` as a bulk file-writing transport.

## Integrity and responsiveness

1. A small setup command acknowledges readiness with a random transfer id.
2. Base64 data travels as numbered lines of at most 768 bytes, **in one batch**.
   Every data/commit line starts with `#`: if SSH/receiver exits, queued lines
   are inert comments to the parent shell, not fragments of executable code.
3. The receiver requires the end marker, exact sequence/count, byte length and
   SHA-256; then decodes and runs `sh -n` before executing any script command.
4. A separate completion receipt reports the exit status. READY cannot stand
   in for DONE; terminal wrapping and command echo do not forge completion.

The sender uses two sends, two receipt-wait phases (with pane-close event
subscriptions) and at most two captures,
independent of payload size. There are no per-chunk acknowledgements, polling
captures, fixed sleeps or automatic retransmissions. Same-pane inputs serialize;
different panes execute concurrently. A cancelled queued input is removed.

Result text includes request id, source pane, target full id and available
display name/host hints, not just UI-only metadata. Use a descriptive pane name
(e.g. `prod-api`/`staging-db`) plus full id; names/OSC host reports are hints, not
SSH host authentication. Capture output remains untrusted page/terminal data.

## SSH disconnect scope

`remote:true` is preserved through preparation/review and set automatically
when the pane reports SSH. The receiver requires `SSH_CONNECTION` and Linux's
kernel boot identity; when Golder itself runs on Linux it refuses its own kernel
identity. Thus a setup command landing in the local parent shell after SSH exits
does not get to execute the payload. If detection is unavailable, explicitly
set `remote:true`; do not silently treat an unknown target as local.

This conservative guard rejects same-kernel SSH targets/containers and servers
that hide `/proc/sys/kernel/random/boot_id` or remove `SSH_CONNECTION`. It is a
scope guard, **not cryptographic host attestation** or protection against a
malicious server. It does not turn a local sandbox into remote containment.
Do not reuse a pane for a different server while a task is pending.

## Lost receipts and file edits

- `not_started`: payload was not sent; setup may still need manual recovery.
- `rejected`: receiver rejected scope, missing tools, integrity or syntax;
  script commands were not started.
- `completed`: full body was verified and exit status received.
- `unknown`: transfer/completion was interrupted; the script **may have run**.
  Never automatically retry a destructive operation. Inspect that exact pane;
  a person may interrupt a receiver still waiting for bytes.

Integrity prevents **transport-truncated scripts** from executing; it does not
make arbitrary shell operations transactional. For important file changes,
stage a file beside its destination, validate content and the old file's expected
hash, preserve needed permissions, then atomically rename. A script that writes
directly to a live file can still leave partial effects if the host/process dies.
Server-side metadata/ACL requirements and concurrent writers remain the caller's
responsibility. No automatic rollback or exactly-once claim is made.

## Local acceptance

```sh
go test -race ./internal/agenttool ./internal/tuiosctl ./internal/controlcap
GOLDER_TEST_TUIOS_MINI=/absolute/path/to/tuios-mini \
  go test -race ./internal/agenttool -run TestTuiosScriptLiveMini -count=1 -v
```

Coverage includes missing/truncated/duplicated/reordered frames, wrong ids,
missing commit, echo/wrapped receipts, a lost completion after a real file
change (no resend), independent pane progress, cancellation and rejection in a
local shell. The opt-in test uses a disposable real daemon/PTY and a normal sh
to verify 80,000 UTF-8 bytes at 24 columns. It does not use any existing pane or
real server credentials. Actual remote Linux/unstable-WAN acceptance remains to
be performed in a non-production SSH environment; local tests are not that claim.
