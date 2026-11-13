# Long-tail CLI operations

Prefer named tools for supported common work. Otherwise:

1. `cli_help` with service=lark/project and a command path, e.g.
   `{"service":"lark","command":["sheets","+styles-put"]}`.
   For Project MQL search help use
   `{"service":"project","command":["workitem","query"]}`; `ticket_search`
   is the MCP tool name, not a Project CLI command family. The installed CLI may
   require Project login and server-side command discovery before help works.
   `cli_help` only covers exposed business commands; it cannot query
   auth/config/install. Skill CLI examples are not MCP capabilities. Use
   Golder's authorization/settings entry for login or incremental scopes.
2. Read only that command's help; ensure targets, options and impact are clear.
3. Use `cli_call` with the exact argv array and confirm=true. The host separately
   authorizes that call: the reviewer decides in `auto`, while `ask` requests a
   person's confirmation. Never bypass a denial.
4. Evaluate the result and perform a focused readback as needed. No write retries.

The executable is fixed. No shell strings, pipes, executable paths, config/auth,
installation, raw API, credentials, @file/stdin, output-paths or identity changes.
File transfer, destructive/permission/automation operations are blocked unless a
dedicated reviewed capability supports them. Do not search for an equivalent
command to bypass a denial. Tool filtering may disable the aggregate cli_call
entry entirely; use the named allowed tools instead.

Installed versions differ. Unknown flags should prompt another focused help
lookup, not speculative retries or an automatic upgrade/download.
