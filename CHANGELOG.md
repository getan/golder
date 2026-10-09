# Changelog

All notable changes to **golder** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

golder is a Go re-implementation of the [pi](https://pi.dev) AI coding agent — a
command-line coding assistant with both a headless script mode and an
interactive REPL/TUI.

## [Unreleased]

### Added
- **Hardened installer**: `install.sh` now verifies the downloaded archive against
  the release's `checksums.txt` (sha256; a mismatch aborts the install, and
  `GOLDER_SKIP_CHECKSUM=1` opts out), rejects version strings outside
  `[A-Za-z0-9._-]`, and resolves the latest tag from
  `https://golder-cli.pages.dev/api/latest` first, falling back to the GitHub API.
  The canonical one-liner moves to `https://golder-cli.pages.dev/install.sh` — the
  site mirrors the script for networks where `raw.githubusercontent.com` is
  unreliable; the raw URL keeps working, and the README documents both because
  reachability varies by network (e.g. Alibaba Cloud ECS cannot reach pages.dev).
- **Mainland-China download fallback** (`golder update` + `install.sh`):
  downloads try the direct GitHub URL first and then community mirrors
  (`ghfast.top`, `ghproxy.net`, `gh-proxy.com`, `gh.zwy.one`); an attempt that
  connects but delivers no data for 15s is aborted and the next source is tried.
  The archive is still verified against the release's `checksums.txt` — a mirror
  serving a corrupt or stale copy fails over to the next source. `GOLDER_MIRROR`
  overrides the list (whitespace-separated URL prefixes) or disables mirroring
  with `off`/`none`.
- **REPL startup upgrade hint**: the cached latest-release check now refreshes
  for both interactive drivers (previously the TUI only), and the REPL prints
  the same one-line `Update available: vX.Y.Z — run "golder update" to upgrade`
  notice the TUI banner shows. Cache-only (no network on the hot path) and
  silent for dev or up-to-date builds.
- **`/provider` command**: lists every built-in provider with the environment
  variable(s) it reads (in precedence order) and whether a credential is
  configured — no README lookup needed. Providers with a credential found are
  sorted first, so the ready-to-use ones are quick to pick; `/provider <name>`
  switches the live provider to its `DefaultModel` (clearing a previous
  `--base-url`/`--protocol` override and the previous gateway's cached model
  list). The TUI opens the same list as an arrow-key picker that mirrors the
  text ordering. Credential detection is a presence probe: variable names may
  be shown, values never are.
- **Two-step model + reasoning picker**: choosing a model now leads to its
  reasoning-level picker (TUI) instead of leaving `/think` to be discovered
  separately — Esc on the level stage cancels both. The bare `/model` list
  annotates every model with the levels it advertises, and
  `/model <n|id> <level>` switches model and level in one step. Levels come
  from models.dev's per-model metadata (the gateways' own `/models` answers
  carry no effort information), so opencode-go, first-party APIs, and future
  providers all get their real ladders; unknown models fall back to the
  hand-maintained family table.
- **Context budget tooling** (`get_context_remaining` / `new_context`): the
  model can now see and steer its own context budget, the same affordance
  codex exposes. `get_context_remaining` reports the live remaining tokens;
  `new_context` asks the loop to roll the window over at the next turn
  boundary (summarize finished work, keep recent messages — the model-side
  counterpart of `/compact`) and refuses gracefully when compaction is
  disabled. The loop observes usage each turn and publishes one shared
  `contextbudget.State` into the run context (the message-snapshot pattern),
  so prompts, task children, and every driver see consistent figures;
  headless also adopts the same default window the REPL/TUI assume. A
  low-budget `<system-reminder>` fires once per crossing (<=10% remaining, an
  escalated warning at <=3%) to prompt landing the work, and the reminder
  ladder survives across prompts in the interactive drivers. Both tools are
  registered read-only / ungraded by the judge.
