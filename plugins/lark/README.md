# Golder Lark MCP plugin

`golder-lark` is a separate MCP stdio executable. It wraps **two official native
CLIs**, not a new Golder runtime:

- `lark-cli`: Feishu documents/Wiki, chats/messages, Sheets and Base.
- `meegle`: Feishu Project requirements, bugs, tasks and workflow transitions.

An existing Feishu application is supported. **Application configuration and
personal OAuth are separate layers**; all business operations use the user, never
an automatic bot fallback. Project needs its own user authorization.

## Layout

```text
plugins/lark/
  cmd/golder-lark/           executable and local operator commands
  internal/larkmcp/          catalog, runners, OAuth, Project and collaboration adapters
  skills/lark/              optional progressive-disclosure workflow and references
  assets.go                 embedded static Skills; never auto-installed by MCP
```

The generic state/setup, sandbox and consent code lives in the host's existing
packages. The plugin does not import the host UI/agent/permission internals.

## Install and register

Build from this repository (or unpack the matching `golder-lark` release):

```sh
GOWORK=off go build -o .build/golder-lark ./plugins/lark/cmd/golder-lark
```

Install the official CLIs **yourself**, only for the services you need:

```sh
npm install -g @larksuite/cli
npm install -g @lark-project/meegle
```

Configure the installed **native payload**, not the npm JS launcher. The adapter
rejects scripts/JS wrappers rather than risk an on-demand download. Meegle's npm
payload is next to `bin/meegle.js`, named `meegle-darwin-arm64`,
`meegle-linux-arm64`, etc. Consult the installed package for Lark's payload.
Use absolute paths, keep them outside `$GOLDER_HOME`, and pin versions you have
validated. Missing/replaced dependencies require source review after installation.

Generate a disabled source table without executing any service command:

```sh
.build/golder-lark print-config \
  --lark-cli /absolute/path/to/native/lark-cli \
  --project-cli /absolute/path/to/native/meegle \
  --skills-dir /absolute/path/to/golder/plugins/lark/skills
```

Merge that table into your Golder config (normally `$GOLDER_HOME/config.toml`).
Omit a CLI flag for a service you do not use. The optional `skills` declaration
is data-only, source-owned, fingerprinted and advertised only after approval.
For a binary-only distribution, explicitly unpack the embedded Skills first:

```sh
golder-lark install-assets --dir /absolute/path/to/lark-package
```

Then use `/absolute/path/to/lark-package/skills` as `--skills-dir`. Existing files
are never overwritten. Registration, tool listing and MCP initialization do not
install anything or trigger authorization.

## Your existing application + personal identity

Add the existing App ID and an App Secret **reference**, not the secret itself,
to the generated source's env table:

```toml
[mcp.servers.lark.env]
LARK_MCP_CLI = "/absolute/native/lark-cli"
LARK_MCP_PROJECT_CLI = "/absolute/native/meegle"
LARK_MCP_IDENTITY = "user"
LARK_MCP_APP_ID = "cli_your_existing_app_id"
LARK_MCP_APP_SECRET = "${credential:lark_app_secret}"
LARK_MCP_DOMAINS = "docs,drive,im,sheets,base"
MEEGLE_HOST = "project.feishu.cn"
```

Use the host's private credential store/reference mechanism or an explicit
operator environment reference. Do not paste App Secret/access/refresh tokens
into a conversation or CLI arguments. The wrapper feeds the secret to official
`config init --app-secret-stdin`; **it never uses `--new`**. An already configured
private application can omit both app settings. It does not import your global
terminal login or read the system keychain master key.

Review and enable, then authorize the corresponding service:

```sh
golder mcp inspect lark
golder mcp enable lark --yes
golder mcp setup lark             # existing application, then personal OAuth
golder mcp setup lark project     # separate Project user OAuth
golder mcp test lark              # obtain the standard MCP tool catalog
```

TUI equivalents: `/mcp setup lark`, `/mcp setup lark project`, `/mcp connect lark`;
the Extensions details menu also offers the configured authorization actions.

