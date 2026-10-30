# Contributing to golder

Thanks for considering a contribution. Bug reports, focused fixes, documentation,
tests, and carefully scoped feature proposals are all welcome.

## Before you start

- For a bug, search existing [issues](https://github.com/getan/golder/issues) and
  include the golder version, OS, terminal, and a small reproduction.
- For a substantial change, open an issue or discussion first so we can agree on
  the behavior and boundaries before you invest in implementation.
- Avoid broad refactors bundled with a behavior change. Small, reviewable changes
  are easier to test and land.

## Development setup

golder is written in Go. The current source requires Go 1.27.1 or newer; check
`go.mod` for the authoritative version. Linux test runs need `bubblewrap` and
`ripgrep` installed, as in the CI workflow.

```sh
go test ./...
go vet ./...
```

The CI workflow additionally runs formatting checks and the race-enabled test
suite. Run `gofmt` on changed Go files. Do not claim Linux/container validation
based only on a macOS run.

## Pull requests

- Keep each PR focused and explain the user-visible problem it solves.
- Add or update tests for behavior changes; update docs when commands, defaults,
  security boundaries, or compatibility change.
- Include the commands you ran and their results in the PR description.
- Never include API keys, credentials, private session data, or unredacted
  diagnostic logs.
- Avoid unrelated formatting churn. Maintainers may ask for a design discussion
  before accepting changes to security, permission, or extension boundaries.

## Security issues

Do not file exploitable vulnerabilities with sensitive details in a public
issue. Contact the maintainers privately through GitHub so the report can be
handled without exposing details before a fix is available.