- **view_image**: `view_image` reads a local image (PNG/JPEG/GIF/WebP, up to
  8 MiB, bounded to the workspace or the skills root like `read`) and attaches
  it to the conversation for visual inspection. Tool-result images now have a
  wire path on every provider that supports them: Responses sends
  `function_call_output` content items (`input_text` + `input_image` data
  URIs, the same shape codex uses), Anthropic sends content blocks on
  `tool_result`, and Chat Completions — which has no image slot on a tool
  message — degrades to a text placeholder instead of dropping the block.
  Read-only class in trust (gated only under `strict_reads`) and ungraded by
  the judge.
- **apply_patch (codex-style editing)**: `write` and `edit` are replaced by a
  single `apply_patch` tool carrying a whole patch per call — add, update,
  move, and delete any number of files, in the `*** Begin Patch` format.
  Update hunks match fuzzily (exact → trailing whitespace → leading/trailing
  whitespace → Unicode punctuation normalized, ported from codex's
  `seek_sequence`), path and context errors are decided before anything is
  written, and every touched path is snapshotted for `/rewind`. `grep` and
  `find` now delegate to ripgrep (with an actionable error when `rg` is not
  installed). Breaking change: `--allowed-tools`/`--disallowed-tools` and
  hook matchers must name `apply_patch` instead of `write`/`edit`.
- **Permission modes + LLM review (`/permissions`)**: golder gains codex-style
  approval presets — `read-only` (mutating tools blocked), `ask` (low risk
  runs, the rest prompts), `auto` (default), and `full-access` (no review, no
  sandbox; the static hard-deny floor remains). Switch at runtime with
  `/permissions` (an arrow-key picker in the TUI with per-mode
  descriptions), or set `--permissions` / `GOLDER_PERMISSIONS`; the live state
  applies to the very next tool call and task children inherit it. The
  `auto`/`ask` reviewer is the session's **active model** (no separate
  classifier service, no `TYPESAFE_API_KEY`; it follows `/model` switches):
  a compact guardian-style transcript plus the exact call are graded into
  Allow/Confirm/Sandbox/Deny with risk/authorization ratings and a rationale
  written in the user's conversation language, cached per (tool, args, trust,
  state). Decisions render as codex-style cards above the call
  (`⚠ 自动审批通过（bash，风险：中，授权：高）：…` / `✗ 自动审批拒绝…`). Any
  reviewer failure fails closed (sandbox when possible, otherwise block);
  `read-only` blocks `bash`/`apply_patch` at both the gate and the bash tool,
  so in-process sub-agents cannot outrun the mode either. Sandbox-tier bash
  commands still run under a per-command `sandbox-exec` profile
  (`internal/seatbelt`, project-scoped writes, secret dirs denied) via
  `GOLDER_SANDBOX=auto|enforce`. `GOLDER_JUDGE=off` is kept as a legacy alias for
  a `full-access` default.
- **Interruptible confirmation prompts**: Ctrl+C during a trust or risk-judge
  prompt now denies immediately instead of trapping the user until they
  answer (reads race the run context); the first-run trust dialog falls back
  to its session-only default on SIGINT.
- **Tool-level admission control**: `--allowed-tools` / `--disallowed-tools`
  (repeatable, comma-separated, case-insensitive) narrow the tool set handed to
  the model, filling the gap between the full set and `--no-tools`. Deny wins
  over allow on conflict; task sub-agents inherit the boundary; an unknown tool
  name aborts with exit code 2 instead of being silently ignored. Because
  filtering happens at the tool-registration layer — before the side-effect
  confirmation gate — `--approve` waives confirmation prompts but cannot widen
  the boundary. Also configurable as `allowed_tools` / `disallowed_tools` in
  `config.toml`.
- **Self-update**: `golder update` with no package name (or flags-only calls such
  as `golder update --check`) now upgrades the golder binary itself to the latest
  GitHub Release, replacing the executable in place (with a `sudo` hint when the
  target path needs elevated permissions). (#465, #466, #468)
- **Startup upgrade hint**: the TUI banner shows the version row and, when a
  newer release is available, a `Run golder update to upgrade` hint backed by a
  24h cached background release check. (#467)
- **`!command` passthrough**: a composer line starting with `!` runs the
  command locally — no model call, no approval, no sandbox — and joins the
  conversation as codex's user-role `<user_shell_command>` record (command,
  exit code, duration, and head+tail bounded output), so the model sees exactly
  what the user ran and what came back. The TUI announces a live `• Running …`
  card that streams the command's output and settles as `• You ran …`; a
  record that lands mid-run is parked and merged when the run drains, so it
  never races the loop that owns the context. Works in the REPL and the TUI,
  the raw `!` line is what ↑ recalls, and a bare `!` prints the usage hint.
- **Ctrl+Z suspend**: pressing Ctrl+Z in any state hands golder back to the
  terminal's job control (bubbletea releases the terminal and raises SIGTSTP;
  `fg` resumes with the layout re-laid out), matching codex and every other
  terminal UI.