The built-in browser opens a **private offline QR page with no control endpoint**.
Scan with Feishu and approve on your phone. The CLI polls and verifies identity;
the host then closes its own QR pane. This intentionally replaces a general
desktop login tab: there are no desktop OAuth redirects/cookies to expose to the
model. Mac uses the existing browser-only runtime exception, not a plugin sandbox
escape. Linux keeps the offline viewer in its OS sandbox. Enforce mode can refuse
the Mac viewer; SSH/missing browser/unsupported launch falls back to the **same
official URL and terminal QR**. No Chrome installation or system browser opener.

Only `setup_origins` reviewed in the source may supply authorization URLs. If
your provider/version uses another exact HTTPS origin, inspect it and update the
source before reapproving. A private Project tenant also needs its explicit host
and reviewed authorization origins; the resource-URL helper supports public
Feishu/Lark/Meegle domains, so private tenant operations should use resolved IDs.

Effective access is constrained by application scopes/tenant approval, personal
OAuth scopes, and your actual document/chat/project/field role. A successful scan
does not grant permissions your tenant or resource owner has withheld.

## Tools and context

There are 20 high-frequency tools and `cli_help`/`cli_call`. Common tools accept
`{operation, options}`; option names use underscores. Examples:

```json
{"options":{"doc":"https://tenant.feishu.cn/docx/actual-token","scope":"outline"}}
```

```json
{"operation":"search","options":{"project_key":"actual-key","work_item_type":"actual-type","query":"登录失败","limit":20}}
```

```json
{"operation":"cells","options":{"spreadsheet_token":"actual-token","sheet_id":"actual-sheet","range":"A1","cells":[[{"value":"001"}]]},"confirm":true}
```

The optional Lark Skill contributes a short index; its body/references and
command-specific CLI help are read on demand. Current Golder still declares all
available tool schemas to the model; this is not intent-based schema unloading.
The model chooses tools semantically, not through a background keyword watcher.

`status` diagnoses both services independently. `cli_help` discovers a focused
business command; `cli_call` invokes the fixed native binary with literal argv,
never a shell. Auth/config/install, raw API, local-file/output paths, destructive
bulk actions and permission/automation bypasses are blocked. A narrower tool deny
disables the aggregate entry rather than leaving a way around that deny.

Writes and long-tail execution require **ask mode and a bound human confirmation**.
`confirm=true` expresses intent only. The Golder host attaches a namespaced MCP
metadata marker after its exact-call approval. Other MCP hosts must implement an
equivalent human gate and attach `io.golder/operator-confirmed`; arguments cannot
set it. A host owns that assertion; this is not a signed cross-host credential.
No automatic `--yes`, bot fallback or write replay. Readback/receipts are explicitly
reported as such, not falsely labeled complete equivalence verification.

## State and supported validation

- Each source has a 0700 private HOME under
  `$GOLDER_HOME/extensions/mcp-lark/home`, shared by setup/business calls.
- Mac Lark file-store adaptation creates a new 0600 random file key only in an
  empty private store; it does not run global keychain downgrade. Project uses
  the official encrypted-file fallback with OS helper lookup disabled. Both are
  version-coupled adapters, not hardware-keystore confidentiality promises.
- CLI refreshes can write back. A cross-process lock prevents simultaneous use
  of the same source; disconnect another session before setup/reset.
- `golder mcp reset lark --yes` clears only this source's local state, not the
  provider's server-side consent. Disable/remove do not erase it implicitly.
- Do not copy state between machines: Project encryption can depend on USER and
  hostname. Reauthorize instead. DPoP errors fail closed without downgrading policy.
- Local fixture tests cover MCP discovery/calls, two-layer app/user auth, Project
  polling, privacy, state isolation and policy gates. **A real account scan and
  remote business writes are operator acceptance checks, not claimed by fixtures.**
- Mac/Linux are the release targets. Linux container preflight remains suspended;
  cross-compilation is not Linux runtime verification.
