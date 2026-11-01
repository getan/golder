# Long-tail CLI operations

Prefer named tools for supported common work. Otherwise:

1. `cli_help` with service=lark/project and a command path, e.g.
   `{"service":"lark","command":["sheets","+styles-put"]}`.
2. Read only that command's help; ensure targets, options and impact are clear.
3. Explain the exact operation to the operator. Use `cli_call` with an argv array
   and confirm=true. This still requires the host's exact human approval.
4. Evaluate the result and perform a focused readback as needed. No write retries.

The executable is fixed. No shell strings, pipes, executable paths, config/auth,
installation, raw API, credentials, @file/stdin, output-paths or identity changes.
File transfer, destructive/permission/automation operations are blocked unless a
dedicated reviewed capability supports them. Do not search for an equivalent
command to bypass a denial. Tool filtering may disable the aggregate cli_call
entry entirely; use the named allowed tools instead.

Installed versions differ. Unknown flags should prompt another focused help
lookup, not speculative retries or an automatic upgrade/download.