### Changed
- **Provider config lives in one registry**: `ProviderSpec`
  (`internal/provider/registry.go`) now carries everything about a provider —
  transport/auth metadata plus `ModelPrefixes` (model-name → provider
  inference), `ModelsDevID` (models.dev catalog key), `ReasoningLadders`
  (gateway-specific effort quirks), and `DefaultModel` (the model a bare
  provider name resolves to). The former prefix table (`infer.go`) and the
  curated preset catalog (`presets.go`) are gone — inference and defaults read
  the registry, so adding a provider is a single entry. Consequence: curated
  namespaced ids that used to route implicitly (e.g. NVIDIA's
  `meta/llama-3.3-70b-instruct`) now need `--provider nvidia` or an
  `nvidia/`-prefixed id; `claude-*`/`gpt-*`/`gemini-*`/CN-cloud ids are
  unaffected (name inference covers them).
- **Reasoning ladders resolve in layers, shared by both wires**: the models.dev
  catalog first (authoritative, per provider), then the provider's
  `ReasoningLadders`, then the shared model-family table, then the conservative
  low/medium/high default. DeepSeek keeps its full ladder, `max` included; Muse
  spark caps at `xhigh` on opencode-go (its `max` is advertised in the error
  text but rejected on the wire). Chat Completions and Responses agree by
  construction. The effort engine and the models.dev catalog now live in one
  file (`internal/provider/reasoning.go`, was split across two).
- **Catalogs are cached 24h on disk, one mechanism**: the provider model list
  fetched by `/model` now persists to `~/.golder/model-catalog.json` with the
  same TTL and file plumbing as the models.dev reasoning catalog, keyed by
  provider + endpoint. A provider is queried about once a day instead of once
  per session, and a stale list is served when the endpoint fails rather than
  breaking the picker.
- **Thinking-level layering fixed**: `--thinking-level` now defaults to unset,
  so `~/.config/golder/config.toml`, `.golder/config.json`, and
  `GOLDER_THINKING_LEVEL` actually take effect; the built-in default moved to
  `max` (was `medium`) and the default model is now `deepseek-v4.1-flash`
  (opencode-go). README documents the defaults and that
  `muse-spark-1.3-contributor` needs a US egress IP (`GOLDER_PROXY`).
- **Startup wordmark**: the splash mark is now the word "golder" set in a
  rounded line face (box-drawing strokes), replacing the spinning single-stroke
  G. The entrance is a short, self-stopping animation — the letters type
  themselves in left to right and a highlight sweeps across the word once —
  after which the banner rests as a static history cell. Fresh sessions play
  the entrance; resumed sessions paint the settled word immediately.
- **项目更名为 golder**：模块路径 `github.com/getan/golder`、命令与二进制
  `golder`、数据目录 `~/.golder`、项目配置 `.golder/`、全局配置
  `~/.config/golder/config.toml`，环境变量前缀统一为 `GOLDER_*`。旧的
  `PIGO_*`、`~/.pigo` 不再读取，升级时需手动迁移（`mv ~/.pigo ~/.golder`）。
- **Risk grading now sees conversation context**: the reviewer state carries a
  guardian-style transcript — up to three recent user turns (intent is
  selected even when the newest entries are all tool output) plus the three
  newest entries of any kind, tool output capped at 1k runes per entry and the
  whole state at 8k — so a command explicitly requested by the user is graded
  differently from the same command appearing without authorization. The
  verdict cache key now includes a state fingerprint: the gate and the
  execution layer still share one grade per call, but a verdict is never
  replayed across changed context. Callers outside a run loop (remote-control
  confirm) degrade to context-free grading.
