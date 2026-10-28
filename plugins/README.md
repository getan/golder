# First-party plugin packages

Each plugin owns its executable, implementation, tests, optional Skills and
operator documentation below one directory. It uses a standard extension
transport; being first-party does not bypass source trust, approval or sandboxing.

| Package | Executable | Transport |
|---|---|---|
| [lark](lark/README.md) | `golder-lark` | MCP stdio |

Host-independent business code stays inside `plugins/<name>/internal/`; generic
host capabilities belong in `internal/extensions` and the public `extension`
contracts. Plugin packages must not import host UI or permission internals.
