# Resources

Files here ship inside the packaged app (`mygo build`).

`bin/golder` is the CLI a workspace window runs for `golder workspace attach`.
It is not committed: the build workflow compiles it per platform into
`resources/<os>-<arch>/bin/golder` (or `resources/darwin/bin/golder` for a
universal macOS app) before packaging, so a double-clicked app does not depend
on the person's `PATH`.

`golderBinary()` in `../mygoui/terminal.go` looks there first, then
`GOLDER_BIN`, then next to the executable, then `PATH`.