- **bash sessions replace background jobs**: `bash` now runs under a shared
  session manager (`internal/execsess`). A command still running after
  `yield_time_ms` (default 10s, capped at 30s) hands back a `bash_id` instead
  of dying at a default 2-minute timeout; an explicit `timeout_ms` remains a
  hard deadline that kills the process group. `bash_output` / `kill_bash` and
  the `run_in_background` flag are gone: one `write_stdin` tool polls
  incremental output or sends `\u0003` (Ctrl-C, a second interrupt force-kills).
  Session output is retained in a bounded 1 MiB head/tail window and sessions
  cap at 64 with codex-style capacity eviction: the 8 most recently used
  sessions are protected, then the least recently used exited session is
  dropped, and only if every candidate is still running is the least recently
  used live one terminated to make room (no more rejected spawns at the cap).
  REPL/TUI/headless/sub-agent teardown kills leftovers. Breaking
  change: `--allowed-tools` / `--disallowed-tools` and hook matchers must name
  `write_stdin` instead of `bash_output` / `kill_bash`.
- **PTY sessions (interactive stdin)**: `bash` accepts `tty=true`, running the
  command on a pseudo-terminal (24x80, `TERM=dumb`/`NO_COLOR=1`/pagers
  disabled) so `write_stdin` can feed arbitrary input — `python -i`,
  `git add -p`, database shells and similar tools now work. `\u0003` (Ctrl-C)
  keeps its process-group interrupt semantics on both transport kinds; pipe
  sessions still reject ordinary input with a hint to rerun with `tty=true`.
  PTY support is Unix-only (darwin/linux, built on `golang.org/x/sys`); a
  `tty=true` request on Windows fails closed with a clear message.
- **Diff cards render like codex, and resume rebuilds real cards**: an
  patch card carries the change counts in its headline
  (`apply_patch path (+137 -0)`, additions green, removals red), suppresses the
  tool's own "Applied N change(s)" summary as a duplicate, lists multi-file
  diffs under `└ path (+N -M)` headers, and paints added/removed rows with
  codex's diff washes (dark `#213A2B`/`#4A221D`, light
  `#dafbe1`/`#ffebe9`) while the code itself stays syntax-highlighted (tabs
  expand to four columns so the code keeps its indentation — diff rows no
  longer run through the command wrapper, which folds whitespace); the
  `---/+++` pair folds into the counts header. Patch cards default to
  expanded, so the change is readable without a keypress. Resuming a session
  now replays every tool call as the card the live run showed — paired with
  its recorded result, diff included — instead of compact one-line summaries;
  a call that never ran replays as a warn card, matching the live closeout.
- **No more tool-turn cap**: the loop no longer force-stops a run after a fixed
  number of tool-calling turns (previously 40, inherited from pi). Like codex,
  a run ends when the model stops calling tools or the user interrupts, so
  long-horizon work is never cut off mid-task. The web-search repeat guard and
  the pure-hosted-turn settle rule still bound their own loops. A TUI card that
  is still open when a run ends (e.g. interrupted mid-batch) now closes to a
  terminal state instead of showing "Running" forever.
