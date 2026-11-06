# Lark MCP setup runbook

This is the operator runbook for enabling Golder's first-party Lark/Meegle MCP
package on a fresh machine. It is written for an agent to follow: every step has
a command, an expected result, and the failure it is meant to prevent.

It complements [`extension/plugins/lark/README.md`](../extension/plugins/lark/README.md),
which describes the adapter itself.

## Desktop GUI path

Packaged Golder GUI builds expose the recommended path under **Settings →
Extensions → Lark + Project**:

1. click **Install** to checksum-verify and copy the matching `golder-lark`
   helper into the private managed package directory;
2. choose the absolute native `lark-cli` and optional `meegle` executables;
3. enter the existing App ID and a write-only App Secret (stored as a named
   credential, never literal TOML);
4. save the disabled configuration, review and explicitly enable it;
5. click **Authorize Lark** and, when configured, **Authorize Project**;
6. click **Test connection**.

The remainder of this runbook documents the equivalent manual/CLI path and the
official CLI installation details still relevant to the file picker. The GUI
does not download or silently choose those third-party native CLIs.

## What the pieces are

| Piece | Where it comes from | Why it exists |
|---|---|---|
| `golder-lark` | this repository, `extension/plugins/lark/cmd/golder-lark` | the MCP stdio server; wraps the two official CLIs |
| `lark-cli` | npm `@larksuite/cli` | official Lark CLI: docs, drive, IM, sheets, base, calendar, mail, tasks |
| `meegle` | npm `@lark-project/meegle` | official Feishu Project CLI: work items, schedules, workflow transitions |
| skills | this repository, `extension/plugins/lark/skills` | progressive-disclosure instructions advertised after approval |

They are separate on purpose: Lark and Feishu Project are different products
with different hosts and different OAuth grants. Configure only the one you use.

`golder` never links any of them. It starts `golder-lark` as a child process and
talks MCP over stdio.

## Background every agent must know

1. **The npm packages are launchers, not the executables Golder runs.**
   `~/Library/pnpm/bin/lark-cli` and `.../meegle` are `#!/bin/sh` shims that run
   `node .../run.js`. The plugin rejects scripts and JS wrappers; it needs the
   native binary.
2. **`@larksuite/cli` produces its native binary in a `postinstall` script.**
   pnpm 10+ blocks dependency lifecycle scripts by default, so without
   `--allow-build=@larksuite/cli` the install "succeeds" but leaves only the JS
   launcher. `@lark-project/meegle` ships prebuilt binaries for every platform
   (`bin/meegle-darwin-arm64`, `-linux-x64`, `-win32-x64.exe`, and so on).
3. **CLI paths must be absolute and are never resolved from `PATH`.**
   `internal/larkmcp/config.go` rejects relative paths, resolves symlinks, and
   optionally checks `GOLDER_EXTENSION_EXECUTABLES`. This is deliberate: the
   plugin performs business writes with the user's own identity, so "which file
   was approved" must be a concrete path, not whatever `PATH` resolves to at
   call time.
4. **Only user identity is accepted.** `LARK_MCP_IDENTITY=user`; bot/auto
   fallback is rejected. App ID/Secret is a separate configuration layer, and
   the secret must be a credential reference, never a literal.
5. **`enabled = true` alone is not approval.** The host keeps an approval
   fingerprint per source; `golder mcp enable <id> --yes --save` records it.

## Install on a fresh machine

Run from the repository root. `$HOME/.local/bin` and `$HOME/.local/lib` are
examples; any absolute directory outside `$GOLDER_HOME` works.

### 1. Code

```sh
git clone https://github.com/getan/golder.git
cd golder
go version   # must satisfy go.mod (1.27.3 at the time of writing)
```

### 2. Official CLIs

pnpm is preferred; npm works the same way.

```sh
pnpm add -g \
  --allow-build=@larksuite/cli \
  --allow-build=@lark-project/meegle \
  @larksuite/cli @lark-project/meegle
```

Expected output includes a postinstall line:

```text
@larksuite/cli postinstall$ node scripts/install.js
@larksuite/cli postinstall: lark-cli vX.Y.Z installed successfully
```

If that line is absent, the build script was blocked and there is no native
binary. Re-run with the `--allow-build` flags, or approve builds interactively
with `pnpm approve-builds -g`.

### 3. The wrapper

```sh
mkdir -p "$HOME/.local/bin"
CGO_ENABLED=0 go build -o "$HOME/.local/bin/golder-lark" ./extension/plugins/lark/cmd/golder-lark
"$HOME/.local/bin/golder-lark" --version
```

