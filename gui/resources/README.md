# Resources

Files here ship inside the packaged app (`mygo build`). Keep them to icons,
localized/static assets and closed-registry first-party helpers.

The GUI runs the Golder Agent runtime in-process and must not carry a second
full `golder` CLI, tuios workspace renderer or Chromium binary. The release
workflow enforces those bundle boundaries and a per-architecture size limit.

The one executable exception is the generated, architecture-matched
`extensions/lark/golder-lark[.exe]` plus its SHA-256 file. It is a small MCP
helper, not the full CLI. Discovery never runs or installs it; Settings →
Extensions copies it only after an explicit Install click and checksum match.

The release workflow also generates two non-native WebAssembly resources in
`renderers/`: a source-built static SVG renderer and a checksum-pinned Mermaid
module. They run offline in a bounded WASM sandbox with no host filesystem or
network. The workflow includes their third-party license notices alongside the
resources; no Rust toolchain or browser is shipped to users.
