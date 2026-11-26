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
   `cli_raw` for raw resource API commands when no shortcut exists. `cli_raw`
   also accepts `api <METHOD> <PATH>` for an endpoint with no typed command:
   GET/HEAD are reads, POST/PUT/PATCH need `high_risk=true`, and DELETE plus
   auth/token endpoints are blocked. `--data`/`--params` are inline JSON only;
   `--output` is workspace-relative, and `path` with `{file}` supplies a local
   target; without `path`, `{file}` output goes to `.golder/artifacts`. Pass the
   exact argv array and confirm=true. The host separately
   authorizes that call: the reviewer decides in `auto`, while `ask` requests a
   person's confirmation. Never bypass a denial.
4. Use `file_transfer` for uploads/downloads. Only local-path flags such as
   `--file`, `--output`, `--output-dir`, `--path` and `--dir` take exactly one
   `{file}` placeholder; `path` is workspace-relative and the adapter validates
   it. Format/name flags such as `--file-extension` and `--file-name` pass
   through. Do not put `@file`, stdin, or absolute output paths in
   `cli_call`/`cli_raw`.
5. Evaluate the result and perform a focused readback as needed. No write retries.

The executable is fixed. No shell strings, pipes, executable paths, config/auth,
installation, credentials, outputs outside the workspace or identity changes. `--as` and
`--format` are adapter-controlled. Tool filtering may disable an aggregate entry
entirely; use the named allowed tools instead. Do not search for an equivalent
command to bypass a denial.

Installed versions differ. Unknown flags should prompt another focused help
lookup, not speculative retries or an automatic upgrade/download.
