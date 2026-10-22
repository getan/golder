# Permissions: one capability decision per call

The permission mode is the whole policy. Risk review is an input to it, not a
second authorization system.

## Modes

| Mode | In-workspace read/write | Outside workspace | Network | Unconfined | Who answers |
|---|---|---|---|---|---|
| `read-only` | read only | deny | deny | deny | nobody; refuse |
| `ask` | needs consent | needs consent | needs consent | needs consent | **one human dialog** |
| `auto` | reviewer decides | reviewer decides | reviewer decides | reviewer decides | reviewer (JEV/local → LLM) |
| `full-access` | allow | allow | allow | allow | nobody; static hard-deny only |

`auto` is deliberately non-interactive: once the reviewer returns `allow` or
`confirm`, every capability the call asked for rides that answer. `contain`
keeps the call inside the file sandbox and never grants an escape; a network
answer the reviewer gave separately still applies inside that sandbox, while
any other un-granted need blocks the call. `deny` blocks. The reviewer chain is
static floor → local rules / JEV → the session LLM only for what remains.

`ask` uses the same reviewer as a filter: a routine `allow` with no capability
expansion is not shown to a person. Everything else produces one request with
the complete `Needs` list.

## Fast path for native readers

`read`, `grep`, `find`, `ls` and `view_image` cannot mutate anything, so the
routine grader settles them as `local` read-only calls before JEV or the LLM.
This rule lives in `judge.LocalReadOnlyVerdict` and is called by BOTH the local
grader and the JEV router, so a configured decision model does not turn every
project read into a model call. It is not an outermost exemption: the static
credential floor runs first, the workspace boundary still asks/records where the
read lands, and the tools' own credential checks remain.

## Reading the approval line

An approval/decision line carries the judge that produced it:

```text
bash · auto · allow · via local → approved (granted: read /outside/file)
bash · ask  · confirm · via llm  → your call
bash · ask  · deny  · via llm   → denied   (needs: write /outside/dir)
```

The source is one of `jev`, `local`, `llm`, `static`. An allow verdict renders
white in every mode, but is hidden by default; set `GOLDER_DEBUG=1` or pass
`--debug` to render it. Containment stays warning-colored and denials/blocks
stay red. The `⚠`/`✗` markers and the wording do not change with the color.

Ratings are concrete capabilities, not high/medium/low: `needs:` names what the
call required, `granted:` names what the answer actually allowed. A simple call
with no extra capability has neither suffix.

`confirm` is a grade, not a capability: in `ask` it is the dialog line before
the person answers (`→ your call`); after an approval the transcript note is an
allow with `granted:` if anything was granted. In `auto` a `confirm` proceeds
without a dialog, so its note is an allow/approved outcome as well. It does not
mean "waiting for confirmation" in `auto`.

## The request

A call becomes one `ConsentRequest`:

```text
tool + summary + verdict
Needs:
  • read  /outside/file
  • write /outside/dir
  • network github.com
  • run outside the sandbox
```

An answer grants exactly that list at one lifetime:

- `once` — this call only (the escape bit is always once-only);
- `session` — these paths/hosts until the process exits;
- `remembered` — written to the managed grant store.

The default answer is the least privilege that satisfies the request. A
single click/key covers all dimensions; there is no second network/sandbox
dialog. Static hard-denies (credentials, `sudo`, `rm -rf /`, protected state)
are checked before any of this and cannot be granted in any mode.

## Sandbox denial recovery

A denied path is discovered after the command already ran. It is not a second
approval system; it is one post-execution `ConsentRequest` for exactly that
path:

- `auto` — the reviewer decides alone. An `allow` registers the path for the
  contained retry; `deny`, `contain` and reviewer failure leave the original
  denial in place. `Confirm`/stdin is never called, so auto has no popup.
- `ask` — the reviewer filters an outright `deny`; otherwise the person sees
  the same one capability question and may grant once/session/remember.
- `read-only` — no recovery; the command should not have run.
- `full-access` — no sandbox, so nothing to recover.

The policy is `judge.ReviewRecovery`, called by the execution layer after the
denial. It shares `consentOpts` with the live gate, so the after-the-fact path
cannot drift from the before-the-call one. A refusal is announced as a note and
the failure is returned to the model; the session keeps going.

## Trust

First-run directory trust is bootstrap consent, not a per-call capability. It
is asked once per undecided directory (or skipped with `--approve` / a saved
decision) and gates project hooks plus side-effect tools. It does not reappear
as a second question for a call that already holds a capability. `auto` has no
capability popups; this one startup trust question still exists unless the
directory is already trusted or `--approve` is used.

## Code

- `internal/judge/consent.go` — `Capability`, `Need`, `ConsentRequest`,
  `ConsentGate`, and the mode policy.
- `internal/cli/run/gates.go` — production chain is `trust → consent`.
- `internal/cli/tui/approval.go` — one capability dialog.
- `internal/permissions/permissions.go` — the four mode presets.

The older `BoundaryGate` / `PermissionGate` remain only for legacy tests;
production drivers use `ConsentGate`, and even the sandbox-denial recovery now
asks a single `ConsentRequest`.