### 4. Locate the native payloads

Do not guess. Resolve them:

```sh
root=$(pnpm root -g)
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in arm64) a=arm64 ;; x86_64) a=x64 ;; *) a=amd64 ;; esac

lark=$(find -L "$root" -path '*/@larksuite/cli/bin/lark-cli' | head -1)
meegle=$(find -L "$root" -path "*/@lark-project/meegle/bin/meegle-$os-$a" | head -1)

file "$lark" "$meegle"     # both must be native executables
"$lark" --version
"$meegle" --version
```

`-L` matters: pnpm links package directories into its content-addressed store,
and a plain `find` will not descend into them.

### 5. Pin the binaries (recommended)

pnpm store paths embed the package version and a content hash, so they change on
upgrade. Copy the two binaries somewhere stable and configure that path:

```sh
lib="$HOME/.local/lib/golder-lark"
mkdir -p "$lib"
cp -p "$lark" "$lib/lark-cli"
cp -p "$meegle" "$lib/meegle"
cp -R extension/plugins/lark/skills "$lib/skills"
```

Upgrading later means re-copying these two files, not editing the configuration.

### 6. Generate and merge the MCP table

```sh
"$HOME/.local/bin/golder-lark" print-config \
  --lark-cli "$HOME/.local/lib/golder-lark/lark-cli" \
  --project-cli "$HOME/.local/lib/golder-lark/meegle" \
  --skills-dir "$HOME/.local/lib/golder-lark/skills"
```

Append the output to `$GOLDER_HOME/config.toml` (`$HOME/.golder/config.toml` by
default). The generated table is `enabled = false` on purpose: it is a review
artifact, not consent.

Minimal shape (replace every path with an absolute one on this machine):

```toml
[mcp.servers.lark]
  command = "/ABS/PATH/TO/golder-lark"
  enabled = false
  transport = "stdio"
  state_dir = true
  network = true
  aggregate_tools = ["cli_call"]
  confirm_tools = ["doc_write", "message_send", "ticket_update", "ticket_transition",
                   "sheet_write", "sheet_structure", "base_record_write", "cli_call"]
  executables = ["/ABS/PATH/TO/lark-cli", "/ABS/PATH/TO/meegle"]
  skills = "/ABS/PATH/TO/skills"
  setup = ["/ABS/PATH/TO/golder-lark", "setup", "--events", "--service=lark"]
  setup_origins = ["https://accounts.feishu.cn", "https://open.feishu.cn", "https://project.feishu.cn"]
  call_timeout = "120s"
  [mcp.servers.lark.env]
    LARK_MCP_CLI = "/ABS/PATH/TO/lark-cli"
    LARK_MCP_PROJECT_CLI = "/ABS/PATH/TO/meegle"
    LARK_MCP_IDENTITY = "user"
    MEEGLE_HOST = "project.feishu.cn"
  [mcp.servers.lark.setup_variants]
    project = ["/ABS/PATH/TO/golder-lark", "setup", "--events", "--service=project"]
```

### 7. Approve, enable, authorize

```sh
golder mcp inspect lark          # read the launch ceiling before consenting
golder mcp enable lark --yes --save
golder mcp setup lark            # personal OAuth for Lark
golder mcp setup lark project    # only if Project is configured
```

`setup` must go through `golder mcp`, not by running `golder-lark setup`
directly: the host creates the private state home that `state_dir = true`
declares. A direct run fails with
`golder-lark requires a private HOME` unless `LARK_MCP_STATE_HOME` is set to a
dedicated absolute directory.

The setup stream exposes only a fixed phase/error-code vocabulary. Raw CLI
stderr, device codes, authorization URLs and tokens never become GUI or TUI
errors. If device-code polling exits non-zero after saving a token, setup checks
`auth status --json --verify` before declaring failure. Final verification uses
short bounded retries for command/network failures, but never retries or falls
back when the CLI explicitly reports a non-user or unverified identity.

## Verification

```sh
golder mcp list                       # lark should be enabled and approved
golder mcp tools lark                 # the tool catalog, no business call
golder mcp test lark                  # starts the child and handshakes
```

Then, in a session:

```text
/status            # extension inventory includes mcp:lark
```

No business command is executed by any of the checks above.

## After enabling: automatic, not command-driven

Once the source is enabled and approved, its tools are registered into the
model's tool list at session start. The user does not run a command per use:
the model chooses a Lark tool when the task needs one.

| Category | Behaviour |
|---|---|
| read tools | may run under the session's approval mode |
| `confirm_tools` (`doc_write`, `message_send`, `ticket_*`, `sheet_*`, `base_record_write`, `cli_call`) | go through the normal consent gate every time |
| OAuth | one-time `golder mcp setup lark`; not repeated per call |