- **`golder update` semantics**: a no-argument `golder update` no longer updates
  every installed package; it now self-updates the binary. Update packages
  individually with `golder update <name>`. (#468)
- Internationalized the codebase: all user-facing strings and internal comments
  were translated from Chinese to English.
- Documented self-update and the revised `update` semantics in the README and
  the docs site.
- **Todo cards read at a glance**: checklist rows are styled by status — the
  in-progress step is light-green bold, completed rows are dim struck-through
  gray, and pending rows stay plain gray (codex's plan-cell treatment), so the
  active step is the only row that draws color.
- **Unseen-output notice moved to a bottom-right toast**: "N new lines ·
  Ctrl+E to jump" no longer claims the status bar (back to the persistent
  readout only); it floats over the transcript's bottom-right corner for a
  few seconds, and only when output is actually waiting below the fold.

### Fixed
- **The running-session note now spells out Ctrl-C**: a bash call that
  outlived its yield window ended with "send chars `\u0003` to interrupt",
  which reads as gibberish to anyone but the model; it now carries the same
  `(Ctrl-C)` gloss as the other interrupt hints, so the transcript stays
  readable for humans while the model still sees the literal it must send.
- **No phantom "new lines" notice at startup**: the banner filling the
  not-yet-pinned viewport was counted as unseen content, so a fresh session
  could greet the user with "↓ 8 new lines · Ctrl+E to jump". The transcript
  now starts pinned to the bottom, and growth that the viewport can still show
  never counts as unseen.
- **Ctrl+C clears the composer before quitting**: with a draft in the input
  box, the first Ctrl+C now discards it (and any paste/image placeholder
  bodies) — the shell-like cancel — instead of arming the quit; only presses
  on an empty box keep the two-stage interrupt/quit role, and a run in flight
  still owns Ctrl+C.
- **Resuming a session heals unanswered tool calls**: a session written by a
  run that stopped between the assistant message and its tool execution (the
  removed tool-turn cap did exactly this, as does an interrupt) contains a
  function call with no output, and the provider rejects the next request with
  "No tool output found for function call …" — so the first prompt after
  `/resume` failed. Every run now repairs such calls up front with a synthetic
  error result placed directly after its call, so the history is valid again
  and the repaired context is persisted with the next save.
- **Sandbox profile now covers symlinked roots**: macOS temp trees live under
  `/var`, a symlink to `/private/var`, and sandbox matches canonical vnode
  paths — a profile carrying only the symlinked spelling silently denied the
  writes it meant to allow (the command just failed, with no sandbox error).
  Project and temp dirs are now emitted in both spellings, and the rules are
  covered end to end by tests that actually run `sandbox-exec`.
- **Deny blocks are actionable and no longer point at the kill switch**: a
  hard `Deny` replies with what to change (avoid privilege escalation,
  destructive scope, or credential material; split into smaller steps) instead
  of pointing at a global off switch, which taught the model to disable the
  whole gate. The static floor and the sandbox-tier fail-closed path are
  unchanged.
- **Sandbox-tier calls now actually run sandboxed**: the judge gate used to
  fail every Sandbox verdict closed in drivers without a stdin prompt
  (TUI/headless), and to show a misleading "[sandbox]" note in the REPL, even
  though the bash tool had a seatbelt runner attached — so a sandboxed call
  could never proceed. The gate now asks the wiring layer
  (`run.SandboxGate`) whether the execution layer will isolate this call and
  lets those through (isolation replaces both the block and the prompt); tools
  with no runner and platforms without `sandbox-exec` keep failing closed. A
  consistency test pins the predicate to `WireBashSandbox`'s runner
  attachment, and the block message format was corrected.
- **apply_patch accepts several hunks in one file section**: a single
  `*** Update File:` block may now carry multiple `@@` hunks the way a git
  diff does. Previously the second `@@` was swallowed as a hunk body line and
  the parse failed with a misleading "chunk lines must start with ..." error;
  the message now names the offending line, the model-facing patch guide
  documents the one-section/many-hunks form, and a test mirrors codex's
  multi-hunk single-file case.
- **Working-spinner token estimate covers every output channel**: the live
  `↓ N tokens` readout only counted visible text, so reasoning-heavy or
  patch-heavy turns showed a number far below the real output (measured 5–40×
  low against session `usage.outputTokens`). Thinking text and tool-call
  argument JSON are now measured against the cumulative stream partial
  (growth-only deltas, reset per message) with a turn-end flush for providers
  that deliver the message whole — still an activity gauge, not billing.
- **Code blocks no longer render mis-analysed content on a red chip**: glamour
  highlights every code block through chroma, and a block with no language tag
  is auto-analysed — when the guess tags prose as syntax errors, the stock
  style paints those Error tokens on a saturated red background (#F05B5B dark
  / #FF5555 light), so a directory listing could render as a wall of red.
  Error tokens now reuse the block's own text style in both renderers (TUI
  transcript and REPL), with the shared glamour configs left untouched.

## [0.4.3] - 2026-07-31

### Added
- Generic **task tool** for sub-agent fan-out, with a concurrency semaphore and
  a nesting guard; advertised in the system prompt. (#454, #458)
- **Sub-agent progress reporting**: `SubAgentProgressEvent` and a context-carried
  progress emitter (#453), surfaced through a loop-injected emitter (#455),
  printed to stderr in headless mode (#457), and shown in a multi-line sub-agent
  status panel in the TUI. (#456)
- TUI prompt-history navigation, plus sub-agent panel and credential fixes.
- Subagent-orchestration PRD and SPEC.

## [0.4.2] - 2026-07-31

### Added
- **Remote control**: pair a phone/browser with a running REPL over LAN. Includes
  a pairing token and session credential store (#438), LAN address / free-port
  resolver (#439), terminal Unicode QR renderer (#440), HTTP + WebSocket server
  (#441), a REPL bridge seam (#442), a responsive web SPA (#444), and REPL
  wiring with server hardening. (#443, #445)
- `--cwd`/`-C` flag to run golder as if launched in a given working directory.
- `/think` slash command to switch reasoning effort at runtime.

### Fixed
- Avoid a slice panic when persisting a session after compaction.
- Auto-scroll the TUI to the newest output on submit.

## [0.4.1] - 2026-07-30

### Added
- **User-extensible hooks**: core `internal/hooks` infrastructure (#417), layered
  `ConfigLayer.Hooks` with append-merge (#418), an `InstallHooks` assembly helper
  (#419), and wiring for PreToolUse/PostToolUse (#420), UserPromptSubmit with
  block + one-shot injection (#421), Stop/SubagentStop (#422), SessionStart (#423),
  and a `HookNotifier` for observer events (#424), converged across all six
  drivers (#425).
- Runnable hook examples, README Hooks section, and security notes (#426).
- `config.toml.example` template and a TUI screenshot for the README.

### Fixed
- Bound the hook runner timeout with `WaitDelay` so orphaned grandchildren can't
  block `Run`. (#437)
- Show tool arguments in the TUI tool-card header.

## [0.4.0] - 2026-07-29

### Added
- **Full-screen TUI** (Bubble Tea v2): skeleton with default entry gating (#384),
  theme layer and width-aware rendering (#385), a persistent bottom status bar
  (#386), an AgentEvent→`tea.Msg` bridge (#387), streaming transcript with
  viewport scrolling (#388), rich tool-call cards (#389), multi-line input with
  CJK editing and two-stage interrupt (#390), slash commands with an autocomplete
  popup (#391), and real run-seam binding with session resume/persistence. (#392)
- Startup logo + config splash, image paste (Ctrl+V/Cmd+V), multi-line paste
  collapsed into a placeholder, and mouse-selection copy.
- lipgloss v2 upgrade; slash-command registry sunk into `internal/cli/prompts`. (#393)
- TUI agent PRD and SPEC.

### Fixed
- Numerous TUI rendering fixes: scrollbar visibility and alignment across
  reflows, Shift+Enter newline reliability (with Ctrl+J/Alt+Enter fallbacks),
  CJK/IME input, terminal query-reply leakage into the input box, and blank-line
  turn separation. (#403–#416)

## [0.3.7] - 2026-07-28

### Added
- **Prompt templates**: shell-style arg tokenizer (#344) and expansion engine
  (#332) with tiered slash-command priority (#335), argument-hint frontmatter
  (#334), `--prompt-template`/`--no-prompt-templates` flags (#339), settings-tier
  `prompts` from `config.toml` (#338), project-level `.golder/prompts` when trusted
  (#337), `~/.golder/prompts` alongside legacy `~/.golder/commands` (#336), and
  autocomplete/`/help` listings with argument-hint and source tier. (#340, #341)
- Install prompt packages to `~/.golder/prompts`. (#342)
- Honor `~/.config/golder/config.toml` over built-in defaults.

### Changed
- Large CLI refactor: `cmd/golder/main.go` converged to a thin entry, with the REPL,
  `/status`, `/btw`, `/goal`, headless/subagent paths, pkgcmd, run-assembly,
  provider resolution, config loading, trust glue, and UI helpers each migrated
  into dedicated `internal/cli/*` packages. (#357–#369)

### Fixed
- Include the model in the Anthropic Messages request body.
- Gitignore golder config files to avoid committing API keys.

## [0.3.6] - 2026-07-27

### Fixed
- Handle Ctrl+D/Ctrl+C as CSI-u reports in the REPL. (#329)
- Allow file tools to reach the advertised skills dir. (#327)
- Align the cursor with wide CJK runes in the line editor.

### Added
- Documented the interactive REPL and slash commands in `index.html`.

## [0.3.5] - 2026-07-27

### Added
- **Multi-line REPL input**: multi-line buffer with a cursor (#313), Shift+Enter
  newline / plain-Enter submit (#314), cross-line cursor movement and
  Home/End/Ctrl+A/E (#315), mid-line editing and line-merging backspace (#316),
  trailing-backslash line continuation (#317), full-line rendering with cursor
  repositioning (#318), and history record/restore. (#319)
- **Model-invoked skills**: name/description validation and
  disable-model-invocation (#301), a `FormatSkillsForPrompt` renderer (#302),
  `<available_skills>` injection into the system prompt (#303), and run-assembly
  wiring. (#304)
- `/status` slash command with env/credentials/telemetry sections and runtime
  config + context rendering, retaining telemetry across runs. (#291–#297)

## [0.3.4] - 2026-07-24

### Added
- **`/btw` side-thread**: command skeleton (#279), multi-turn follow-up and exit
  interaction (#280), bare `/btw` to reopen the most recent side thread (#281),
  and per-thread model/thinking overrides. (#282)
- **`/goal` autonomous mode** (mirrors pi-goal / Claude Code goal). (#278)
- Bundle built-in skills and install them on first run. (#277)
- Browse prior REPL inputs with up/down arrows on a blank line.

## [0.3.3] - 2026-07-24

### Added
- Render assistant replies as Markdown in the REPL. (#276)
- Cycle REPL input suggestions with the up/down arrows. (#275)

## [0.3.2] - 2026-07-24

### Added
- Recent-input suggestions in the REPL. (#274)

## [0.3.1] - 2026-07-24

### Added
- **pi-extension hosting**: an embedded Node host (`pihost.mjs`) to run pi
  extensions (#263), routing extension launchers through it (#264), plus
  `Plugin.CallCommand` / `Manager.Commands` command aggregation (#262) and
  `commands/call` wire types (#268), registering plugin commands as slash
  commands with prompt injection. (#265)

## [0.3.0] - 2026-07-23

### Added
- **Harness engineering**: a generic system-reminder dynamic-context injection
  mechanism (#248), a unified tool-result trimming budget in the tool executor
  (#250), structured harness telemetry collection (#251), and classification
  with bounded retries for tool-execution failures. (#252)
- `--thinking-level` flag with layered config resolution. (#246)
- **Model-name → provider inference**: an inference table (#234) wired into
  `resolveProvider` (#235) and documented in the README/`--help`. (#236)
- Output byte cap and head/tail preview truncation for the bash tool. (#249)
- Harness capability matrix and baseline docs; `internal` package diagrams.

### Fixed
- Model-quality degradation and strict-gateway compatibility issues. (#240–#244)

## [0.2.1] - 2026-07-22

### Added
- Chinese-market providers: Qianfan, Volcengine Ark, DashScope (Bailian), and
  Hunyuan. (#232)
- README architecture overview with the two-layer agent-loop flowchart and the
  runtime layering diagram.

## [0.2.0] - 2026-07-22

### Added
- **Provider registry**: a central registry as the single source of truth (#182),
  provider-derived API-key resolution (#183), a `--provider` flag to select a
  built-in provider (#184), base-URL override precedence (#185), and wiring for
  all OpenAI-protocol (#186) and Anthropic-Messages (#187) providers, with
  special-auth parameter validation. (#188)
- Curated model catalog expanded with 11 providers and curated models. (#189)
- **The companion book** *Writing the pi Agent in Go*: `book/` scaffolding with a
  pandoc + xelatex + ElegantBook build pipeline (#200), a full 10-chapter outline
  (#201), the introduction (#202), all chapters 1–10 and the afterword
  (#204, #212–#221), sample figures, and a PDF pipeline with SVG embedding. (#205)

### Changed
- Renamed `manual.html` to `index.html` and made the docs site bilingual (zh/en).

## [0.1.2] - 2026-07-20

### Added
- One-click install script (`install.sh`).
- REPL tool-status line coloring: green on success, red on failure.

## [0.1.1] - 2026-07-20

### Added
- CI GitHub Action and goreleaser support.

## [0.1.0] - 2026-07-20

Initial public release. golder lands as a working Go re-implementation of the pi
coding agent.

### Added
- **Agent core**: an `EventStream` mechanism (#18), a tool registry with JSON
  Schema validation (#19), streaming backfill (#20), three-phase tool execution
  (#21), parallel & sequential batch tool execution (#22), and the two-layer
  agent loop `runLoop`. (#23)
- **Provider layer**: a unified `Provider` interface with a dual failure model
  (#25), a shared transport driver (SSE + retry + dual watchdogs) (#26),
  three-stage thinking normalization (#27), decoders for Anthropic Messages
  (#63), OpenAI-compatible (#29), and Gemini (#30), and the first concrete
  providers — Bedrock/OpenRouter/Ollama. (#33)
- **Built-in tools**: `read` (#34), `write` (#35), `edit` with diff (#36),
  `bash` with streaming/timeout/cancellation (#37), `grep`/`find`/`ls` honoring
  `.gitignore` (#38), `todo` task tracking (#127), and `webfetch`. (#128)
- **Model & auth**: a model registry and provider directory (#31), plus OAuth
  and API-key resolution. (#32)
- **Security**: an in-process sandbox gate and secret redaction. (#44)
- **Run modes**: a headless/stdio mode and the `golder` CLI entry (#39),
  headless/stream-json runs carrying a session id with resume support (#176),
  and `--system-prompt` / `--append-system-prompt`. (#180)
- **System prompt & config**: system-prompt assembly with `AGENTS.md` injection
  (#40) and a layered configuration system. (#42)
- **Sessions**: local JSONL persistence and resume (#43); a session tree with
  id/parentId and v3 migration (#121); `/fork`, `/clone` (#122), `/tree`
  navigation (#123), `/export`/`/import` (#124), and `/copy` + `/session`. (#125)
- **Context compaction**: token accounting and trigger detection (#117),
  `FindCutPoint` (#118), summary generation with on-disk `CompactionEntry` (#119),
  and the `/compact` command with automatic loop compaction. (#120)
- **Extensibility**: sub-agent orchestration, skills, and slash-commands (#45);
  a plugin system with a subprocess JSON-RPC 2.0 protocol (#132) and lifecycle
  event subscription (#133); project trust (#134); and process-isolated
  sub-agent mode. (#135)
- **Package manager**: a lockfile and install-dir conventions (#154), `npm:<name>`
  reference parsing (#155), npm detection and content fetch (#156), pi package
  classification (#157), distribution of extensions (#158), skills (#159),
  prompts/commands (#160), and themes (#161), plus `golder install` (#162),
  `list` + `uninstall` (#163), and `update`. (#164)
- **Interactive TUI** (bubbletea v2) with a lipgloss theme layer, width-aware
  rendering, Markdown output, streaming spinners, tool-call cards, viewport
  scrolling, role-styled transcripts, action slash-commands, and a model picker.
  (#41, #89–#95, #103)
- Image input as content blocks with two-provider encoding. (#126)
- Switched to `spf13/pflag` (#87) and split `internal/agent` into `agentcore`,
  `provider`, `agenttool`, and `runtime` leaf packages with a verified layering
  DAG. (#74–#79)

[Unreleased]: https://github.com/getan/golder/compare/v0.4.3...HEAD
[0.4.3]: https://github.com/getan/golder/compare/v0.4.2...v0.4.3
[0.4.2]: https://github.com/getan/golder/compare/v0.4.1...v0.4.2
[0.4.1]: https://github.com/getan/golder/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/getan/golder/compare/v0.3.7...v0.4.0
[0.3.7]: https://github.com/getan/golder/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/getan/golder/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/getan/golder/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/getan/golder/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/getan/golder/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/getan/golder/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/getan/golder/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/getan/golder/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/getan/golder/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/getan/golder/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/getan/golder/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/getan/golder/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/getan/golder/releases/tag/v0.1.0
