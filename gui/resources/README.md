# Resources

Files here ship inside the packaged app (`mygo build`). Keep them data-only:
icons, localized assets and other small static resources.

The GUI runs the Golder Agent runtime in-process and must not carry a second
full `golder` CLI, tuios workspace renderer or Chromium binary. The release
workflow enforces those bundle boundaries and a per-architecture size limit.
