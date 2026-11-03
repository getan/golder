# GUI Extension management

The desktop Settings → Extensions page is the operator surface for installing,
reviewing, authorizing, testing, enabling and disabling extension processes. It
does not turn extensions into in-process Go plugins: MCP/CLI/RPC sources retain
their existing process, sandbox and fingerprint boundaries.

## Product boundary

```text
Golder GUI
  └── Extension Manager UI
        ├── managed first-party package install
        ├── source configuration
        ├── explicit fingerprint approval
        ├── operator-only OAuth/setup events
        └── enable / disable / test / reset / uninstall

Golder host
  └── internal/extensions.Manager
        ├── config.toml source declaration
        ├── extensions-state.json approval fingerprint
        ├── private source HOME
        ├── OS sandbox + network ceiling
        └── MCP stdio child process
```

The GUI never loads extension code into its own process and never treats package
visibility as installation, approval or authority.

## Lark + Project flow

The packaged GUI carries a small, matching `golder-lark` helper under:

```text
Resources/extensions/lark/golder-lark[.exe]
Resources/extensions/lark/golder-lark[.exe].sha256
```

This is not a second full `golder` binary. It is the Lark MCP stdio adapter. The
release workflow builds it for the package architecture, strips it, records its
SHA-256 and lets MyGo include/sign it with the app.

Nothing happens during discovery. The person explicitly performs these steps:

1. **Install package**
   - only the registered first-party package ID `lark` is accepted;
   - the bundled source must be an absolute regular executable, not a symlink;
   - SHA-256 must match the signed bundle's checksum file;
   - the helper is copied atomically to
     `$GOLDER_HOME/extensions/packages/lark/<version>/bin/`;
   - every state component is a real private directory (`0700`), not a symlink;
   - install does not edit config, approve a source, start code or begin OAuth.
2. **Configure application and official CLIs**
   - choose absolute native `lark-cli` and optional `meegle` executables with a
     native file picker;
   - enter the existing application ID;
   - enter a new App Secret into a password field or choose a named credential;
   - the literal secret is written only to `.credentials.yaml` (`0600`);
   - `config.toml` receives `${credential:lark-app-secret}`, never the value;
   - the source is written disabled and added to the live Manager unapproved.
3. **Review and enable**
   - the page states the fixed network ceiling, private HOME and exact executable
     dependencies;
   - **Approve & enable** fingerprints helper bytes, official CLI bytes and the
     complete source declaration;
   - the remembered approval and `enabled=true` are separate persisted facts;
   - changing/replacing any executable invalidates approval.
4. **Authorize**
   - Lark and Project remain separate OAuth grants;
   - setup runs only after approval and enablement;
   - the child emits bounded, versioned setup events;
   - authorization URLs are checked against reviewed exact HTTPS origins;
   - the GUI opens the system browser and retains the URL only in transient view
     state, never in Agent history, logs or tool results;
   - closing Settings cancels an in-flight authorization and clears secret/URL
     view state.
5. **Test**
   - Test connection performs MCP initialization/catalog discovery only;
   - it invokes no Lark business tool.

## Other configured sources

The page also lists every configured MCP, CLI and legacy RPC source with:

- exact source identity (`mcp:<id>` / `plugin:<id>`);
- endpoint/executable;
- state and authentication status;
- trust status;
- network ceiling, private-state flag and executable count;
- available tool count.

Disabled sources need a two-step **Enable → Confirm enable** interaction. The
second click fingerprints and approves the exact source. Disabling is immediate
and fails closed; testing is available only to enabled sources.

## Reset and uninstall are different

- **Reset local login** deletes only the source-scoped private HOME. It does not
  revoke server-side consent, remove the helper or delete the named App Secret.
- **Uninstall** disables/disconnects the source, removes its config and approval
  row, then removes every valid managed package version. Login state and named
  credentials remain until their own explicit destructive actions.
- Both operations require a second confirmation click.

## Security invariants

- Discovery is data-only.
- Package IDs come from a closed first-party registry.
- There is no model tool for package install/config/setup.
- Bundle source, managed package root and executable path are exact and
  checksum-bound.
- Managed package lookup itself creates no files.
- Package manifests cannot redirect execution outside their exact version root.
- Official CLI paths resolve symlinks once and the canonical binaries enter the
  source fingerprint and sandbox executable declaration.
- Secrets never enter argv, TOML, events, snapshots or logs.
- Extension children receive a scrubbed environment, private HOME and immutable
  launch-time network ceiling.
- Business writes still pass ordinary tool review after an extension is enabled.

## Pane Agent control is separate

Extension enablement grants no terminal pane authority. Multi-Agent pane control
is already implemented by the pane-control registry and tuios menu:

- candidates are online top-level Agents in the same tuios session/workspace;
- temporary task sub-agents are read-only and not candidates;
- leases bind session ID + full window ID + Agent ID + conversation + scope;
- one pane has one exclusive writer; transfer is explicit;
- all Agents may continue read-only capture/wait;
- one-call leases are consumed atomically;
- revoke/transfer wins revalidation before queued input is sent;
- pane close, workspace move, Agent/conversation exit and daemon replacement
  invalidate the lease;
- every concrete command still passes ordinary review.

See [`broker-permissions.md`](broker-permissions.md#operator-assigned-pane-leases)
and the v1.5.44/v1.5.45 release notes for that protocol.
