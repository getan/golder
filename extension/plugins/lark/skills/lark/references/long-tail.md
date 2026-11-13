# Long-tail CLI operations

Prefer named tools for supported common work. Otherwise:

1. `cli_help` with service=lark/project and any command path, for example
   `{"service":"lark","command":["sheets","+styles-put"]}` or
   `{"service":"lark","command":["calendar","events","search_event"]}`.
   There is no service or command allowlist. `auth`, `config`, and `install`
   remain unavailable. The installed CLI may require login and server-side
   command discovery before help works.
2. Read only that command's help; ensure targets, options and impact are clear.
3. Use `cli_call` for Lark `+shortcuts` and Project business commands, or
   `cli_raw` for raw resource API commands when no shortcut exists. Pass the
   exact argv array and confirm=true. The host separately authorizes that call:
   the reviewer decides in `auto`, while `ask` requests a person's confirmation.
   Set `high_risk=true` only when help marks the command high-risk; the adapter
   adds `--yes` after host authorization. Never bypass a denial.
4. Use `file_transfer` for uploads/downloads. Its argv contains exactly one
   `{file}` placeholder; `path` is workspace-relative and the adapter validates
   it. Do not put `@file`, stdin, or local paths in `cli_call`/`cli_raw`.
5. Evaluate the result and perform a focused readback as needed. No write retries.

The executable is fixed. No shell strings, pipes, executable paths, config/auth,
installation, credentials, output-paths or identity changes. `--as` and
`--format` are adapter-controlled. Tool filtering may disable an aggregate entry
entirely; use the named allowed tools instead. Do not search for an equivalent
command to bypass a denial.

Installed versions differ. Unknown flags should prompt another focused help
lookup, not speculative retries or an automatic upgrade/download.