## Security invariants

- Absolute native binaries only; relative paths and JS wrappers are refused.
- Symlinks are resolved before the path is accepted.
- When the host declares `GOLDER_EXTENSION_EXECUTABLES`, a configured path must
  appear in that list.
- `state_dir = true` gives the child a private HOME; it never sees the user's
  real home or the keychain master key.
- `network = true` is the launch ceiling, not a per-call grant.
- App Secret uses `\${credential:name}`; never paste a literal secret into
  `config.toml`, a prompt, or CLI arguments.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `CLI_UNAVAILABLE` | path points at the JS shim, or the postinstall never ran | reinstall with `--allow-build`; point at `bin/lark-cli` / `bin/meegle-<os>-<arch>` |
| `golder-lark requires a private HOME` | ran `golder-lark setup` directly | use `golder mcp setup lark` |
| `extension requires explicit trust approval` | enabled but not approved | `golder mcp enable lark --yes --save` |
| `PROJECT_CLI_UNAVAILABLE` | the configured native Project CLI could not be started | save the correct native executable path and reapprove the source if its dependencies changed |
| `PROJECT_AUTH_NETWORK_FAILED` | Project authorization initialization could not reach the configured host | check the saved Project host, network and proxy; no authorization page was opened |
| `PROJECT_AUTH_TLS_FAILED` | Project authorization initialization could not verify TLS | check the saved host and system certificate trust; do not disable TLS verification |
| `PROJECT_AUTH_RESPONSE_INVALID`, `PROJECT_AUTH_DEVICE_MISSING`, `PROJECT_AUTH_CLIENT_MISSING`, `PROJECT_AUTH_EXPIRY_INVALID`, `PROJECT_AUTH_URL_MISSING`, `PROJECT_AUTH_USER_CODE_INVALID`, `PROJECT_AUTH_INTERVAL_INVALID` | Project CLI initialization returned a response that cannot be used safely | report the code and CLI version; do not share the raw response, device code, user code or authorization URL |
| `AUTH_POLL_FAILED` | device authorization did not complete and status was not verified | start a new authorization flow; do not reuse the previous URL |
| `AUTH_VERIFY_NETWORK_FAILED` | token may be saved, but bounded server verification did not complete | check network/service health, then verify or retry setup; do not reset credentials solely for this error |
| `AUTH_VERIFY_IDENTITY_FAILED` | status explicitly reported a non-user or unverified identity | authorize the required user identity; bot fallback remains disabled |
| pnpm: `Cannot find module .../pnpm/bin/pnpm.mjs` | a stale `pnpm setup` shim in `$PNPM_HOME/bin` shadows the real CLI | move the shim aside and use the package-manager-installed `pnpm` |
| pnpm: `ENOENT ... mkdir '~/Library/pnpm/bin'` | `~/Library/pnpm` is a dangling symlink (store moved to an unmounted disk) | mount the disk, or replace the link with a real directory |
| `Permission denied` deleting an old store | the mount's parent directory is not writable by the user | `sudo rm -rf /Volumes/<DISK>/pnpm_store` |

## Upgrading

The GUI distinguishes the helper bundled with the App, the newest managed
installation, and the executable used by the active Source. When they differ,
authorization and connection testing are blocked and Settings shows **Update
helper**. One click verifies and installs the bundled helper, preserves App ID,
CLI paths, domains, Project host, named App Secret and the private login HOME,
then rebuilds the Source disabled and unapproved. Review it and click **Approve
& enable**; do not reset a valid login merely to update the executable.

The standalone official CLIs can be upgraded separately:

```sh
pnpm update -g @larksuite/cli @lark-project/meegle
root=$(pnpm root -g)
cp -p "$(find -L "$root" -path '*/@larksuite/cli/bin/lark-cli' | head -1)" "$HOME/.local/lib/golder-lark/lark-cli"
cp -p "$(find -L "$root" -path '*/@lark-project/meegle/bin/meegle-*' | head -1)" "$HOME/.local/lib/golder-lark/meegle"
```

The configuration does not change. Re-run `golder mcp test lark` afterwards.

## What must never be committed

- `$GOLDER_HOME/config.toml` and anything under `$HOME/.golder/`
- absolute paths to a person's home directory or external volumes in docs,
  examples, or test fixtures (use `/path/to/...`, `/srv/...`, `/Volumes/External`)
- App Secret, access/refresh tokens, or OAuth caches

The repository already ignores a local `.golder/` directory; keep it that way.
