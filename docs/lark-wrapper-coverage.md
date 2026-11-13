# Lark helper 1.5.68: expanded wrappers

Named MCP tools are the first choice for operations they support. `cli_help`,
`cli_call` and `cli_raw` remain available for unsupported capabilities; a CLI
fallback must never bypass an authorization denial or missing scope.

- `sheet_structure`: add copy, rename, move, hide, unhide and delete. Delete
  requires the exact `sheet_id`, verifies that ID exists in the selected workbook
  before dispatch, requires exact-call host approval and appends CLI `--yes`.
  A well-formed post-write workbook listing can confirm the ID is absent;
  otherwise the result remains unverified. These checks cannot prevent concurrent
  edits by other users between preflight and write.
- `sheet_read`: cell search. `sheet_write`: replace and high-risk clear, with
  explicit preview via `dry_run`. This does not imply full coverage of Sheets.
- `drive_write`: copy a resource or create a folder. Copy requires
  `docs:document:copy`; do not treat a failed or preview copy as an existing
  copy and never edit the original in place of the requested copy.
- Calendar: free/busy and RSVP. Task: reopen and assign. Wiki: copy a single
  node (without descendants). Existing named tools remain available.
- Successful Lark CLI `--dry-run` requests are reported as `not_executed` /
  `preview`, not `submitted`. Ambiguous or contradictory dry-run flags are not
  treated as proof that a write was preview-only.
- Host exact-call policy covers the new `drive_write` as well as existing
  calendar/task/wiki writes and the `cli_raw`/file-transfer aggregate entry
  points. Existing source configurations inherit this protection from the host.

These are contract/unit tests against pinned upstream command definitions, not a
claim of real-account acceptance or equivalence to every Feishu/Doubao feature.
No user's Lark data is touched by the tests. To activate, bundle helper 1.5.68
in a new GUI release, update/review the managed helper, and refresh the session's
MCP catalog.
