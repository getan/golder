# Changelog

All notable changes to **golder** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

golder is a Go re-implementation of the [pi](https://pi.dev) AI coding agent — a
command-line coding assistant with both a headless script mode and an
interactive REPL/TUI.

## [Unreleased]

### Added
- **`view_image` joins the exploration cell**: it reads one local file like
  `read` does, so its card now coalesces into the same `• Explored` block and
  contributes a `View <path>` summary line. The card renders the call's text
  summary rather than the image, so grouping hides nothing. `webfetch`,
  `websearch` and `memory_search` stay out on purpose: they are parallel reads
  too, but their result is what the person came to see, and folding them would
  trade a page of fetched content for a summary line.
- **Ripgrep preference is stated, not assumed**: the environment block now says
  whether `rg` is on PATH, and the code-search guide names the fallback for a
  machine without it — there the `grep` and `find` tools cannot run at all
  (they are ripgrep-backed), so the shell's own `grep`/`find` is the only route
  left, and the guide says so instead of leaving the model to discover it by
  failing. A `bash` command that fails because `rg` is missing now returns a
  hint with that same fallback, detected from the shapes the shells actually
  print (`bash: rg: command not found`, `command not found: rg`, `rg: No such
  file or directory`) and reported in the result details as `rgMissing`. dash —
  `/bin/sh` on Debian and Ubuntu, so `sh -c` and `#!/bin/sh` scripts — is
  covered too with its own `rg: not found` wording. The match names `rg`
  explicitly and anchors the two ambiguous shapes (PATH-unset at the start of a
  line, dash's at the end), so a missing program called something else, a
  `grep: rg: No such file or directory` from a file literally named `rg`, or a
  message mentioning rg mid-sentence does not trigger it.
- **Out-of-workspace reads ask the user first**: the read-only tools (`read`,
  `view_image`, `grep`, `find`, `ls`) read the workspace and nothing else until
  the person says otherwise. The workspace is the directory they opened, so
  crossing it is a question of consent rather than a risk verdict — which is
  why the gate is a sibling of the permission gate instead of a tier inside it,
  and why **no approval mode waives it**: read-only, ask, auto and full-access
  all ask, because full-access turns off review and sandboxing, not consent. A
  run with nobody to ask (headless, a sub-agent thread) fails closed with a
  message naming the path and the setting that would allow it. Two answers are
  offered, at different sizes: **just this read** rides the call's own context
  (`agentcore.WithReadGrant`, consumed by the call it was asked for), and
  **this directory** registers a session grant (`permissions.ReadRoots`) that
  the file tools consult and the OS sandbox's read whitelist includes, so the
  two sides agree about a granted directory instead of letting a tool read what
  a sandboxed command cannot. The directory offered is the file's own, or its
  parent when that is too broad; granting something that would remove the
  boundary (the filesystem root, `$HOME`, `$HOME`'s parent) is refused outright,
  and **credential material is never put to the user at all** — the static floor
  denies `~/.ssh`, `~/.zshrc` and the keychain, so no dialog offers a keystroke
  that hands them over. Pre-granting skips the dialog: `[permissions]
  readable_roots` in `config.toml`, or `GOLDER_READABLE_ROOTS` (unlike
  `GOLDER_SANDBOX_READABLE`, which only widens the sandbox). The write path is
  deliberately untouched: `apply_patch` still refuses an out-of-workspace target
  even when the call carries a read grant and the session has granted the
  directory for reading.
- **Grep tool flags**: the `grep` tool now takes `ignore_case`, `word`,
  `literal`, `invert`, `files_only`, `type`, `hidden` and `no_ignore` — the
  structured form of the ripgrep options that were the remaining reason to
  shell out. They are whitelisted fields rather than a raw flag passthrough on
  purpose: rg's `--pre` executes a command per file, and this tool is a
  read-only tool that runs without an LLM review, so a passthrough would be an
  unreviewed code-execution path. The code-search guide names those arguments
  instead of advertising the shell: it had kept pointing at `--type` as a reason
  to reach for `rg` through bash long after the `type` argument shipped, which
  invited a pipeline for a search the tool already does — multiline mode is the
  one gap left in the guide. `hidden` also forces `--glob=!.git` (appended
  after any user glob so it wins) to keep the tree search out of object blobs
  and refs — hygiene rather than a boundary, since an explicit path into `.git`
  is not subject to ignore rules; `files_only` takes its own output shape since
  `-l` prints no line numbers. Pattern validation now belongs to ripgrep
  alone: the local `regexp.Compile` pre-check graded patterns with Go's regexp
  while ripgrep matches with the Rust regex crate, and the dialects disagree in both
  directions — `\Qfoo(bar)\E` compiled in Go and was rejected by rg (the search
  failed anyway, after the check passed), while rg's verbose mode `(?x)` was
  refused as "invalid pattern" though ripgrep runs it. rg's own verdict is now
  the only one, with its multi-line parse dump collapsed back into the short
  message the pre-check used to produce. That collapse matches the phrase
  "regex parse error" rather than the prefixed line, because the header is
  version-dependent: ripgrep 13 (Debian 12, Ubuntu 22.04) prints
  `regex parse error:` while 14+ prints `rg: regex parse error:`. Keying off the
  prefixed line left Debian 12 builds with a bare "unrecognized escape sequence"
  where the label belongs — found by running the suite inside a Debian 12
  container, not by reading the code.
- **Context lines in the grep tool**: the `grep` tool takes an optional
  `context` argument (rg `-C`, capped at 50) and returns each match with the
  surrounding lines, so seeing a hit in place no longer needs a shell. The
  no-context output is unchanged, and a context result reports the number of
  matches rather than the number of printed lines. The capture uses ripgrep's
  `--null` form to tell a match from a context line: in the plain form the path,
  line number and text are all separated by `:` or `-`, so `x-1-3-2-y` cannot be
  read as either a context line in `x-1` or one in `x`; the NUL ends the path
  unambiguously and the two forms are then rewritten back into the shape a shell
  would print. The code-search guide no longer advertises context lines (or
  `--type` filters) as the reason to reach for `rg` through bash — that wording
  was an open invitation to leave the workspace boundary, the parallel batch and
  the driver's exploration grouping behind, and context lines were an everyday
  need rather than the exotic case it implied.
- **Context windows in the model lists**: `/model` now shows the catalog's
  context window wherever it knows one — leading each row in the numbered list
  (`1. claude-sonnet-4-5  200K  [low|medium|high]`), in the list header for the
  current model, in the TUI picker rows (`200K context · reasoning: …`), and in
  `/model` / `/provider` switch confirmations. The number behind the status
  bar's percentage is visible where the model changes; unknown models simply
  omit it.
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
- **Conditional models.dev catalog refresh (ETag)**: the once-per-TTL
  `models.dev/api.json` fetch (~5.4 MB) now sends `If-None-Match` with the ETag
  recorded by the previous fetch, so an unchanged catalog answers
  `304 Not Modified` with an empty body and only the freshness stamp moves.
  A cache written by an older schema still forces a full fetch — a `304` on a
  trim that never carried the newer fields would silently "confirm" data it
  does not have.
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
- **Search tools take lists, exclusions and a limit**: `grep` and `find` now
  accept `path` as one directory or a list of them, `exclude` (rg
  `--glob=!X`, also a list — an exclude always beats a matching `glob`), and
  `limit` (default 1000, max 10000) so a "these three packages, minus the
  tests, first N hits" query is one call instead of a shell pipeline.
  `find` gains the rest of `grep`'s narrowing — `type`, `hidden`, `no_ignore`
  — so a name search no longer has to reach for `rg --files` to filter. The
  one-string form keeps working for the common single-directory case, and both
  spellings are declared in the schema (`oneOf`), so validation and the
  decoder agree. A capped result now says what to do about it ("showing the
  first N …; narrow the search with path/glob/exclude, or raise limit") rather
  than printing a bare `[truncated]`, which is what invited the pipeline the
  arguments exist to replace.
- **`/resume --all` and a directory column**: every session row now carries the
  directory the session ran in — the REPL list, the TUI picker's second line,
  and `golder -l` / `golder session list` — so two rows with the same preview
  can be told apart without opening them (`~` shortens `$HOME`; a session
  written before the `cwd` field existed shows `unknown`). `/resume --all` (or
  `-a`) lifts the project filter and lists every project's recent sessions, and
  it is the escape hatch the empty-list and out-of-range hints name.

### Changed
- **File viewing belongs to `read`, and the prompt says so**: the code-search
  guide now forbids viewing a file through `cat`, `sed -n`, `head` or `tail`
  and points at `read` with `offset/limit` instead (bounded, line-numbered, and
  it says when it truncated) — the shell spellings fail silently in ways the
  tool cannot: `sed -n '380,470p'` returns a plausible window of the wrong
  lines once the file has shifted, and a truncated `cat` gives no sign that
  anything is missing. The same guide names the two shapes that stay legitimately
  the shell's (multiline search, and aggregating matches such as `rg -l | xargs`
  or `wc -l`), so the rule reads as routing rather than a ban on bash. The `read`
  tool's own description carries the same instruction, since that is the text
  the model sees when it is deciding how to open a file.
- **`/resume` and `--continue` are project-scoped by default**: a bare
  `/resume` lists the sessions that ran in the current project instead of the
  whole store, where the project is the repository containing the launch
  directory (the nearest ancestor holding a `.git` entry) — so a monorepo keeps
  one history whether golder was started at the root or three levels down, and
  a directory outside any repository is its own project. Nothing is hidden
  silently: a list that is not complete ends with the count it is keeping back
  and names `--all`, an empty project says the project has no sessions rather
  than that the store is empty, and an out-of-range number reports the scoped
  size together with the full one. `--continue` now takes this project's newest
  session and only falls back to another project's with a note naming the
  directory it came from, instead of silently continuing whatever was globally
  newest. Exact ids are never narrowed: `/resume <id>` and `golder --resume
  <id>` still resolve across the whole store, and unattributed sessions (no
  recorded `cwd`) only appear under `--all`. Storage itself is unchanged —
  `~/.golder/sessions` stays one flat store with one `List()`; only the view
  is filtered.
- **Startup wordmark in the block face**: the splash's `GOLDER` is now the
  solid ANSI Shadow blocks — the face the tuios splash uses — instead of the
  hollow box-drawing outline: six rows, 50 cells wide, with the entrance
  unchanged (letters type themselves in left to right, one highlight sweep,
  then the mark rests). Coverage of the face's seven glyphs was measured with
  CoreText before the switch: SF Mono (every upright weight, Bold included),
  Menlo Regular and Italic, and Courier New carry all of them; Monaco carries
  `█` but none of the six double-line strokes, as do Menlo Bold, Menlo Bold
  Italic and every SF Mono italic, so those faces fall back per glyph — what
  the tuios splash already ships. No bold attribute is applied for the same
  reason. The Linux side was measured against the fonts distributions actually
  install (the Ubuntu archive packages, read with CoreText): DejaVu Sans Mono in
  all four styles, Hack, JetBrains Mono and Liberation Mono in Regular and Bold,
  and Ubuntu Mono in Regular, Medium and Bold carry all seven glyphs, and every
  glyph advances the same width in each of them — Bold included. The caveat
  above is therefore a macOS caveat.
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
- **Ctrl+C reads the same everywhere**: interrupting used to speak differently
  in every branch and every front-end — the TUI said `(interrupting the current
  run…)`, then `Run ended: context canceled` or `error: aborted` depending on
  which termination event won a race, while the REPL said `^C interrupted`, and
  two branches (discarding a draft, copying a selection) said nothing at all.
  The same keystroke read as five different things, which is what made it feel
  broken even when it worked. Now `ui.InterruptNotice`/`InterruptingNotice`
  and friends are the single source of that wording: every branch acknowledges
  the press, both front-ends settle on one line, and the settlement prints
  exactly once whether or not the aborting turn-end happened to survive the
  cancellation — the aborting event is dropped when the cancel wins the race,
  so the transcript used to depend on timing rather than on what happened.
  Repeated presses during one run are idempotent (the first cancels and says
  so; the rest are absorbed), which stops the notice repeating and reading as
  "the key did nothing". An interrupt is also no longer filed as an error
  anywhere in the pipeline: `EventStream.Result` prefers an outcome the
  producer already recorded over a cancelled ctx, the provider transport and
  the runtime's terminal-message builder classify a cancelled ctx as `aborted`
  (not `error`), and the REPL/goal/btw notices were brought in line. A real
  failure still reports as one — only cancellations changed classification.
  Interrupt latency was measured rather than assumed: cancelling stops a run in
  ~20µs mid-stream, ~9µs mid-tool, ~65µs while parked on an approval — the
  visible delay is the distance to the next cancellation point, and the only
  case that can exceed it is a tool that ignores its context (measured at the
  tool's own duration; the loop then aborts the rest of the batch without
  running it). The measurement itself is a test
  (`internal/runtime/interrupt_latency_test.go`), so a future change that stops
  honoring cancellation fails loudly.
- **`todo` accepts an omitted status**: a submitted item with no `status` is
  read as `pending` instead of being rejected by schema validation. Measured
  across 82 stored sessions, this was the only tool-argument validation failure
  in the whole store — three occurrences, every one of them a `todo` call
  missing `status` on a not-yet-started entry, each costing a full round trip
  for a field with exactly one sensible reading. The schema no longer requires
  it (`content` still is), and an explicitly wrong status is still refused:
  inferring intent from `done` would be guessing.
- **`golder update` no longer reports "already up to date" during the release
  window**: latest-tag discovery asks the golder site first (reachable from
  mainland China, no anonymous rate limit) and that endpoint is edge-cached, so
  right after a release it can still answer with the previous tag — exactly when
  someone runs `golder update`. Observed on the v1.2.15 release: the endpoint
  served a snapshot with `age: 3368` (v1.2.14) while GitHub already had v1.2.15,
  and since a successful site answer was taken as final, the GitHub fallback
  never ran. `LatestTag` now takes the running version and, when the site's
  answer is not newer than it — the answer that makes the command say "already
  up to date", or the tag a source build would install — cross-checks the GitHub
  API and takes the newer of the two. The common case (the site reports an
  upgrade) still costs a single request, and a failed cross-check keeps the
  site's tag, so mainland China loses nothing. The site endpoint itself moved
  from a fixed one-hour cache to stale-while-revalidate (serve the snapshot
  immediately, refresh in the background) with `x-cache-status` /
  `x-cache-age` / `x-cache-fetched-at` headers for the next time this is
  diagnosed, and gained a token-guarded `POST /api/purge` that the release
  workflow calls so a fresh release invalidates the snapshot outright; without
  the token both sides degrade quietly (the endpoint answers 503, the workflow
  logs a notice and continues).
- **Expanded sub-agent output no longer pins a CPU core**: `wrapToWidth`
  advanced with a Truncate/TruncateLeft pair per segment, re-scanning the
  remaining text each time — quadratic in the accumulated output, so a few
  tens of KB made every render tick burn a core for seconds (and the test
  suite minutes under `-race`). It now decodes the string once into graphemes
  and escapes, keeping the open SGR attributes across a line break so wrapped
  colored output stays colored on every row. The old implementation's phantom
  escape-only segment (one extra blank line on colored output) is gone too.
  A performance regression test now pins the linear cost, and CI's test step
  runs with `-timeout 6m` (tighter than Go's 10m default) so the next such
  slowdown fails the build instead of creeping up on the default unnoticed.
- **Startup no longer waits for the models.dev catalog refresh**: the refresh
  held the same mutex lookups serialize on, and the new startup seeds resolve
  their context window right after main kicks the background fetch off — so
  the TUI/REPL blocked until the ~5.4 MB download (or its 20s timeout)
  finished. Every launch after the catalog schema bump had a stale cache, so
  this hit the first start of every existing install. Fetches now serialize on
  their own lock, separate from the state lock: a lookup arriving mid-fetch
  reads the previous snapshot and returns immediately, and the refreshed
  values land when the download completes.
- **Mirror-served checksums are now cross-checked**: both `golder update` and
  `install.sh` used to accept `checksums.txt` from whichever source answered
  first, so a malicious mirror could poison the archive and its digest
  together and the sha256 check would "pass". GitHub direct is the trust
  anchor and is still tried first; when only mirrors are reachable, a copy is
  accepted only if every responding source agrees on the digest for this
  archive, and disagreement aborts as possible tampering. A lone answering
  mirror (a blocked network with nothing else) still installs, with an
  explicit warning instead of silent trust. `GOLDER_MIRROR` overrides the
  mirror list as before.
- **Installer fallback log now names the file it is fetching**: `install.sh`
  downloads the archive and `checksums.txt` independently, so a run where both
  fell back to a mirror printed two unlabeled `下载失败` lines that read like
  the same failure twice. Each line now carries its target name, and the last
  source no longer claims to "try the next source" when none is left (mirroring
  the Go updater's already-labeled notices).
- **Slow release downloads now fail over instead of crawling**: the download
  watchdog only aborted an attempt that delivered *zero* bytes for 15s, so a
  source trickling at a few KB/s — the "connected but crawling" mirror case —
  was accepted and could take tens of minutes (or, in `install.sh`, forever).
  `golder update` and `install.sh` now share a real floor: an attempt whose
  windowed average stays below 50 KB/s for 15s is aborted and the next mirror
  is tried. The per-attempt wall-clock cap in `golder update` moves from 60s
  to 5 minutes so a source that does hold the floor (~3.5 min for the ~10 MB
  archive) can finish instead of being cut off mid-transfer.
- **Context gauge and auto-compaction now use the model's real window**: the
  budget was hardcoded to 1M, so a 200K model (Claude Sonnet 4.5, MiniMax) read
  as a fifth of its real usage and never crossed the compaction threshold until
  the provider rejected the request. The window now comes from models.dev's
  `limit.context`, extracted from the same catalog fetch and 24h disk cache the
  reasoning ladder already uses (no extra request), and `/model`, `/provider`
  and `/resume` re-resolve it so the gauge and the threshold follow the model
  actually in use. The catalog is a hint, not a hard dependency: unknown
  models, custom base URLs and a cache written before this change keep the
  1M fallback (a case-only difference from models.dev's brand casing still
  matches — models.dev writes `MiniMax-M2.5`, gateways list it lowercase),
  and an out-of-date cache still serves its reasoning levels while being
  refetched on the next lookup instead of waiting out the 24h TTL (it reads
  as an unknown window, never as a zero-token one). The Zen gateway
  (`opencode-zen`) now maps to models.dev's `opencode` entry, which it
  previously missed, so its models report real windows too.
- **Anthropic context accounting now counts prompt-cache tokens**: the
  Anthropic wire excludes cached content from `input_tokens` and reports it in
  `cache_read_input_tokens` / `cache_creation_input_tokens`, which golder
  dropped — a cached session read low and auto-compaction fired late on
  anthropic-wired providers (anthropic, minimax, bedrock, Cloudflare).
  `Usage` carries both counts now and context accounting folds them in
  (parity with pi and codex).
- **Early-ended runs no longer clear the context gauge**: a run that stopped
  before its first turn boundary (error/abort mid-stream, a terminating tool
  batch) emitted a telemetry summary with no window, and the TUI responded by
  hiding the context segment it had been showing. The run now records the
  final context figure at `finish`, so the summary carries a live window on
  every exit path.
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
