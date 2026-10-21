# Changelog

All notable changes to **golder** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

golder is a Go re-implementation of the [pi](https://pi.dev) AI coding agent — a
command-line coding assistant with both a headless script mode and an
interactive REPL/TUI.

## [Unreleased]

### Added
- **Verdict source is visible in the approval line**: `via local`, `via jev`,
  `via llm` or `via static` sits beside the mode and grade, and the same source
  is carried into notes and stdin prompts. No more inferring the judge from the
  rationale's wording.
- **Native file readers take the local fast path**: `read`, `grep`, `find`,
  `ls` and `view_image` are settled as `local` read-only calls before JEV or the
  LLM. The gate still runs the static credential floor and the workspace
  boundary; this only removes the pointless model round trip for calls that
  cannot mutate anything.
- **Allow verdicts are hidden unless debug is on**: `--debug` / `GOLDER_DEBUG=1`
  enables the diagnostic line; the default transcript no longer prints an allow
  verdict for every call. Denials, containment, and approval dialogs are never
  hidden.
- **Verdict lines name concrete capabilities**: `needs:` lists what the call
  required (outside read/write, network, sandbox escape) and `granted:` lists
  what the answer actually allowed. The old `risk: high` / `authorization:
  medium` words are gone from the note and dialog lines.
- **`apply_patch` recovers from the two typos that used to cost a round trip**:
  an unprefixed hunk line is read as an addition when its neighbours are
  additions (and as context when they are context), with the assumption named
  in the result ("N line(s) without a prefix were read as additions"); an
  unknown argument key is ignored and reported instead of refusing the call;
  and when no context line matches at all, the error names the closest line in
  the file. An unprefixed line whose neighbours disagree is still refused —
  guessing there would silently change the file.
- **`apply_patch` locates a hunk the way Codex does**: an `@@` line is now a
  literal anchor that moves the search forward (exact first, then containment,
  so `@@ func b()` finds `func b() {`), two `@@` lines are two nested anchors
  (codex's class-then-method shape, which the parser used to reject), and a
  stale anchor no longer fails the hunk — it is a hint, not a promise. A hunk
  whose edge context has drifted is retried with leading/trailing context
  dropped, but only while the remainder is unique in the file, and the result
  reports `note: hunk N applied with K context line(s) reduced` instead of
  pretending it was exact. The failure message now shows the closest match with
  line numbers and the surrounding lines, so the model can resend the hunk
  without another read.
- **A failed patch reports everything at once**: `Compute` keeps going after a
  hunk fails — a dry run in memory, nothing is written — so one reply lists every
  broken hunk (with the diagnostics above) and how much of the patch would have
  applied ("3 hunk(s)/change(s) across 2 file(s) would apply; nothing was
  written"). Fixing one hunk no longer costs a round trip per broken hunk, and
  the model knows the rest can stay as it is.
- **The approval envelope**: an approved bash command whose whole effect is
  enumerable (`git push`, `go test`, `ls`, …) now runs on the host instead of
  inside the file sandbox; variables, substitutions, interpreters, wrappers,
  and credential paths keep a call contained. A bare call carries its network
  as part of the same approval, and `GOLDER_SANDBOX=enforce` still contains
  every call. Results expose `details.envelope`, and the card/REPL marker says
  `sandbox` or `unconfined`. The same marker covers `read`/`grep`/`find`/`ls`/
  `view_image`/`apply_patch` when the one-shot file worker runs them, with the
  in-process fallback marked `unconfined`.
- **Assignments are classified by what they change, not by whether they contain
  a slash**: `GOCACHE=…`/`TMPDIR=…` no longer push an otherwise enumerable
  command out of the envelope; `PATH`, `LD_*`/`DYLD_*`, `HOME`, and the
  interpreter/toolchain variables still contain.
- **Compound network commands can be remembered**: `/network add host|command|tool
  <key>` pre-authorizes, and a compound line with one rememberable program plus
  non-network parts (`cd … && GOCACHE=… go test … | tail`) remembers `go test`;
  a line with a second or unknown network-capable program stays per-call.
- **`/network follow`**: an opt-in policy (persisted in `network.toml`) where an
  auto-mode Allow carries its network; a remembered denial still wins, and ask
  mode is unchanged.
- **Command Code stream endurance**: the `commandcode` provider gets its own
  transport — no compression, no keep-alive reuse — so a proxy cannot lose the
  tail of a long stream through a stale pooled connection. HTTP/2 stays on; the
  endpoint serves h2, and forcing HTTP/1.x made the client parse its SETTINGS
  frame as a malformed HTTP response. A cut after
  visible text with no tool call is continued from the partial answer by
  default (up to two continuations; set `GOLDER_STREAM_RESUME=0`/`off` to keep
  every cut terminal). A tool-call turn still surfaces the cut. A cut that
  landed after only reasoning — no answer text, no tool call — is now re-issued
  instead of ending the turn with "stream cut … retry the turn": thinking is
  transient, so the retry had nothing of the user's to duplicate or lose.
- **A default provider you can set from inside the session**: `/provider default
  <name>` writes the `provider` line of `config.toml` (show, or `off` to clear),
  rewriting the pinned `model` line to the new gateway's own default when one is
  set. The provider list tags it `(default)` beside `(current)`, and the TUI
  picker sets it on `d` without leaving the list. The write edits those two
  lines, so comments and unrelated settings survive; asking for it through the
  picker or the command produces the same report.
- **`/usage` reports the remaining subscription quota**: three providers answer
  it with the same key the conversation uses — `opencode-go` (`{base}/usage`), the
  5h/weekly/monthly windows as percentages with their reset times; `commandcode`
  (`/alpha/billing/credits` and `/subscriptions`), the 5h and weekly dollars used
  against their caps plus the monthly credit balance, plan and renewal date; and
  `deepseek` (`/user/balance`), the prepaid and granted balance. Every other
  provider answers with the explanation instead of a failed request: a relay such
  as sub2api exposes its own wallet balance, which is not the upstream
  subscription's remaining quota, and no endpoint can ask the upstream on its
  behalf. The TUI runs the lookup off the tea loop (spinner, cancellable) and the
  REPL uses the shared registry action, so both surfaces print the same report.
- **Command Code (GOAT) is a first-class provider**: `commandcode` (alias
  `command-code`) uses the official Provider API at
  `https://api.commandcode.ai/provider/v1`, authenticates with
  `COMMANDCODE_API_KEY`, and carries the OpenAI Chat, OpenAI Responses, and
  Anthropic Messages wires; its default model is
  `deepseek/deepseek-v4.1-flash`, matching the CLI's default family. GOAT's API
  usage is metered against its plan credits, and the provider joins the
  ForceProxy gateways so foreign upstreams use the configured local proxy.
- **User-wide instructions**: `$GOLDER_HOME/AGENTS.md` (usually
  `~/.golder/AGENTS.md`) is now injected into every session before the project
  AGENTS.md chain. Personal working preferences no longer need a per-repository
  file or a `--append-system-prompt` wrapper; a project's AGENTS.md still comes
  later and can refine them. The read is best-effort, so an unreadable file
  never blocks a run.
- **A gateway can serve more than one wire, and opencode-go's Messages endpoint
  is now reachable**: `ProviderSpec.WireVariants` lets a registry entry declare
  the extra protocols a gateway is known to serve, each with the auth scheme that
  wire takes. An explicit `--protocol` naming one resolves to that wire's driver
  under the provider's own name — so per-provider request decoration (the
  opencode routing header) still applies — while the registered protocol stays
  the default and nothing changes for a user who does not ask.
  `opencode-go` declares `anthropic` (x-api-key, probed), so
  `--provider opencode-go --protocol anthropic --model claude-haiku-5-5` runs
  over its `/messages` endpoint; the conflict error now lists every value the
  provider accepts instead of only its default.
- **Thinking control follows the model's advertised shape**: models.dev (the same
  catalog and 24h cache the context windows come from) publishes each model's
  `reasoning_options`, and golder reads them now. A model whose control is an
  effort ladder takes the adaptive body — `thinking: {"type":"adaptive"}` plus
  `output_config.effort`, the requested level clamped into that model's own
  ladder (claude-haiku-5-5 rejects `minimal`, and the legacy token-budget block
  outright) — while a model that still advertises a budget keeps the
  byte-for-byte body golder has always sent, and a model the catalog does not
  cover is never guessed at.
- **A local rule table decides the routine shell commands without a model**:
  the reviewer is a model call — ~2.2s per gated call without the decision model
  — and a session pays it for `ls`, `grep`, `git status` and `go test` as surely
  as for `rm -rf`. A table now grades a shell command and the tier it names is
  settled by the gate:

  | table says | verdict | what happens |
  |---|---|---|
  | `READONLY` | allow | runs, no model call |
  | `LOCAL_EFFECTS` | confirm | `auto` runs it, `ask` asks; no model call |
  | `EXTERNAL_EFFECTS` | confirm | same |
  | `DANGEROUS` / `UNKNOWN` | — | handed to the reviewer, unchanged |

  The table is bash-classify's (Filip Prochazka, MIT), copied rather than
  depended on: the runtime dependency would be a Python package and a tree-sitter
  grammar, for a decision this package already has the mvdan parse for. 168
  programs, 2,301 subcommand entries (the source nests: `gh run list` is a
  subcommand of a subcommand) and the 32 option overrides that RAISE a
  classification (`git push --force` is not `git push`) — the overrides that
  lower one are deliberately not carried, which is why a rule here can only be
  more conservative than its source. `internal/judge/gen_risk_table.go`
  regenerates the Go table from a checkout of the reference; the shell's own
  builtins and two tools the reference lacks (`rg`, `gofmt`) are small
  hand-written supplements beside it, each with its own reason stated.

  Measured on a real 731-command session: 65% allowed, 15% graded confirm, 80%
  decided without a model. The 20% that still goes to one is what it should be —
  `rm`, `python3` and other opaque or destructive commands, plus programs the
  table has no rule for.

  Where it sits is the design, and it is neither an exemption nor a second
  opinion. The pipeline the approval gate consults is `static floor → the routine
  grader → session model`, and the routine grader is ONE of two things: the
  decision model when its credential resolves, the local table otherwise. They
  answer the same question ("is this call routine?") and a run wants one answer
  from one place, with the calls neither can settle going to the session model —
  so a run WITH the decision model behaves exactly as before, and a run without
  its credential gets the table instead of a ~2.2s round trip per `ls`. Chaining
  them would have routed the decision model's escalations into the table, and an
  escalation means "this deserves a model's judgment", not "try something
  cheaper first". Every call is still graded by the gate's chain, and the table
  has no off switch: it IS the fallback, and a fallback that can be disabled is a
  fallback that leaves a run paying a model round trip for `ls`. The verdict's own
  sentence follows the conversation's language the way the decision model's
  rationale does.

  Two things the table must not get wrong, and both are tested: a privilege
  escalator is never unwrapped (`sudo ls` is deferred, not read as `ls`), and a
  redirect that writes a file is a mutation whatever the program is (`ls > out`
  is graded local-effects). An expression is as severe as its worst command, so
  `curl … | sh` lands on the shell and is deferred.
- **`/boundary` and `/grants`: consent and the table each get their own
  command**: the four things a person can decide used to be spread over two
  commands that each carried two subjects. `/trust` held the per-directory
  decision (`on|off|once|status`) AND the whole authorization table
  (`list|add|rm`), while `/permissions` held the approval mode AND the path
  grants, with `readable`/`writable` hanging off it as subcommands. A person
  who wanted to know where a read might reach typed `/permissions` and got a
  risk-mode list; a person who wanted the mode typed `/trust` and got a path
  table. Now each command answers one question: `/trust` for whether
  side-effect tools may run in this directory, `/boundary` for which paths
  outside the workspace may be read or written, `/permissions` for how calls
  are graded, and `/grants` for the raw table (every row, refusals included;
  `add`/`rm` write one directly, for a path no dialog has touched yet).
  `/permissions list` deliberately no longer lists paths at all — seeing them
  there taught people to look for "permissions" when they meant "where may
  this read" — and names `/boundary list` instead. The TUI's writable-path
  picker follows (`permissions-writable` is now `boundary-writable`, and its
  Add row seeds `/boundary writable add ` into the composer).
- **The reviewer routes through a decision model, and falls back to the
  conversation model**: grading every mutating tool call with the session's own
  model costs a full generation, and its failure mode is a parsing one — the
  reviewer asks for strict JSON, then retries when the model writes prose around
  it. TypeSafe's System One model answers typed questions with calibrated
  probabilities instead: one request carries a Choice for the verdict, a Score
  for the risk, and four independent booleans (irreversible, touches
  credentials, matches what the user asked for, plus a reason label), and there
  is no prose to parse.

  The routing rule is the design: the cheap judge may DECIDE a call, never
  decide it CHEAPLY. `Allow` demands high probability AND a wide margin over the
  runner-up AND a clean bill from every danger signal; `Deny` and `Sandbox` are
  accepted at moderate confidence, because their failure modes are a redundant
  block or a redundant sandbox; `Confirm` always escalates, since in auto mode a
  Confirm runs anyway and treating a cheap judge's Confirm as final would make
  the cheapest reviewer the weakest link. The levels it emits mean exactly what
  the slow reviewer's levels mean, so an Allow still passes every downstream
  gate — consent is not a risk verdict, and a probability is not a permission.

  Measured against the live endpoint on ten commands: seven denied, two allowed,
  one escalated, no dangerous command passed, at ~0.7s per call against the
  conversation model's 2.2s median. It is on by default when a `TYPESAFE_API_KEY`
  (or a credential for the `typesafe` provider) exists and falls back silently
  when one does not, so a run without a key behaves exactly as before.
  `GOLDER_JEV=off` disables it, `GOLDER_JEV_ENDPOINT` / `GOLDER_JEV_TIMEOUT_MS`
  override the endpoint and the per-call timeout, and `/proxy typesafe on`
  routes it (TypeSafe refuses some regions with HTTP 451 — measured directly,
  and the same request through a proxy answers 200).

  Two failure behaviours are deliberate. A hard failure — the region block, a
  rejected key — switches the cheap path off for the rest of the PROCESS, which
  is why the breaker is package-level: the classifier is rebuilt per turn, so a
  per-instance flag would pay one failed call per turn forever. And a caller
  that cannot reach it escalates to the conversation model, or fails closed when
  there is none — never an approval.
- **The shell parse reaches the boundary, so a command's paths come from what it
  IS rather than from what its text looks like**: the read-scope gate read
  command lines with a hand-rolled word scanner, which sees arguments and
  nothing else. `internal/shellfacts` parses with `mvdan.cc/sh/v3` and reports
  facts only — argv vectors, redirects with their operation already decided
  (shell semantics are fixed, unlike a path passed to a program), nested
  commands inside substitutions, and which words expand at runtime. It never
  expands anything: a word holding `$VAR` is reported as dynamic WITH the
  variable's name, and a substitution's body is parsed rather than executed,
  because running it to learn its output is the exact mistake the layer exists
  to avoid. Quote removal and backslash escapes ARE applied, since both are
  lexical; mvdan's `expand.Literal` was measured and rejected for the job, as it
  answers "rm-rf/" for `rm$IFS-rf$IFS/` and "/x" for `"$HOME"/x` — a
  benign-looking lie about what will run.

  The gate now takes the UNION of the parse and the program-aware scanner,
  because each sees what the other cannot: the parser knows redirects,
  substitutions and quote removal but not that `sort --files0-from=/x` names a
  file to read, while the scanner declares exactly those flags. Keeping only the
  parser silently dropped the scanner's half and the boundary regression test
  caught it — `sort --files0-from=/outside/list` became neither asked about nor
  reviewed, which for a consent gate is the wrong direction.
- **One authorization table replaces three stores**: per-directory trust
  (`trust.json`), the sandbox roots (`permissions.toml` plus the `config.toml`
  seed) and the in-memory registries each answered part of "may golder touch
  this path", and each could only point at the others. `internal/grants` keeps
  one row per path — what it permits, what it refuses, and how long it lasts —
  under three capabilities rather than two: **execute** is separate from
  read and write because it is what directory trust has always meant, and
  folding it into write would make "let me edit these files" quietly equal to
  "let anything run here". The existing stores are migrated on first run and
  left untouched, and the migration cannot widen a grant: a trusted directory
  becomes Execute (NOT read+write), an explicit refusal is carried across as a
  refusal, an explicit null is not a decision, a writable root stays read+write,
  and a readable root stays READ ONLY. Verified end to end by running the binary
  against a synthetic old-user home and reading the file it wrote.

  Every write path records into it — the read dialog, the trust dialog, the
  write dialog, and every `trust.Manager` write — and every reader consults it:
  the read-scope gate, the trust gate, the sandbox's read whitelist, and the
  authorization snapshot a sub-agent inherits. Writes are serialized against
  other golder processes with a lock file, and the read-modify-write merge keeps
  a concurrent process's grant instead of overwriting it.
- **golder now shows up as an agent in the multiplexer it runs in — tuios and
  herdr — with neither of them changing**: the terminals people already run
  coding agents in track agent state (a rail, an Inbox, phone alerts, resuming a
  conversation after the daemon restarts), and golder was invisible to both: no
  harness manifest for tuios, no report of any kind for herdr. Each host already
  accepts a report from the agent itself, so this is one new leaf package
  (`internal/hostcompat`) speaking two dialects, plus a hook at the moments the
  drivers already knew about.

  The two hosts differ in more than a command line, which is why the package
  keeps one neutral vocabulary and translates. tuios wants a **file** as well as
  a report — recognition is a harness manifest, a report only moves the state of
  a pane it can already name — so `golder host-compat install` writes
  `~/.config/tuios/harnesses/golder.toml` (idempotent, atomic, refuses to
  overwrite a file golder did not write, writes through a symlink so a
  dotfile-managed config keeps control of it, and `uninstall` removes only
  golder's). herdr wants nothing installed at all: the report names the agent,
  and the resume command rides the report. herdr also requires a `--seq` above
  the last one it accepted, so a frozen or backwards clock must still produce an
  advancing number, or the rail would go silently stale. And herdr's vocabulary
  is four states where tuios has seven: `done` and `errored` fold onto `idle`
  with the word kept in the message, rather than being invented or dropped.

  **The Inbox can answer for you in tuios — and that is the part with a
  standing cost.** Pressing the key is tuios's job, and it can only do it for a
  prompt it can read: the manifest's screen rules describe all four prompts
  golder can put to a person — the tool approval, a read outside the workspace,
  the launch-time trust picker and the mid-run trust question — and the answers
  bind keys to those rows' labels. tuios has five answer shapes where golder's
  dialogs carry more rows, so the mapping is a choice rather than a
  transcription, and it takes the conservative half: the rows that write a
  permission to disk (the remembered read and write grants) are left to the
  pane, because a key pressed from another window is not consent to something
  persistent, and an answer bound to a row a dialog does not show (the narrower
  reads rung, on a command) would press nothing at all. A reworded dialog
  therefore costs the ability to answer from the Inbox silently rather than
  loudly, which is why a test renders each dialog and fails when a string the
  manifest matches on is gone (`internal/cli/tui/manifest_sync_test.go`, checked
  to fail by breaking it), the manifest carries a version marker, and
  `golder host-compat status` reports an outdated one. herdr has no such
  coupling: it only reads reports.

  One seam was added to golder's gates, because every "a person is being asked"
  moment already passes through a single place: `OnPrompt` on
  `judge.GateOpts`, `judge.ReadScopeOpts` and `trust.GateOpts`, carried through
  `run.GatesOpts` and `cli.HostGates` so a `/goal` or `/btw` side run answers
  the same as the main conversation. Parallel tool calls can each open a
  prompt, so the pane's blocked state is a count rather than a flag, and a turn
  that ends while a prompt is still open stays `needs_input` — reporting `done`
  over an unanswered question is how a person misses the one thing that needed
  them. Reporting never blocks the agent: it coalesces onto a single slot,
  sends on its own goroutine with a 1.5s deadline, and logs rather than returns
  a failure. Outside a host pane the whole thing is nil and every call is a
  no-op, `GOLDER_HOST_REPORT=off` disables it, and `=tuios`/`=herdr` forces a
  host by hand.

  Verified against the real thing rather than only against the contract: the
  manifest loads in the released tuios (`Manifest golder is loaded from
  ~/.config/tuios/harnesses/golder.toml`), and an opt-in live test run inside a
  real pane (`GOLDER_HOSTCOMPAT_LIVE=1 go test ./internal/hostcompat/ -run
  TestLive`) has the daemon report the pane back with golder's state and session
  id. The unit tests pin the exact argv each host is sent, herdr's sequence
  never moving backwards, the concurrent-prompt accounting, and the install
  cycle including a dangling symlink.

- **Every tool call is reviewed, and both grading exemptions are gone**:
  `apply_patch` skipped the reviewer when every path it named was inside the
  workspace (584 of 586 real patches), and a provably read-only shell command
  skipped it always — the commands a reading session lives on. Both are removed,
  together with the tool table's "no side effects, no review" dimension, so the
  chain is now the static floor and then the reviewer with nothing in between.
  The decision model is what makes that affordable: a graded call costs ~0.78s
  against the previous reviewer's 2.16s median, and over the 32 commands a
  session actually runs every verdict is Allow — the exemption was buying
  latency, not a different answer.

  Removing the exemptions is what let the rest of the design shrink. The
  read-only recognizer no longer decides anything, so it lost the three things
  that existed only to make an exemption defensible: the kill switch, the
  credential-floor re-run inside itself, and the escalation carve-out. It now
  answers one containment question — run a recognized command inside the sandbox
  rather than bare — which is a fact about the command, never a verdict about the
  call. The tool table lost its "ungraded" dimension for the same reason, so one
  rule covers every tool, and the two reminder tools that had never been
  classified (`schedule_create`, `schedule_delete`) joined it so read-only mode
  treats that family alike.

  The knowledge behind the patch exemption's two exceptions did not go with it.
  `.git/**` (a hook runs on the next git command, and `.git/config` is the same
  door via `core.hooksPath`/`core.sshCommand`) and `AGENTS.md` (read into the
  system prompt of every later session, so a patch to it rewrites the
  instructions the agent starts from) are now reported to the reviewer as
  `State.Outliving` instead of gating the call. A person asking for a pre-commit
  hook is asking for something legitimate, and the old code agreed: those paths
  kept their review, they were never denied.

- **A write outside the workspace is a question now, not a wall**: `apply_patch`
  refused an out-of-workspace path outright — no prompt, no grant, no answer that
  could have changed the outcome — while the same path as a read asked the user
  and offered three answers. The write side now takes the same shape. The tool
  table records which capability a tool's paths are asked about; the boundary
  gate asks the same question (this once, this directory for the session,
  remembered) and records the answer in the same two places (a one-call grant on
  the call's context, and a standing grant through the callback the escalation
  dialog already uses, so one answer means the same thing to a patch and to a
  command). The dialog's write rows say "write" rather than "read", because a
  directory allowed for reading is not thereby writable, and the capabilities
  stay apart in the table and in the answer. Live secret material is refused on
  both sides, whatever the grant says.

- **The reviewer is told what a call touches — in words — and what the session
  already decided**: the decision model's state carried a bash command's parsed
  facts and, for every other tool, raw arguments, plus an access list whose
  operation was a bitmask (`"Op":2`, which is not a fact a reader can use). It
  now carries the paths a call names rendered as words (`"op":"write"`, and the
  form round-trips), what the session knows about them (inside the workspace or
  outside, whether a consent question is in front of this call, and the
  authorization table's per-capability answer), and the one thing a patch's
  contents cannot reveal: whether the edit's effect outlives the call. All of it
  comes from the gates' own predicates rather than a second reading of the
  arguments, so the reviewer is never told something the gate does not believe.

- **The base instruction names the default way to inspect code — and the
  boundary of that default**: a new guide sits between the code-search and
  batching ones and says to reach for `read`/`grep`/`find`/`ls`/`view_image`
  first, because they run in-process, never reach the risk reviewer, return
  bounded output that names its own truncation, and render as one cell; bash is
  for a read the tools cannot express — a multiline or aggregated search,
  anything git (`git diff`, `git log`, `git status`), or a command's own output
  such as a test or build log. The second half is what keeps the first half
  honest: on its own, "prefer the tools" reads as "never touch a shell" and
  pushes the model off `git diff`, which has no native tool at all — the same
  shape of mistake the code-search guide made when it still pointed at `--type`
  after the tool had grown that argument. The asymmetry being taught is real
  rather than a preference: a read-only shell command skips the reviewer (the
  fast path) but is still contained in the sandbox, so it pays a process spawn
  and a policy — the native tools pay neither and fold into the exploration cell.

  The same guide carries the batching rule for reads: several files or searches
  belong in ONE message, because consecutive native reads coalesce into a single
  `• Explored` cell and the model's own text seals that cell
  (`transcript.announceToolCard`). Measured over ten real sessions, that is why
  nearly half the cells wrapped a lone call — the reads were fine, the narration
  between them split them. It is phrased as "narrate before or after a batch, not
  inside it" rather than "do not narrate": a progress line is worth keeping, and
  the person still needs to follow along.

- **A provably read-only command is contained rather than run bare**: the
  recognizer in `internal/judge/readonly.go` used to be a grading exemption
  (a recognized command skipped the reviewer). That exemption is gone — every
  call is graded — and what the recognizer answers now is a containment
  question: `readOnlyCommand` decides whether the execution layer runs a
  command inside the sandbox instead of directly.

  The safety argument is unchanged and still worth stating, because it is what
  the recognition MEANS: the whitelist, not the parser. A command is recognized
  only when the scanner can prove what will run — no redirection but
  `/dev/null` and fd duplication, no substitution, no assignment, no wrapper
  (`env`/`nohup`/`time`), no variable, no glob in command position, no program
  named by path — and every command in the sequence is a whitelisted program
  whose arguments cannot write or execute. The whitelist is closed and each
  entry's escape hatches are checked by name: `rg --pre`, `find -exec/-delete/
  -fprint*`, `fd -x`, `sed` beyond `-n Np`, `sort -o/--compress-program/-T`,
  `file -C`, `diff --paginate`, `printf -v` and `%n`, `uniq`'s output operand,
  BSD `date MMDDhhmm`, `hostname NAME`, and git's `-c`/`--config-env`/
  `--exec-path`/`--paginate`/`--output`/`--textconv`/`--ext-diff`. Anything not
  provable runs directly as before, and is reviewed like every other call.

  **The containment floor** is why the recognition is not an optimization: a
  recognized command still runs INSIDE the sandbox when a runner is available
  (measured at ~4ms over a bare run). "This only reads" is a statement about
  what the scanner could see, not a promise that the consent gate saw every path
  it touches — that promise rests on the recognizer's tables staying in step,
  and when they fell out of step the read was neither asked about nor reviewed.
  The sandbox's read whitelist (the workspace plus the roots the user granted)
  is the floor under that promise, so a path the scanner missed is refused by
  the kernel instead of being read quietly. One escape hatch is pinned by a
  test: a one-call read grant runs directly, because a single approved path has
  no sandbox-whitelist form and containing the call would fail the read the user
  just approved.

  **The operand-coverage invariant** (`B`): every whitelisted program must
  declare its operands — either as paths the read-scope scanner collects, or as
  deliberately not paths with a reason — and its file-reading flags
  (`grep -f`, `date -r`, `sort --files0-from`, `diff --from-file`, `file -m`, …)
  must be extractable. Writing that test found two live gaps on the spot
  (`sw_vers` undeclared, and `date`'s flags declared but unreachable), and it
  turns the whole class of mistake into a red build: the two tables live in
  different files and nothing else connected them, which is exactly how
  `cksum /outside/x` came to be whitelisted while its operand was invisible.
  Alongside it, a cross-layer regression drives the real gate chain and asserts
  the property directly: a shell command that reads outside the workspace is
  either asked about or reviewed, never both silent — the shapes that actually
  fell through that gap (`cksum`, `cd <dir> && ls`, the flag-named second file,
  `git diff --no-index`) are its cases.

- **`bash` cards no longer fold into `• Explored`**: the exploration cell keeps
  the native read tools only (`read`/`ls`/`grep`/`find`/`view_image`), and a
  shell call always shows its own card with the full command. Folding a shell
  call required a one-line summary, and a summary has to pick ONE segment of a
  compound command to name as the headline; measured over ten real sessions,
  20% of foldable shell calls led with a banner echo, so the cell read
  `echo "=== cacheKey ==="` (with the `rg` that did the work invisible) while
  the noise was promoted and the signal hidden. Printing the command cannot be
  wrong about what ran. The native tools keep folding because their arguments
  ARE the whole call — `Read a.go, b.go` summarizes them faithfully — and they
  are also the tools the fast path does not touch (they were already exempt from
  review). This removes `judge.ReadOnlyShellSummary` and the whole summary
  formatter (`readOnlySummary`/`summarizePlan`/`summarizeArgv`) with it: they
  existed only to serve that rendering, never the gate.

  Two related observations from the same measurement, recorded so the next
  attempt starts from the data: foldable calls form runs of 1 (48%), 2 (33%) and
  3+ (19%) — so nearly half of the cells were a lone call wrapped in an
  `Explored` header that bought nothing — and the model's own text between tool
  calls seals a run, which is what breaks most of them into singletons.

- **Every built-in command's usage is one field, and a test holds it there**: a
  command's grammar lived in two places — an `ArgumentHint` (which `/help` and the
  REPL's completion render, and which the popup now renders too) and, for the
  commands that never declared one, as a trailing `: /name [args]` inside the
  description. Fifteen of the twenty-one built-ins were in the second shape, so
  the popup could only show usage for the six that had a hint, and the rest were
  sentence-plus-usage run-ons everywhere they appeared. All of them now declare
  their grammar as a hint and keep a sentence for a description, including
  `/proxy`, whose usage was lost outright when its description was trimmed.
  `usage_convention_test.go` is what keeps it: no built-in description may name
  its own command or spell a `--flag`, every hint must be bracketed usage rather
  than prose, the set of commands that take arguments is pinned so a new argument
  cannot ship without a hint, and `/help` must print each of the two fields
  exactly once.
- **The usage line a command answers a bad argument with is the registry's own**:
  every front-end printed its own copy of the grammar — the TUI, the REPL, `/goal`
  and `/btw` through the session host, the resume resolver, and the commands whose
  actions compose one — so a command's hint and the line a user was taught after a
  typo were two independent strings, and they had already drifted (`/permissions`
  advertised `list` while its error still taught the old list). They all render
  from the command's own record now: `SlashRegistry.UsageLine(name)` builds
  `usage: /name <hint>` from the hint the popup and `/help` advertise, and the
  front-ends call it on the registry they already hold (`m.slash`, `deps.slash`,
  `host.Slash()`). `cli` cannot reach a registry — `cli/prompts` imports `cli`, so
  the resume resolver returns a sentinel instead of a grammar, and nothing in this
  package spells usage any more. Each family's grammar text lives in one place:
  the command hint for the whole command, and — where a subcommand is narrower than
  the whole (a verb that takes no path, a provider without its address form) — the
  two spellings declared side by side. The tests drive each path with a registry
  whose hint is a sentinel, so a reinstated literal cannot pass.
- **`/trust list` and `/permissions list` show every grant, not just the ones in
  force here**: the two halves of the permission state — per-directory trust
  (`trust.json`) and the sandbox writable/readable roots (`permissions.toml` plus
  the `config.toml` seed) — could only be read one scope at a time, and `/trust
  status` answered the narrower question ("what applies to THIS directory") by
  walking up from the working directory, which is exactly what cannot list what a
  decision recorded for a *parent* applies to. `/trust list` prints every saved
  decision sorted by path with this directory marked, then the session-only
  grants that are not on disk at all (trust, and reads) and the store it read.
  `/permissions list` prints the live approval mode, both grant kinds with their
  provenance, the files they come from, and how to change them. Each names the
  other, since neither covers the whole picture alone.
- **The slash-command popup shows the whole entry for the command you are on**:
  every row was one line with an ellipsis, so a candidate whose text did not fit
  was unreadable exactly when you had arrowed onto it and wanted to read it. The
  highlighted row now wraps — continuation lines hanging under the description
  column, so the `/name` column stays clear and the block still reads as one row
  — while the rows you are only scanning past keep the single ellipsized line,
  which is what stops eight long entries from turning the popup into a page. The
  wrap breaks at spaces rather than mid-word (`read-only` could come out as `re` +
  `ad-only` under the transcript's rune wrapper), and a token with no spaces in it
  — a usage string, a path, a run of CJK — breaks at its own punctuation (`[a|b|`
  + `c]`) before falling back to a rune boundary.
- **The popup renders a command's usage hint**: the description and the arguments
  are two fields (`ArgumentHint` is what `/help` and the REPL's completion already
  render), but the popup showed only the description — so five built-ins
  (`/model`, `/provider`, `/permissions`, `/proxy`, `/trust`) spelled their usage
  inside the description to get it displayed at all (`/trust`, which had no hint
  field, gets one here: its usage was invisible in the popup without it), and
  `/permissions` carried a
  hand-written `…` abbreviation that went stale the moment rows could wrap: it
  read as a rendering bug rather than as the writer's shorthand. The hint is now
  rendered under the description on the highlighted row, in the style the
  transcript uses for command text, so usage reads as a signature instead of more
  prose; the five descriptions are sentences again, and each usage string has one
  home. How many lines the block may take is tiered by where the command came
  from, because the two populations are written for different readers: a built-in
  is written to be read here (four lines, which holds the longest one down to a
  ~68-column terminal), while a skill, template or plugin description is
  frontmatter written for the system prompt — a paragraph is normal there — so the
  menu previews it in two lines and leaves the text the agent receives untouched.
  The tier keys off the same source test that groups commands, so the `Extensions`
  block and the two-line cap cannot drift apart. The popup also counts its
  reserved height from the same renderer that draws it (`rows(width)`), so a
  wrapped row can no longer make the overlay and the transcript disagree about how
  tall the popup is.
- **The REPL's risk prompt offers the write grant too**: the TUI dialog has
  offered "allow writes to `<path>`" for a while, but the line-based REPL asked
  a plain `[y/N]` for the same escalation — so the same conflict had a narrower
  answer depending on which front-end you happened to be in. The prompt now
  carries the same ladder (`y` runs it unisolated, `w` re-admits just the denied
  path for the session, `s` remembers it) whenever a driver can actually grant
  one, and falls back to `[y/N]` when it cannot — an option that silently
  forgets would be worse than an absent one. The path extraction it needs moved
  to judge (`EscalationPathFromText`), so the two dialogs cannot disagree about
  what the grant covers, and a `w`/`s` answer keeps the call contained (the
  sandbox bit) rather than letting it out.
- **Read and write grants can be remembered from the dialog**: both approval
  dialogs that offer a path grant (a read outside the workspace, a sandbox write
  the reviewer contained) now offer it at two lifetimes — *this session* and
  *remembered* (`s`) — and the remembered answer writes the managed store
  (`~/.config/golder/permissions.toml`) as part of answering, instead of sending
  the user to a command. The labels never abbreviate which is which: a grant
  that outlives the process must not be one keystroke away from being granted by
  accident, which is also why the session answer is kept beside it. A write that
  fails leaves the grant session-only and says so rather than reporting a
  success that will not survive a restart.
- **`/permissions readable list|add|rm`**: the read side of a command that only
  the write side had. The read grant used to live in memory alone — the approval
  dialog's answer died with the process and the only durable form was hand-
  editing `config.toml` — even though the managed store's schema already carried
  `readable_roots` and startup already read it back. Both grants now live in the
  same file, are maintained by the same shape of command, and are listed with
  the same provenance labels (`config.toml` seed / `permissions.toml` persisted /
  this session). `config.toml` keeps working as the hand-edited seed: the
  program never rewrites it (that would drop the user's comments), so a path
  seeded there is reported as already remembered rather than copied.
- **A command's file operands go through the workspace boundary too**: the
  read-scope gate asked about a `path` argument, and `bash` has none — the
  paths are words in the command — so `cat ~/.config/foo` read whatever it
  liked with nobody asked. The gate now reads the file operands out of the
  command line (`internal/judge/bashpaths.go`) and asks about the first one
  outside the workspace, exactly as it does for `read`. The reading is
  best-effort and says so: only commands that actually open files are scanned
  (a path-shaped string handed to `echo`, `git commit -m` or `jq` is not a
  file being read), `$HOME`/`${HOME}` are substituted because their value is
  knowable while `$UNKNOWN` is left alone rather than questioned on every build
  script, and an interpreter's inline program is scanned because
  `python3 -c 'open("/x")'` reads `/x`. What it cannot see — an unknown
  variable's value, a script's own reads, a path assembled at runtime — stays
  the sandbox's business, which is the enforcement that does not depend on
  reading the command. The always-readable whitelist the sandbox already used
  now lives in `permissions.SystemReadRoots` so the gate and the sandbox share
  one definition: a path the sandbox would serve is not a consent question.
  Commands whose reads the gate cannot see still fail closed where there is
  nobody to ask (headless, a sub-agent), the same contract the file tools have.
- **Ctrl+O reads a collapsed paste without sending it**: the composer's
  `[Pasted text #N …]` token used to be a dead end — the body was readable only
  by submitting, and the token said nothing about what it held. `Ctrl+O` now
  opens the body **inside the composer's own frame**, under the draft: the rows
  appear between the draft and the bottom rule, `PgUp`/`PgDn` page through
  them, `Ctrl+O` or `Esc` closes. The draft, the caret and the submit path are
  untouched — the token stays exactly what gets sent, and the body is rendered
  from the stored paste rather than swapped into the buffer (bubbles' textarea
  has no "move the caret to this offset" call, so a buffer swap would move the
  user's caret every time they peeked). The window is bounded on purpose: at
  most a third of the terminal and never more than 8 rows, so reading a paste
  never eats the conversation. When several tokens are in the draft the caret
  decides: `Ctrl+O` opens the one the caret sits in, and falls back to the
  newest token when the caret is elsewhere. While a token sits in the draft the
  composer carries a dim row naming the key (`ctrl+o previews it here`, or
  which paste opens when the draft holds several), because a feature nobody can
  discover is a feature nobody has: the open preview's footer already carries
  its keys, so the hint steps aside for it, and it stays out of the mid-run
  composer where the key does nothing.
- **The paste token now says what it holds**: `[Pasted text #1 · 42 lines ·
  2.5 KB]` instead of `[Pasted text #1 +42 lines]`. The line count is what a
  person would count (a selection's terminating newline does not add a line),
  and the size is there because the line count says nothing about a single-line
  payload. A one-line paste past 1000 characters now collapses like a
  multi-line one — a base64 blob, a minified bundle or a one-line log used to
  sail straight into the composer — and a one-line selection that merely
  carries its trailing newline now stays inline instead of collapsing into a
  token with nothing to say.
- **One assembly point for a session's tool gates**: the trust gate, the
  workspace-boundary gate and the permission gate are now composed once
  (`run.Gates`) and installed on every run of the session — the main run, a
  `/goal` or `/btw` side run, and any sub-agent they dispatch. The chain used to
  be spelled out at seven sites (tui / repl / goal / btw / headless /
  subagent-rpc / the task factory), and every place it drifted was a bug of the
  same shape: the TUI consulted the side-effect tool map directly, so the read
  half of `StrictReads` was live in the REPL and dead there; the goroutine
  sub-agent ran without the trust gate and without the tool-use hooks; the
  process sub-agent ran with the permission gate alone. Each driver now supplies
  only what makes it that driver — its answer channels (`Confirm`, or the shared
  stdin prompt) and its output writer. Two chains come out of one set of inputs:
  the interactive one carries those channels, and the derived one a sub-agent
  inherits carries the same gates with **no** channels, so a child's
  unanswerable question fails closed with guidance instead of blocking on a
  dialog opened under a running batch. `InstallDriver` wires the gates, the
  PreToolUse/PostToolUse seams, the Stop decorator, SessionStart and the hook
  notifier in one call, so a driver cannot wire one half while forgetting the
  other. The two older entry points (`InstallDriverHooks`, `InstallHooks`) are
  gone rather than left beside the new one — a second entrance is how the
  original drift happened.
- **Sub-agents inherit their parent's gates, including across a process
  boundary**: the tool executor publishes the session's chain onto every tool
  call's context, and the sub-agent tool hands it to the child — so a `task`
  child runs under the session's trust, workspace-boundary and permission gates
  and under the session's PreToolUse/PostToolUse hooks, with no wiring at the
  dispatch site, and a nested child inherits again the same way. The session's
  own `Stop` hook is deliberately not inherited (a child's end is its own), while
  the `SubagentStop` family is, with its own consecutive-block budget — that
  wiring had been written in #422 but never called, so the event never fired for
  a child until now. A process-isolated child cannot share the parent's
  in-memory gates, so the RPC request carries an authorization snapshot (`auth`:
  the session-trust decision, the session-reads rung, and the read roots the
  user granted) and the child rebuilds equivalent gates around it; a snapshot
  entry the read-root policy refuses is reported instead of silently widening
  the boundary, and a parent that ran with no gates sends nothing, leaving the
  child to fail closed on its own.
- **A trust question offers a reads rung**: in an untrusted directory the read
  tools (`read`, `view_image`, `grep`, `find`, `ls`) now have a middle answer
  between "once" and "the whole folder" — *allow reads in this folder (this
  session)*, granted by `trust.Manager.SetSessionReads` and revoked with the
  rest of the session state by `/trust off`. The ladder is spelled out where
  the choice is made rather than left to labels that mean different things in
  different dialogs: the TUI dialog names each row's scope ("Allow this read
  only" / "Allow reads in this folder (this session)" / "Allow all tools in
  this folder (this session)"), and the REPL asks `[r]eads` beside `[a]ll`, so
  stopping a 20-file exploration from asking 20 times no longer requires
  granting bash as well. The grant covers local reads only — the folder key
  says nothing about egress, so `webfetch`/`websearch` keep asking — and
  out-of-workspace reads are untouched: they still take the read-scope consent
  gate, which no trust answer waives.
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
  that hands them over. Pre-granting skips the dialog:
  `/permissions readable add <path>` writes it to the managed store, and
  `GOLDER_READABLE_ROOTS` does the same for a machine that cannot edit a file
  (unlike `GOLDER_SANDBOX_READABLE`, which only widens the sandbox). The write path is
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
  permitted in read-only mode, and reviewed like every other call.
- **view_image**: `view_image` reads a local image (PNG/JPEG/GIF/WebP, up to
  8 MiB, bounded to the workspace or the skills root like `read`) and attaches
  it to the conversation for visual inspection. Tool-result images now have a
  wire path on every provider that supports them: Responses sends
  `function_call_output` content items (`input_text` + `input_image` data
  URIs, the same shape codex uses), Anthropic sends content blocks on
  `tool_result`, and Chat Completions — which has no image slot on a tool
  message — degrades to a text placeholder instead of dropping the block.
  Read-only class in trust (gated only under `strict_reads`) and permitted in
  read-only mode.
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
  arguments exist to replace. The tool descriptions now name that routing
  outright — "use this rather than searching through bash", with the two
  shapes that stay the shell's (multiline patterns, aggregating matches) — and
  `find` says the same about `find` in a shell. The measured cost of getting
  this wrong is visible in the transcript: a search issued through bash renders
  as its own card and is excluded from the `• Explored` cell, so a burst of
  shell greps both loses the parallel batch and splits the exploration group
  into single-row cells.
- **`/resume --all` and a directory column**: every session row now carries the
  directory the session ran in — the REPL list, the TUI picker's second line,
  and `golder -l` / `golder session list` — so two rows with the same preview
  can be told apart without opening them (`~` shortens `$HOME`; a session
  written before the `cwd` field existed shows `unknown`). `/resume --all` (or
  `-a`) lifts the project filter and lists every project's recent sessions, and
  it is the escape hatch the empty-list and out-of-range hints name.

### Changed
- **Allow verdicts are white**: `NoteApproved` renders in the approved style in
  both TUI and REPL, for every mode. Containment stays warning-colored and
  denials stay error-colored; the `⚠`/`✗` markers and wording are unchanged.
- **Authorization is one capability decision per call**: boundary, network
  and sandbox escape are collected as a `Needs` list and settled once. `ask`
  shows one dialog for the whole list; `auto` lets the reviewer (local rules,
  JEV, then the LLM only when needed) settle risk and expansion together, so
  an `allow`/`confirm` grants every requested capability without a second
  prompt; `full-access` has no prompts and keeps only the static hard-deny
  floor. `read-only` allows in-workspace reads and refuses any expansion.
- **Sandbox-denial recovery is reviewer-only under `auto`**: after the sandbox
  names a refused path, the same `ReviewRecovery` policy asks the reviewer
  whether to grant that one path for a contained retry. An allow retries, a
  deny/contain/failure leaves the denial and returns a labelled failure to the
  model — no human dialog. `ask` still shows one capability question; review
  failure there falls through to the person.
- **Mouse reporting is back to cell motion (xterm 1002)**: click, release,
  wheel and motion while a button is held are enough for the TUI, and the
  1003 all-motion mode that fed the status bar's hover expansion sent an event
  for every cell the pointer crossed — costly over SSH. A click now pins a
  status segment open (a second click releases it); hover expansion is gone.
- **The family table is a rule, the probe is a question — and the two are no
  longer mixed up**: the families a vendor publishes on a wire (GPT, Codex, the
  o-series, Grok, Muse, DeepSeek on Responses; Claude on Messages) come from the
  shared table (`ModelWire`), and a gateway declares only where it *diverges*
  (`ModelWireRules`, e.g. opencode-go's MiMo on Chat) — nothing is copied
  per provider. Where either answers and the endpoint serves that wire, the lazy
  probe is not asked at all; it runs only where the question is genuinely open (a
  gateway that does not serve the model's family wire, so the static choice is a
  translation — OpenRouter reaching Claude over Chat). The wire cache now records
  what each answer is worth: one a real request produced (the adaptive fallback
  watched that wire carry the model's own turn) may stand in for the table, a
  probe's 1-token greeting may not — and entries written before the distinction
  existed read as the weaker kind, so an old mistyped answer re-learns itself
  instead of ruling for a month.
- **The per-turn memory recall ships off, with guards and a throttle**: the
  automatic `memory_search` channel (last user message → BM25 → injected as a
  `<system-reminder>`) used to run on every turn. It is now off by default
  (`[memory] recall`), and when enabled it skips one-unit queries ("继续",
  "ok"), requires a hit to share two distinct query units, and reconciles the
  index at most every 30 seconds — that reconcile is linear in the number of
  memory files and used to run on every turn. Measured warm cost: 3ms at 50
  files, 19ms at 500, 78ms at 2000. `/memory recall [on|off]` toggles it
  (persisted to `config.toml`, live in the current session); `/memory` reports
  the policy and the last decision, and the `memory_search` tool is unaffected
  either way.
- **The `sandbox` reviewer tier is now `contain`**: the tier names what the
  execution layer must do, not the mechanism. Custom reviewer prompts must
  return `contain` instead of `sandbox`; `allow`, `confirm`, and `deny` are
  unchanged.
- **`--provider` without `--model` starts on that provider's default model**:
  the global `-m` default belongs to the default gateway. Selecting another
  provider now uses its `DefaultModel` (the same rule `/provider` uses), which
  is what makes vendor-prefixed Command Code ids work without repeating `-m`.
- **The system prompt now spells out approval etiquette**: batch calls that
  need the same capability, treat a refused escape/path/network request as
  final instead of retrying it, never answer a PTY allocation failure with
  `require_escalated`, invoke installed programs by name rather than by an
  absolute `$HOME` path, and ask once for a boundary grant or state network
  intent up front. This is the behavior that turned one capability question
  into several dialogs.
- **OpenAI's default model is `gpt-6.1-sol`**: GPT-6.1 Sol — the Sol line's
  current release, near-Astra capability at a fraction of the price — takes the
  slot `gpt-6-astra` held. Astra remains the Pro-tier option and both
  `gpt-6.1-sol` and the previous `gpt-6-sol` stay selectable by id. A bare
  `/provider openai` (and `/model openai`) now resolves to GPT-6.1 Sol.
- **Every built-in provider now declares a default model**: the gaps —
  `opencode-zen`, `kimi-coding`, `azure-openai-responses` and the `typesafe`
  decision endpoint — are filled with the highest-version model each gateway
  serves (`gpt-6.1-sol` for OpenCode Zen and Azure's OpenAI family, `k3` for
  Kimi Code, `jev-latest` for TypeSafe). `/provider <name>` therefore always
  moves the model with the provider, never just the provider block.
- **A blocked path is offered back at the moment it blocks**: until now the
  sandbox's own refusals only labelled the failure for the model — the person got
  nothing to answer, and the way forward was a `/boundary writable add` somebody
  had to be told about. A bash command the sandbox denied is now offered the path
  it named, through the *same* question the refused-escape dialog asks (reading
  for the session or remembered, reads and writes for the session, or deny) and
  the *same* registration every layer reads; a yes re-runs the command with that
  path admitted — still contained, never bare. Asked once per path per run, and
  skipped for a command that names no path, for a driver with no dialog, and for
  an escalation request (the escape flow already had its say about that path).
  Read and write capabilities stay independent: `a`/`s` grant reading, `w` grants
  both, and a read grant never makes a write succeed.
  The question now has one implementation for its two triggers — one dialog kind,
  one path extractor (`GrantablePathFromText`), one answer-to-grant mapping
  (`ApplyReAdmittedPath`) and one once-per-path memo (`OnceSet`) — wired where
  every driver installs its gates, so the tool and the gate cannot drift apart in
  what a "yes" means.
  The question also says which side was denied and leads with the row that
  unblocks it: a path that is not there can only have been needed for writing, so
  the write row comes first and the title reads "denied writing"; anything
  unrecognizable stays "read", the narrower grant, and the read rows stay one
  keystroke away either way.
- **A refused escape now offers the smaller answer where it is refused**: under
  auto, an escape request the reviewer declined used to hand back a message and
  nothing else. When the request names a path, the gate now asks the person
  directly — a dialog that re-admits *that path* inside the sandbox instead of
  dropping the sandbox for the whole call: reading for the session (`a`) or
  remembered (`s`), or reads and writes for the session (`w`), because a
  toolchain cache needs both and a write grant carries read. The answer is
  registered in the same table the gates and the sandbox read, so the call runs
  contained and every later call sees the grant too. The question is asked once
  per path per run, so a retry cannot turn a refusal into a nag; a refusal keeps
  the message that names the smaller fix. The offer covers both shapes of
  refusal: a call the reviewer graded containable (which would otherwise run
  contained and fail on the very path) and one it left uncontainable (which would
  otherwise just stop). A "no" records no denial, because nothing was denied —
  the call is contained when containment is possible, refused otherwise.
- **A refused escape now names the smaller fix**: under auto, an escape request
  the reviewer did not authorize was refused with "make the command work inside
  the sandbox, or switch to ask" — which never said what usually unblocks it:
  *one path*. The refusal (and the headless one) now adds the least-privilege
  route, naming the path the request itself carried when there is one: re-admit
  a blocked read with `/boundary readable add <path>` (or
  `GOLDER_SANDBOX_READABLE`), or grant a blocked directory through the ask
  dialog's `w`/`s` rows and stay contained. The model-facing sandbox-denial hint
  under auto carries the same two fixes, so a `go test` blocked by an unreadable
  module cache learns to ask for a read grant instead of retrying the escape
  that was just declined.
- **One vocabulary for approvals, everywhere it is spoken**: the words that
  describe who answers a call now match what actually answers it. The system
  prompt, the bash tool's description and its `sandbox_permissions` schema, and
  both sandbox-denial hints told the model the *user* decides an escape — true
  under ask, wrong under auto, where review does — and the REPL's stdin prompt
  was the one surface still printing its own sentence instead of the decision
  line. They all say the same thing now: the approval mode decides who answers;
  under ask an escape takes a deliberate second key next to containment-first
  defaults; under auto review decides, and retrying cannot change that answer,
  so the denial hint names the one step that can (switching `/permissions` to
  ask). The mode descriptions in `/permissions` say what each mode will and will
  not ask, the boundary dialog says "write" when the question is about writing,
  and the READMEs and `docs/sandboxing.md` describe the mode-split the code
  implements.
- **Approval says who decides, and auto means auto**: every approval surface now
  carries one decision line — `tool · mode · grade → who settles it`, e.g.
  `bash · ask · confirm → your call` or
  `bash · auto · sandbox → contained · runs in the sandbox` — built by a single
  renderer (`judge.DecisionLine`), so the dialog, the transcript notes and the
  REPL prompt use the same words as `/permissions`. The mode and the grade are
  the canonical tokens rather than paraphrases, because they answer the question
  people actually have ("does a human need to answer this?"): the same `confirm`
  is the user's call under ask and already decided under auto, and that used to
  be invisible. Under auto an escape request no longer interrupts the person at
  all — the reviewer decides, an explicit Direct grant runs the call bare and
  announces it with a new `NoteEscaped` kind, anything less re-contains it and
  says so — and a run outside the sandbox is never silent. Under ask the
  escalation dialog now leads with "Run it in the sandbox (recommended)" and
  makes the escape a deliberate second key (`e`), so approving a command no
  longer means approving its escape by default. The note line stops hiding
  "approved" and "approved but contained" behind one sentence, and the status bar
  carries the live mode (`⚖ auto`, in the alert block for `full-access`), so "am
  I being asked at all?" is answered before the next dialog rather than by it.
- **A network tool is gated like a network tool**: `NetworkRequested` recognized
  only bash's `network: "on"`, so `webfetch`/`websearch` — which run in this
  process, where no OS sandbox can contain them — reached the internet with no
  consent step, on the strength of their descriptions. They take the network
  path now: the reviewer's capability or the person's answer decides, the
  "networked call needs a runner" refusal stays scoped to exec tools (a fetch has
  no filesystem boundary to lose), and nothing about a call is announced while a
  question about it is still open.
- **The status bar adapts to its width, and moves only when something changed**:
  the bar is one fixed-width row, and it used to drop whole fields when they did
  not fit — the cwd first, being the lowest priority. Replacing the app badge with
  the provider name (`opencode-go` where `golder` was) made the row five columns
  wider, which is how a path that had always been visible started disappearing on
  an 80-column terminal. It now solves the row in three steps, in order of how
  little they cost the reader: the whole value when it fits; the abbreviation when
  it does not (`▸ …/golder`, a still bar — what a normal-sized window gets); and
  then a tail-keeping cut, so the model stays recognisable (`✱ …haiku-5-5`) and the
  path keeps whole trailing directories (`▸ …/harness/golder`) rather than a
  directory sliced in half. Nothing on the bar moves on its own: a value that just
  changed — a `/model` or `/provider` switch, a session adopted, the first frame —
  is shown in motion for one pass (head to tail, a brief dwell) and then settles,
  so "what changed?" is answered once instead of living as perpetual motion. Every
  other field holds a short value and is drawn whole; a fixed field gives way only
  when even one-column cuts cannot fit, so a bar narrower than a single block
  still degrades in the old priority order rather than overflowing.
- **Resuming a session no longer moves the process onto the session's recorded
  model**: `/resume` adopted the target header's `Provider`/`Model` — re-resolving
  the driver, clearing `--base-url`/`--protocol` — so switching into a
  conversation started on another gateway silently changed what every later turn
  ran on, and cost. It also made the two ways to resume disagree: launching with
  `--resume` / `-c` / `golder session resume <id>` seeds the run configuration
  from the CLI/config and had always ignored the record. Both now keep the live
  configuration; a session's recorded `Model`/`Provider` are its history, shown
  in the `/resume` list and in exports, and a user who wants them says so with
  `/model` or `/provider`, here or at launch. Replay is unaffected either way:
  the history is re-encoded for whichever wire is in use, the same path a
  mid-session `/model` switch already takes.
- **The status bar's first block is the provider, not the app badge**: the
  left-most block said `golder` — a word already on every line of the transcript
  — while the fact the bar could not express was which gateway the conversation
  is talking to, since a model id does not name its gateway. The block now shows
  the provider, live state like the model, so `/provider`, `/model` and the
  `/resume` that adopts another conversation's history all move it. An unknown
  provider renders no block rather than a placeholder, and the provider rides
  the model's truncation tier, so a narrow terminal keeps them together.
- **Auto-compaction fires earlier and keeps more**: two defaults were wrong for
  the way a session actually grows. The trigger sat at 90% of the window (a 10%
  headroom), which left 20k tokens on a 200k model for the *next* turn — and a
  tool-heavy turn adds 20-40k of file contents and command output in a single
  batch, so the overflow arrived mid-turn as a provider error rather than as a
  graceful compaction; it also left the model finishing its work in the dirtiest
  tenth of the window. The headroom is now 20%, so the trigger sits at 80%. And
  the verbatim tail went from 20k to 64k: compaction is lossy in exactly the
  place continuity matters, since the retained messages are the raw reads, edits
  and decisions a summary flattens, and 64k holds a real task's working set on a
  200k window. On smaller windows the tail is clamped to half the usable window —
  a fixed 64k tail on a 64k window is a cut the cut-point search can never reach,
  which would have left auto-compaction silently doing nothing while the context
  sat over its own threshold. `/status` and `/memory` print the trigger and the
  reserve they are computed from, so both follow automatically. Settings
  themselves are unchanged: `[compaction] max_context` is still the only knob,
  and its clamp to the provider window still applies.
- **The reviewer's decision model is listed apart from the chat providers**: `typesafe`
  speaks a typed-decision API, not chat completions, which the registry knew but
  the command did not say — `/provider` listed it among the conversation
  gateways, `--provider typesafe` looked legal, and the TUI picker offered it.
  The registry now carries `DecisionOnly`, so the listing gives it its own
  section with its credential, endpoint and proxy route intact (that is how the
  reviewer's requests are routed and how its key is found), while switching —
  `/provider <name>` and `--provider` alike — is refused with the reason, and
  the picker leaves it out. `golder --help` marks it too.
- **Twenty providers retired, from thirty-nine to nineteen**: the registry had
  grown one entry per endpoint anyone ever named, and most carried no behaviour
  of their own — a name, a base URL, and the generic OpenAI-compatible driver.
  Retired in two passes: `ant-ling`, `huggingface`, `novita`,
  `vercel-ai-gateway` and the three `xiaomi-token-plan-*` regional hostnames
  (three names for one API the vendor's own entry already covers); then
  `nvidia`, `fireworks`, `together`, `minimax-cn`, `moonshotai-cn`,
  `zai-coding-cn`, `qianfan`, `volcengine`, `hunyuan`, and four of the five
  enterprise endpoints — `amazon-bedrock`, `google-vertex`,
  `cloudflare-workers-ai`, `cloudflare-ai-gateway` — leaving
  `azure-openai-responses` as the one enterprise entry with the most machinery
  behind it (endpoint composed from a resource name, an API version and a
  deployment map). `dashscope` stays for its qwen- family, and every
  first-tier, subscription and aggregator entry the workflow depends on is
  untouched.

  No functional code was removed: the special-auth resolvers for Bedrock,
  Vertex and both Cloudflare forms remain in `special_auth.go` and remain
  covered — `special_auth_test.go` builds their `ProviderSpec` locally instead
  of looking it up, so the resolvers and the registry cannot drift — and
  `NewNvidiaProvider` keeps its driver and its tests. Bringing any of them back
  is one registry entry. A retired NAME now reports "unknown --provider" with
  the available list, which is the honest answer: it is not a gateway this build
  knows, and its old key variable is not silently accepted by another provider.
- **One state root: everything golder writes now lives under `~/.golder` (or
  `$GOLDER_HOME`)**: the state was split across two directories — sessions,
  history, trust and the credential file under `~/.golder`, while `config.toml`,
  `proxy.toml` and `grants.json` sat under `~/.config/golder` (or
  `$XDG_CONFIG_HOME/golder`) — and each location was re-derived independently in
  a dozen packages, so "where does golder keep this" had two answers that could
  disagree. The split had already cost something real: `grants.json`, the one
  file that decides what the agent may read and write, sat outside the sandbox's
  own protected set while `trust.json` sat inside it. Resolution now goes
  through one leaf package (`internal/golderhome`), `XDG_CONFIG_HOME` no longer
  influences golder's own paths, and the sandbox denies the state root as a
  directory — so a file added there later is protected without anyone
  remembering to add it to a list. Existing files are moved once, at startup:
  the destination wins if it already exists, the move is announced, and
  `permissions.toml` is deliberately not moved (it is retired, and only the
  authorization migration reads it, from where it was written).
- **One authorization store: `grants.json`, and nothing else**: three things
  held path grants — the authorization table, the managed `permissions.toml`,
  and two process-level registries (the read roots in `internal/permissions`,
  the sandbox writable roots in `internal/seatbelt`) — so every reader had to
  consult the right pair, every writer had to write the right pair, and the
  pairs drifted. That is how a directory could be granted for reading and
  still be refused by the sandbox, or granted in a dialog and invisible to
  `/trust`. The registries are deleted rather than shadowed (the sandbox
  profile builders, the boundary gate and the file tools all read the same
  rows), and `permissions.toml` is retired with them: it existed for the
  dialogs to write, and the table is where their answers belong. Two entry
  points keep the validation unskippable — `grants.AddReadRoot` and
  `grants.AddWritableRoot` — because a caller must not be able to introduce a
  grant the store itself would refuse (a root too broad to be a boundary).
  `RemoveCapability` is new with it: a row carries a SET while the commands
  that manage it are separate, so clearing what `/trust` speaks for must not
  silently revoke the read or write grant beside it.
- **Every dialog answers to the same keys, and `p` no longer writes to disk in
  one of them**: the keys were consistent inside each dialog and inconsistent
  between them, in the direction that costs a permission. The launch-time trust
  picker gave `p` the REMEMBERED meaning while the tool dialog gave it "this
  session", so a person who learned the ladder from one place wrote a decision
  to disk while meaning to try it out; and the same trust question answered to
  different letters in the two front-ends (`r` for the reads rung in the REPL,
  `a` in the TUI dialog, where `a` meant all-tools in the REPL). The ladder is
  now one ladder everywhere — `y` once, `p` this session, `a` reads (only where
  the grant reaches), `s` saved, `n`/Esc deny — and only `s` writes to disk. The
  tuios manifest goes to version 3 with it, since its screen rule matches the
  picker's own words; `golder host-compat status` reports an installed copy as
  outdated rather than letting the Inbox go quietly unable to answer.
- **The composer keeps working while a run is in flight**: typing has always been
  enabled mid-run (that is how a steering message is composed), but ↑/↓ refused
  to browse the prompt history and a typed `/` refused to autocomplete — the
  whole cluster was treated as idle-only because *submitting* a slash command
  mid-run was refused. The refusal was one rule too broad: it existed so a
  command would not be queued as steering prose, and so a prompt-expanding
  command could not start a second run (which would tear the first one down) — but
  it also blocked the commands that are meaningful exactly then (`/model`,
  `/permissions`, `/help`), and made a long run the one time the composer stopped
  helping. Now `/name …` lines are resolved as commands whether or not a run is in
  flight, and each command class that must not run mid-run refuses itself where it
  is handled: a prompt/hybrid command (a skill, a template, a plugin command)
  would start a second run, and `/status` and `/memory` read the conversation,
  which the run goroutine owns until the run settles — reading it from the TUI
  goroutine is a genuine data race against the loop's own appends, pinned by a
  race-detector test that reports three `DATA RACE` failures if either guard is
  dropped. Both refusals say why and leave the line in the composer so it runs
  after an Esc, and `/exit` is refused mid-run for the reason Ctrl+D already is:
  the in-flight turn is persisted when the run settles, not before. ↑/↓, Tab
  completion and the autocomplete popup are keyed on the buffer rather than on the
  run state, with the sub-agent panel still owning ↑/↓ while it has rows and the
  composer is empty. One consequence of the composer being live mid-run was wrong,
  and is fixed with it: Ctrl+C skipped its shell-like first stage while a run was
  in flight, so it interrupted the run even with a recalled entry sitting in the
  box. That stage is now unconditional — a non-empty composer absorbs the press,
  mid-run included, because a draft is not a request to stop the run and the box
  mid-run holds recalled history as often as it holds steering text. The
  acknowledgement says which world it is in (idle: `(draft discarded)`; mid-run it
  also names Esc and a second press as the interrupt), and discarding ends the
  browse, so the next ↑ starts from the newest entry instead of walking up from
  wherever the discarded recall left the cursor.
- **Auto-compaction now keeps 10% of the window free**: the trigger was
  `window − 16,384` tokens, which is sensible on a 128k model (~87% full) but
  a rounding error on a 1M one — a 1M-window session only compacted at 98.4%
  full, so a single fat turn could still overflow it. The usable window is now
  `window − max(16,384, 10% of window)`: 90% full on large windows, unchanged
  on small ones where the fixed reserve is the larger of the two. 10% matches
  the low-budget reminder's first notice and the industry norm (Codex compacts
  at 90% of its window; Claude Code around 90–95%). `/status` and `/memory`
  print the threshold with the knob that set it (`reserve: 16,384` or
  `10% headroom`).
- **Shell calls batch in parallel**: bash was declared sequential — one line of
  reasoning, "commands can have side effects" — which made every shell call a
  barrier that also stalled the reads beside it, while codex treats the shell as
  shareable (`supports_parallel_tool_calls = true`, dispatched under a shared
  lock) and gives the exclusive lock to its editing tools. golder now makes the
  same division: `bash` is parallel, `apply_patch` stays the barrier (and still
  cannot race a concurrent write to the same tree). Two commands ordered against
  each other therefore need separate messages, or one chained call — the
  batching guide says so now, where it previously said only that read tools
  batch. Making it safe meant closing two races the old serial assumption hid:
  `BashTool.manager`'s lazy session-manager fallback is mutex-guarded (two
  commands in one batch reach it together), and the TUI's remembered
  approve/deny maps are guarded because `confirmApproval` now runs from several
  tool goroutines at once. The trust gate's stdin prompt was already
  mutex-serialized; its comment claimed that was "cheap insurance" for a
  hypothetical parallel side-effect tool, and it is now load-bearing.
- **A card body hangs its wrapped continuations**: a response line too long for
  the pane used to continue at the margin, so it stuck out to the LEFT of the
  block it belonged to — under the bullet, left of the `└ ` line it continued
  and of every line after it. Wrapped rows now hang at `bodyIndent`, the column
  the body's text already starts at (`wrapBodyLine`), so the whole body —
  opener, continuations, following lines, the collapsed tally, the expanded
  card's `│ ` arguments — has one left edge. The text itself is only prefixed,
  never rewritten, and the wrap budget is the same for every body row, so
  nothing a tool returned is altered and nothing overflows the pane.
- **The collapsed card's "… +N lines" tally sits in the body's column**: it
  counts the body's hidden lines, and it now starts where the body's text does
  (`bodyIndent`, the same column the preview's continuation lines and the
  diff's rows use) instead of flush against the margin, where it read as a row
  of its own rather than the body's own count. It hangs its own continuation
  like every other body row (a narrow pane can wrap it) and wraps on the body's
  budget, so the two cannot disagree about where the pane's edge is.
- **A resumed session rebuilds its system prompt, keeping only the user's own
  words**: the prompt was stored whole and reused verbatim on resume, which
  froze two very different things together — our description of the running
  binary (tool guide, environment block, AGENTS.md, installed skills) and the
  user's own inputs (`--system-prompt`, `--append-system-prompt`). The first
  half goes stale and the second must not. Measured on this machine's store:
  the guide text grew from 1020 to 4959 bytes across twelve days and 11
  distinct versions, so a session frozen a few days earlier advises the model
  about a tool surface that has since moved (a session from 2026-10-10 still
  tells the model to reach for a shell for scoped searches, unaware of the
  `path`/`exclude`/`limit` arguments that shipped afterwards), carries the
  date it was created (the stored environment block says `Date: 2026-09-28`),
  and keeps the working directory it was created in even when resumed
  elsewhere. AGENTS.md edits were ignored for the same reason. Now our half is
  recomputed from the launching binary and working tree, while the user's half
  is carried over from the header — the new `baseInstruction` /
  `appendInstructions` fields record the raw inputs (not a second copy of the
  assembled text), this launch's flags win where given, the session's appends
  layer before the launch's, and a fork inherits both. Sessions written before
  those fields existed have none recorded, so their stored prompt is replaced
  outright by a freshly built one — which is the point, though it does mean a
  custom base in such a session (none exist in this store: all 82 are the
  default guide, 48 of them still opening "You are pigo") is not recoverable,
  since the input was never kept. The header still records what the session was
  created with, so `/export --include-system-prompt` and the session file remain
  a faithful record of the original run. `runtime.PromptInputs.Build` is now the
  single assembly path for fresh and resumed sessions, so the two cannot drift.
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
- **A `write_stdin` card names the command it waits on**: the headline used to
  be the tool plus the session's internal id (`Ran write_stdin bash_355`),
  which told a reader nothing — a run of three polls repeating the same number
  reads as noise. It now says what the call does to the session's command:
  `Waited for sleep 90; echo done`, `Sent "print(1)\n" to python -i`,
  `Interrupted python -i` (codex reads the same way, `Waited for \`cmd\``).
  The bash_id stays in the model-facing result — it is the handle the model
  addresses the session by — but the transcript names the command instead. The
  input preview keeps a trailing newline visible as `\n` rather than
  collapsing it the way prose is collapsed, because whether Enter was part of
  the input is the difference between typing and submitting. When the
  originating bash call is not in view the sentence degrades to a subject-less
  `Waited for the background command` rather than falling back to the id.
- **Notices sit in the content column**: every transcript row starts its
  content at two columns — a user turn leads with `› `, a tool card with `• `,
  and their continuations use `  │ `/`  └ ` — except system notices and gate
  verdicts, which ran flush against the margin. They now indent to the same
  column, which matters most for an interrupt: `(interrupting…)` and
  `Interrupted.` land directly under the card they stopped, and the `⚠` verdict
  under the command it graded. The indent is taken out of the wrap width, so a
  long notice still cannot spill past the pane's right edge.
- **`/think` retired**: it was a second entry point to the same
  `thinkAction` the `/model think <level>` form already called, so the level had
  two spellings for one behavior. The command is gone from `/help`, the TUI
  menu and autocomplete; the surviving spelling is `/model think <level>`
  (bare, it reports the current level), and the action's own hint now names it.

### Removed
- **The reviewer's verdict cache**: it memoized successful grades per `(tool,
  args, trust, conversation state)` so the permission gate and the bash tool's
  sandbox routing would share one review. Both of its reasons to exist went away
  with the read-only fast path: the bash tool no longer grades at all (one grade
  per call), and read-only commands no longer reach the reviewer. It also could
  not pay for itself: replayed over eight real sessions it scored **0 hits out of
  1548 calls that reached a reviewer**, because the conversation state in its key
  changes with every turn while the commands are ~98% distinct — and dropping the
  state from the key would have recovered only 3.6%, at the price of grading a
  call against a conversation it no longer describes. It was also the one
  unbounded `sync.Map` in the judge, growing for the life of the process. Every
  reviewed call is now graded on its own evidence.

### Fixed
- **The model is told which sandbox envelope its command actually ran in**: a
  `sandbox_permissions="require_escalated"` request is a request, and under auto
  the gate can allow the call while re-containing it (judge's
  `ApprovalEscalation`: only an explicit Direct grant runs bare). The person saw
  that decision on the card (`·sandbox` / `·unconfined`) and in the gate's note,
  but the model's context only carries the result text — so a contained retry
  looked exactly like a granted escape, and the model went on reasoning about a
  sandbox it was not in (and blamed the sandbox for unrelated behavior). Every
  escalated call's result now ends with `[escape granted: …]` or
  `[escape not granted: …]`, and the sandbox guide says to trust that line over
  the request: asking is normal work, a refused shape is not repeated, and a
  changed shape (a different path, a narrower command) may ask again.
- **Scrolling stopped rebuilding the whole frame per event, and history stopped
  freezing the flick that reached it**: Bubble Tea asks for a frame after every
  message it dispatches, so one two-finger flick on a trackpad paid a full shell
  render per wheel event and per motion event — 484µs and 9,041 allocations per
  frame, two thirds of it the per-row lipgloss pass the transcript ran over its
  window. The transcript now slices that window out of its content lines and
  pads them itself (39µs and 219 allocations for a 40-row frame, byte-identical
  output), an assistant block reuses its rendered Markdown when neither its
  text, the wrap width nor the palette changed (a glamour render costs 3ms for a
  short answer and 42ms for a long one, and scrolling while an answer streamed
  re-ran it at every step), and materializing deferred history is bounded by an
  8ms render budget per scroll event instead of one 64-block chunk — which is
  the two-second freeze a fast flick hit when it reached the top of a resumed
  session's materialized window. The rest of that step fills in on a 30ms
  continuation tick, so history streams in above the position the flick stopped
  at rather than blocking the event loop in one lump.
- **A turn that ends with reasoning and nothing else no longer ends the run**:
  the model could spend its whole response thinking, stop there, and leave the
  task stopped mid-step — the last tool card stayed on screen and nothing
  explained it, because the check that should have spoken up asked "is there any
  content?", which a reasoning block satisfies. The loop and the front-ends now
  tell an answer apart from reasoning: the loop asks the model to carry on (twice
  at most), feeding back what the previous turn was missing, and stops after that
  with the reason stated instead of going quiet; the REPL, the TUI and the side
  runs say which attempt it is on ("carrying on automatically (attempt 1 of 2)")
  or that the run stopped there. An empty completion is still the provider's own
  retry, and a blank text turn is still an ordinary end.
- **`git clone`'s destination is a path the gates can see**: the directory a
  clone creates is an operand of the *subcommand*, and `git` is in neither table
  that names paths (it neither opens a file operand it is handed nor counts as a
  write verb), so `git clone https://…/repo.git ../repo` named no path at all —
  the boundary gate had nothing to ask about, the sandbox had no writable root to
  widen, and the escalation dialog had no directory to offer back inside the
  sandbox. Two identical clones therefore came out differently: the one whose
  reviewer granted the escape into `direct` ran, and the one whose reviewer did
  not was blocked with advice naming nothing to grant. The destination is now
  read from the shell parse — the second positional word after the clone
  subcommand, with git's global `-c`/`-C` values and every clone option's value
  stepped over, so a proxy URL, a config setting, a repository URL and an option
  value are never the destination — `git -C` folds into a relative destination,
  and the question about it is asked as a WRITE whose offered grant is the
  destination directory itself, not the tree holding every sibling of the
  workspace. No explicit destination, an unknown option, or a value the shell
  computes yields no path at all.
- **A wire probe no longer answers for a wire that never answered**: the probe
  took the first candidate that replied within its budget, so a gateway whose
  `/responses` answered a working 200 in 12-19s (a 24KB response body through a
  relay) was recorded as a Chat endpoint for the model *and* its family for a
  month. Only a rejection settles a question now: a candidate that times out ends
  the probe with no answer, the run keeps its static choice, and a wire that
  really is unsupported is still discovered by the request itself (the adaptive
  fallback records a wire only after that wire served a real request). The budget
  is 30s instead of 5s, and the probe asks over the route the run's own requests
  take (`HTTPClientFor`) instead of a client of its own that ignored `/proxy`
  selection and the provider's per-route transport.
- **The boundary gate asks about the side the call actually takes**: a shell
  redirect outside the workspace (`echo hi > /outside/f`) used to raise a READ
  question — so "allow reading <dir>" could authorize the write — and a write
  verb's operands (`rm /outside/f`, `mv`, `cp`, `mkdir`, …) were not extracted
  at all, leaving a bare (envelope) shell with no place question for its writes.
  Each shell candidate is now classified by the command's own text
  (`PathWritten`), read grants no longer silence write questions, and the
  dialog/title name the write. The re-admit dialog also drops its "reads and
  writes" row unless the command needed to write, and labels say when a grant
  covers the folder and below.
- **`/network follow` now ships on, and covers every approved level**: under
  auto an Allow, a Confirm approved on the spot, and a Contain all mean "this
  call may run", so the network rides that answer instead of raising a second
  question — the reviewer already settled the risk, and the prompt was the same
  decision asked twice. `follow` is a tri-state in `network.toml`: an absent key
  means on, `follow = false` is the explicit opt-out that restores per-call
  asking. A remembered denial still wins, Deny and a failed review still grant
  nothing, and ask mode is untouched.
- **Command Code's transport keeps HTTP/2**: the no-HTTP/2 tuning made the
  client parse the endpoint's h2 SETTINGS frame as a malformed HTTP response;
  the provider now leaves HTTP/2 enabled and keeps only the compression and
  keep-alive tuning.
- **The bare-run network fold now checks enforce, not RequireSandbox**: auto
  sets `RequireSandbox` because isolation is the default, but the envelope can
  still choose the host. A separate `Enforce` flag now gates the fold, so an
  auto Bare+Allow carries its network while `enforce` still asks.
- **A refused escape no longer explains itself with the command's safety**: the
  note read `allow → denied …: this command is read-only and reasonable`, because
  it reused the reviewer's rationale for grading the COMMAND while the thing
  refused was the ESCAPE. It now says the command was allowed but no `Direct`
  grant was given, and names the two real fixes: request `network:on` if only
  network is needed, or switch `/permissions ask` to decide the escape yourself.
- **A connection that dies before any response is retried instead of ending the
  turn**: a POST that never got a reply reaches the user as net/http's own
  wrapper — `error: Post "https://…/responses": EOF` — which nothing classified
  as a cut, so the retry budget never applied to it. `isConnectFailure` now
  recognises the shapes that mean "no response was produced" (EOF, connection
  reset, broken pipe, refused, GOAWAY, DNS, TLS and I/O timeouts) while leaving
  a truncated *response* to the truncation path and a configuration error alone.
  The retry layer covers it on the first call and mid-stream alike, and when the
  budget is spent the sentence names the provider, the attempt count and the
  transport's own text.
- **The denial dialog can request one unconfined run**: when the
  sandbox refuses a path the command needs and no grant can fix it — a
  SIP-protected system file, or credential material the static floor refuses to
  hand over — the question now offers `Request one unconfined run` beside the
  path grants. That is a request, not the answer: it goes through the permission
  gate again (a second, narrow confirmation under ask; the reviewer under auto),
  and only a Direct verdict runs the command once outside the sandbox. Nothing
  is registered or remembered, the run is announced like any escape, and the row
  is not offered in read-only mode. Both the TUI dialog and the REPL prompt
  carry it.
- **A SIP error is no longer offered as a sandbox path grant**: macOS reports
  protected system files such as `/private/var/select/sh` as `Operation not
  permitted` even outside the sandbox. The re-admission dialog used to take that
  first path and offer to allow it, while the real blocker (`~/.ssh/known_hosts`
  in the reported push) was never named. `PathsFromDenial` now skips
  system-protected and credential paths; when no grantable path remains, the
  tool explains what was refused and points at a one-call unisolated approval or
  a known_hosts copy outside `~/.ssh`, instead of a misleading grant.
- **Remote control no longer replaces the gate chain**: while a browser was
  paired, `bridgeBeforeToolCall` returned nil after the browser answer, so the
  workspace-boundary and permission gates never ran for tools the trust manager
  could gate. It now uses the trust answer only for the trust question and then
  runs the boundary → permission chain; calls the trust manager does not gate
  run the full local chain. A browser may answer "do you trust this folder"; it
  may not answer "may this read leave the workspace" or "may this command run".
- **Network approvals can be remembered per host or command**: `webfetch` keys
  on the exact host, `websearch` on the tool (its backend host depends on which
  credential is present), and `bash` on a literal program/subcommand such as
  `git push`. The question now offers one call / this session / remembered, and
  `/network list|rm` manages the rules. Local, private and IP-literal hosts are
  never offered as standing rules.
- **A path prompt and its network question are one dialog**: when a call needs
  both consent to reach outside the workspace and network access, the boundary
  prompt carries the network question instead of the permission gate opening a
  second one. The TUI keeps one modal open across both stages; the REPL asks the
  second question in the same block.
- **A person can now answer the questions that used to fail closed**: network
  approval, no-sandbox approval, re-admission after a sandbox denial and a
  failed-review call all have stdin prompts where a REPL is present, and the TUI
  dialog covers the same paths. Auto mode still decides ordinary risk tiers by
  review; it asks only when it cannot proceed (reviewer unavailable, no sandbox,
  or network not authorized).
- **Session network answers reach isolated sub-agents**: the parent's
  session-only network rules travel in the child's authorization snapshot, so a
  delegated run does not ask again for a host or command the parent already
  allowed. Remembered rules are reopened from `network_grants.json` by the child.
- **Failure text says what was not done and what to do next**: the gate and bash
  no longer end at `failing closed`; the model-facing messages name the missing
  capability and the smaller fix. The TUI trust prompt no longer records an
  undeliverable browser question as a user denial.
- **A session grant for a credential path no longer breaks every file tool**:
  after approving a one-session read of `~/.ssh`, every later `apply_patch`,
  `read` or `grep` call failed with `tool worker grant targets protected state or
  credential material`. The worker policy check rejected the whole request when
  any mounted path was protected; it now drops that ambient grant from the mount
  set and proceeds. A call that explicitly requests a protected path is still
  refused, because that is the caller asking for this exact file.
- **A refused command no longer ends with a green success dot**: tool results
  carried no error flag, so `bash` rejecting a network request (and any other
  `errorResult`) was recorded as a successful run. `AgentToolResult` now carries
  `IsError`, the executor passes it to the end event and the tool message, and
  the card colors the refusal as an error.
- **A long prefill is no longer cut like a mid-stream hang**: the stream guard
  uses two different budgets now — a first-token window (3 minutes, widening to
  4 and 5 minutes on the first two retries;
  `GOLDER_STREAM_FIRST_CONTENT_TIMEOUT` overrides the base) and the inter-chunk
  window (the existing idle × 1.2) once content has arrived. The single window
  before this cut a genuinely slow request at the same mark it used for a stalled
  answer, which is exactly the reported opencode-go case: three attempts, 108
  seconds each, no token ever produced.
- **A watchdog failure says what it saw**: the failure and the TUI retry notice
  now carry the guard's evidence — `412 bytes, 0 content frames, 12 keep-alives,
  last line: comment` — and name the phase (`no answer started within 3m0s`
  versus `output stopped for 1m48s`). That distinguishes a gateway that holds the
  socket open while producing nothing from a connection that went dead, which the
  old "no output" wording could not.
- **Two silent attempts end the turn instead of widening the wait**: when neither
  the first attempt nor its retry ever starts an answer, the retry chain stops and
  reports `no answer started on 2 attempts — the connection stayed open and the
  model produced nothing`. Burning the remaining budget on identical waits was
  what made the reported turn sit for four minutes before a manual interrupt.
- **A model without a Responses endpoint falls back to Chat automatically**:
  when a pristine first request on the Responses wire is rejected with
  `400 ModelProtocolUnsupported` (the reported opencode-go/mimo-v2.6-flash
  case), the driver retries the same request over the gateway's Chat
  Completions wire, forwards that stream, and records Chat in the persistent
  wire cache for the model and its family. `ProviderSpec.ModelWireRules` pins
  known exceptions — `mimo` on opencode-go — so a fresh install chooses Chat
  without paying for the first failed request; the cache still covers every
  other family the gateway rejects.
- **A non-protocol wire answer is no longer masked by the fallback**: when the
  current wire says `model_coming_soon` — or fails for any reason other than
  `ModelProtocolUnsupported` — the adaptive chain stops and surfaces that real
  answer instead of replacing it with a later wire's generic protocol
  rejection. The answering wire is recorded, so a model that is merely not
  live yet stops paying for the remaining attempts.
- **A fallback wire is discovered once per process, not once per turn**: after
  the adaptive chain finds the wire a model actually speaks, later requests
  start on that wire instead of replaying the static guess and its `400` every
  turn. The persistent cache already covered the next resolution, but a running
  session reuses the same provider, so it kept paying the failed request; the
  wrapper now promotes the confirmed wire in-process (first success wins, safe
  under concurrent sub-agent turns) and keeps the remaining declared wires as a
  fallback if the gateway changes its routing mid-session.
- **A pipe-mode command can no longer prompt on the TUI's terminal**: a
  non-tty session's stdout/stderr are captured, but the child inherited
  golder's controlling terminal, so ssh's host-key confirmation (seen on a push
  whose sandbox could not read `~/.ssh/known_hosts`), sudo and git credential
  prompts opened `/dev/tty` and wrote straight through the raw-mode TUI —
  leaving a stray "yes" prompt and garbled screen. Pipe sessions now start in
  their own session with no controlling terminal: those prompts fail fast with
  their error in the captured output, while process-group interrupts still
  reach forked children. `tty=true` sessions keep their PTY and prompts by
  design.
- **A stream cut before the answer arrives is retried, and named when it cannot
  be**: a stream ends early in three ways, and every one of them ended the turn.
  The connection dies mid-response — `io.ErrUnexpectedEOF`, whose text is
  exactly `unexpected EOF` (`net/http` reports it for a chunked body that never
  sees its last chunk, or one short of its Content-Length), which the Responses
  wire's OpenAI SDK hands back unchanged, so the transcript showed a bare
  `error: unexpected EOF` naming neither provider, nor wire, nor what was lost.
  The connection goes quiet entirely, which the Responses wire had no watchdog
  for at all: a gateway that accepted the request and then went silent left the
  turn hanging with no timeout, no error, and no way out but Ctrl+C. Or the
  connection stays alive on SSE keep-alives while the model produces nothing,
  the failure a keep-alive-happy gateway hides longest. All three now speak one
  vocabulary and are retried once when nothing has been shown yet — the same
  safety rule as the empty-completion retry, since no content means a retry
  cannot duplicate anything — while a cut that lands after content keeps the
  partial answer and explains itself: `opencode-go: stream cut — the connection
  closed before the answer finished (unexpected EOF); retry the turn`. The retry
  is one decorator applied at resolution, so the policy cannot vary by wire or
  by how the provider was selected, and the SDK path now carries the same dual
  watchdogs as golder's own transport, governed by the same
  `GOLDER_STREAM_IDLE_TIMEOUT`.
- **Chat, Messages and Gemini streams can no longer be held open by
  keep-alives**: their content-stall watchdog was re-armed on every SSE blank
  line, including one that closed a comment-only keep-alive frame with no
  `data:` payload. A gateway that keeps saying "I'm alive" while producing
  nothing therefore defeated the very guard meant to catch it, and the turn hung
  until the idle window or Ctrl+C. Only a framed payload resets
  the stall window now; keep-alives still reset the idle window, which is what
  they are for.
- **A stream that ends without a final event is retried like a dropped
  connection**: the wire fallback turns a start-only stream into
  `stream ended without a terminal event`, which the retry layer saw as a real
  provider answer and surfaced without a second attempt — the exact
  commandcode/mimo turn that ended in fourteen seconds with that sentence. Both
  sides now share a sentinel, so the silent end is classified as a cut and gets
  the same single retry when nothing has been shown.
- **A cut stream's sentence names the provider once and admits a spent retry**:
  the drivers already prefix their errors with the provider, so the sentence read
  `commandcode: stream cut — … (commandcode: unexpected EOF)`; the cause is now
  stripped of the prefix the sentence already carries. A cut that survived the
  retry says so — `already retried once, and the connection dropped again` —
  instead of inviting the user to wait for a retry that has already run, and an
  interrupted turn that was mid-retry is filed as an abort again rather than as a
  provider failure.
- **A denial says what it refused**: a call that asked to run outside the sandbox
  and had that request declined now reads `denied · the sandbox escape was not
  granted` (or the Chinese equivalent) instead of a bare `denied`. The verdict line
  judges such a call twice — once on the command, once on the escape — so a plain
  denial left a grade and an outcome beside each other, `allow → denied`, with
  nothing explaining why the two differed; that reads as a contradiction rather
  than a decision, and it is why an approval card could look like it had both
  allowed and refused the same call.
- **The status bar folds what is not pinned, spends the whole row, and expands on
  hover**: every field folds — the provider and the branch included, so a single
  rule explains the whole bar — into its glyph plus as much of its value as its
  share allows. A folded field is not a fixed stub: after the focused field has
  what it needs, the remaining columns are shared among the folded ones, so each
  shows a recognisable slice (the end of the model name, the last path components,
  the context percentage) and the row is used to its last column. All fields,
  folded or not, are separated by the bar's own powerline arrows. Moving the
  pointer onto a segment expands it in place (the path shows whole), and moving
  away collapses it on the next frame; clicking pins one open for a terminal
  whose mouse reporting carries no motion events, with a second click or a click
  on the background releasing it. A **protocol** segment after the model reports
  the wire actually in use (`responses`, `messages`, `chat`, `v1beta`), read live
  from the provider so an adaptive wire fallback updates it mid-session. What is
  painted and what the pointer hits come from one layout pass, so they cannot
  disagree, and `/status` still prints every field whole. The mouse mode moves
  from xterm 1002 to 1003 (all motion) to deliver a hover at all; click, release,
  wheel and drag are a subset of it, and a terminal that reports 1002 only keeps
  the click path.
- **A cut stream gets several reconnects, and each explains itself**: the retry
  layer now makes up to four reconnects (five attempts) instead of one, separated
  by an exponential backoff from 250ms to 5s, because a long-running turn is
  expensive to lose and a reconnect usually succeeds within a few tries.
  `GOLDER_STREAM_MAX_RETRIES` overrides the budget (0 disables retries). Before
  each attempt the transcript shows a notice naming the provider and the failure —
  `provider commandcode 的流中断（1m30s 无数据），正在重试（2/4）…` (or the English
  equivalent, following the conversation's language) — and a spent budget says
  `already retried 4 times`, so the wait is never a bare spinner.
- **The idle watchdog now defaults to 90 seconds**: five minutes of silence was
  indistinguishable from a hang, and it made a single cut cost up to ten minutes
  once the retry's own window was added. A provider that has sent nothing for
  90 seconds is now declared cut and retried; `GOLDER_STREAM_IDLE_TIMEOUT` still
  overrides the window (and the content-stall window remains 1.2× it).
- **A failed patch hunk explains the closest match, and out-of-order hunks
  apply**: `apply_patch` now retries a hunk from the start of the file when the
  forward search fails, so a patch whose hunks were listed out of order still
  applies as long as they do not overlap. When a hunk genuinely cannot be located,
  the error names the closest line, how many of the hunk's lines matched, and the
  first expected/found pair — `closest match at line 42 (3/5 lines match; expected
  "foo", found "bar"); re-read the file and resend` — instead of a bare hunk
  number. The patch guide now says explicitly that hunks are matched against the
  file as it is now, must not overlap, and that a failed patch should be re-read
  and resent rather than retried verbatim.
- **A patch that changes nothing says so**: a hunk made only of context lines
  matched the file, wrote it back unchanged, and reported `Applied 1 change(s)`
  with no diff — indistinguishable from a real edit whose diff had gone missing.
  Such an update now produces no change at all, and a patch with no changes tells
  the caller `no changes — every hunk matched the file but none added or removed a
  line`, so a context-only patch is never mistaken for a successful edit.
- **A nil trust store no longer crashes a session grant**: when `trust.json` could
  not be opened, the front-ends carry on with trust disabled and a nil manager.
  Every read already answered "untrusted" there, but `SetSessionTrust`,
  `SetSessionReads` and `ClearSessionTrust` dereferenced the receiver — so
  answering a trust prompt in that state panicked the process (it took down an
  unrelated TUI test on a sandboxed host). All three are no-ops on a nil manager
  now, matching the rest of the package.
- **Command Code's endpoint-mismatch 400 advances the wire chain**: its
  `unsupported_model` response says the model "is not available on this
  endpoint" (or "not supported on this endpoint") and names the endpoint to
  use instead. That phrasing now counts as a wire mismatch, so a model served
  only on `/chat/completions` falls through Responses and Messages to Chat
  instead of being cached on the wrong wire.
- **Shift+Enter keeps working after a paste**: a terminal or multiplexer that
  pushes/pops its keyboard-enhancement flags around a paste can leave
  Shift+Enter encoded as a bare Enter, which the composer reads as submit — so
  no newline is inserted. Every paste now re-asserts the flags Bubble Tea
  requested at startup (`CSI =1;1u` plus modifyOtherKeys level 2); the sequence
  is a no-op when the flags survived. Ctrl+J remains the protocol-free fallback,
  and the composer placeholder names it. The composer also recognizes the same
  gesture when a terminal encodes it as Shift+Enter carrying associated text
  (`String()` is a bare `"\r"`) or as xterm modifyOtherKeys' Ctrl+J code 10
  (`String()` is `"ctrl+\n"`); those shapes used to fall through as unhandled
  keys, which is why Ctrl+J looked dead while ordinary typing still worked.
- **The composer keeps accepting newlines past its six visible rows**: the
  textarea's `MaxHeight` doubles as a logical-line cap through its legacy
  content check when `MaxContentHeight` is unset, so the seventh Shift+Enter
  was silently refused and later characters piled onto the sixth line. The
  editor now sets `MaxContentHeight` independently: the box stays six rows
  tall and scrolls internally, while the buffer keeps growing to 10000 visual
  lines.
- **The wire probe can no longer mistake a gateway's web page for a working
  wire**: a probe accepted any 2xx and threw the body away, so a gateway that
  serves its SPA at unknown paths answered a root-level `/messages` with 200 +
  HTML and was cached as an Anthropic endpoint — which then made
  `/provider openai` display (and use) `protocol: anthropic` against a relay
  that actually speaks Responses. The probe now asks only the wires the
  provider declares, accepts a 2xx only when the body is that wire's own
  response shape (JSON, not HTML or an error object), and ignores a cached
  answer outside the provider's declared wires. Existing false positives stop
  applying without deleting `wire-cache.json`.
- **A PTY failure is a capability answer, not a sandbox-path denial**: when the
  environment cannot allocate a pseudo-terminal, the bash tool now appends a
  hint that no escalation can create one and points at in-process tests. Before,
  the failure fell through to the sandbox advice, which invited a
  `require_escalated` retry that could only produce the same consent dialog.
- **A `/provider` switch lands on a model, not just a gateway**: `opencode-go`
  — the multi-vendor gateway golder launches on — declared no default model, so
  switching to it changed the provider block while the model kept naming the
  previous gateway; the status bar read `opencode-go` beside `gpt-6-astra`, and
  the next turn went out with the old model. The gateway now declares the pair
  golder launches with (`deepseek-v4.1-flash`), so both blocks move together.
  A registry test now pins a default for every built-in provider, so a future
  entry cannot reopen the gap.
- **Ctrl+C closes an open picker, exactly like Esc**: the command popup and the
  pickers hold focus with an empty composer (the command that opened one was
  submitted), so Ctrl+C fell through to the global handler, whose empty-box
  branch arms the quit — the selection box stayed up, and a second press quit
  the program. It now dismisses whatever is open first, and the second press
  does its usual job.
- **A model list can no longer describe the gateway you already left**: `/model`'s
  catalog fetch is asynchronous, and its cache key was computed after the
  request — so a `/provider` switch that landed while the list was in flight
  filed the old gateway's lineup under the new gateway's key, in the session and
  on disk (24h). Every later `/model` then offered the previous provider's models
  under the new provider's name, which read as "the switch didn't take". The
  gateway identity is now read once, before the request: a result whose provider
  no longer matches is neither adopted nor stored, the fetch reports the change
  instead, and the picker refuses to open for a gateway it no longer describes.
- **A thinking request a gateway refuses is re-issued in the shape it named**:
  the provider catalog is fetched in the background, so a one-shot run
  (`golder -p`) may never have it — and a model newer than the cache then got
  the legacy token-budget body, which the Messages API rejects outright
  ("thinking.type.enabled is not supported for this model ... use
  thinking.type.adaptive", reproduced live against opencode-go). A 400 whose
  message names the adaptive form now corrects the body and re-issues the
  request once, and the verdict is remembered for the rest of the process, so
  the failure costs one extra request instead of the turn. Unrelated 400s are
  not retried: the correction is anchored on the shape the error names.
- **Reasoning from another provider is no longer replayed onto the Messages
  wire**: the Anthropic encoder echoed every stored thinking block, signature or
  not, so a conversation carried over from another gateway (`/model`,
  `/provider`, or a resume) sent that model's reasoning as a signature-less
  `thinking` block — which the API refuses outright ("messages.1.content.0.
  thinking.signature: Field required", reproduced live against opencode-go's
  Messages endpoint with Claude Haiku 5.5). A block is now replayed only when
  golder holds the evidence that it came from this wire — the signature the
  Anthropic decoder fills (or a redacted block's data) — and only when the
  request turns thinking on; anything else is dropped while the text and the
  call/result pairing ride as before. The reverse directions were already
  covered and are now pinned by tests: the Responses input builder and the Chat
  Completions encoder take text and calls only, and the Responses reasoning
  items stay gated to the model that produced them.
- **The Messages driver now sends opencode's routing header**: `opencode-go`
  refuses a `/messages` request without `x-opencode-session` (400
  MissingSessionID: "cannot be routed efficiently"), the header the chat and
  Responses drivers already attached. The Anthropic-wire driver attaches it for
  `opencode`-named providers too, so those gateways' Messages models are
  reachable.
- **`/resume` leaves the bar and the banner describing the conversation in the
  pane**: the status bar and the redrawn splash panel printed the launch
  provider and model after a session switch, so `/status` (which reads the live
  config) disagreed with both. `/resume` is intercepted before the generic slash
  path that re-synced the bar, and the banner was rendered from the launch
  Options. Both now read the live config: the bar through one helper
  (`syncLiveDisplay`) that `/model`, `/provider`, `/resume`, `/clone`, `/fork`,
  `/import`, a branch switch, `/rewind` and the first frame all go through, and
  the banner through `bannerOptions`, which layers the live provider, model,
  protocol and thinking level over the launch snapshot. Every surface that shows
  the provider/model now shows the same one.
- **A Responses tool step is replayed in one shape for every backend**: the
  replay used to special-case OpenCode Go's DeepSeek models, and it did so
  wrongly — it inserted a reasoning item directly before *each* replayed
  function call, with an empty `reasoning_text`. Probed live against both
  DeepSeek endpoints (the first-party API and OpenCode Go, which behave
  identically) and an OpenAI relay, the wire rules turned out to be two, and the
  old shape broke both: an item inside a call run ends the run as far as the
  gateway is concerned (400 `No tool output found for tool call …`, naming a
  call whose output *is* present — the misleading error the upstream
  [opencode issue](https://github.com/anomalyco/opencode/issues/54698)
  describes too), and a step whose head
  carries no non-empty `reasoning_text` is rejected as `The 'reasoning_text' in
  the thinking mode must be passed back to the API`. The provider/model test is
  gone: every thinking-mode tool step now leads with its reasoning — the
  captured items replayed verbatim, plus one explicit non-empty placeholder
  ("the model's original reasoning for this step was not recorded") when the
  step has none to offer — and its calls and their outputs stay contiguous.
  Thinking-off requests, text-only steps and the OpenAI wire are unaffected.

- **A consent question is raised for a path, not for a string that looks like
  one**: the boundary gate asked the user to allow reading `/60` — the divisor
  in a python program's arithmetic — because the candidate list was the UNION of
  two readers, and whichever was less careful won. One reader was the shell parse
  (mvdan, via `shellfacts`), which reports every path-shaped ARGUMENT and says
  the caller must narrow it; the other was a hand-rolled word scanner that knew
  about programs (a `sed` script and a `grep` pattern are not files) but knew
  nothing about a program handed to an interpreter. The union meant the parse's
  un-narrowed word arrived as a question, and the scanner's `/60` arrived beside
  it: `sed -n '/func X/,/^}/p' f.go` asked about `/func X/,/^}/p` too.

  There is one reader now. The shell parse answers everything lexical, and the
  program-semantics tables answer what a grammar cannot: which programs open a
  file they are handed, which argument positions hold a pattern or a script, and
  how a word's parts become a value (`$HOME` and a leading `~` are knowable from
  the parse's parts, an unknown variable is not, and a glob is a location with a
  pattern). The rules are the ones the scanner already had — they now run on the
  parse's words instead of on a second tokenizer.

  An interpreter's inline program is handled by kind rather than by guessing. A
  shell program (`sh -c '…'`) IS a command line, so it is parsed recursively and
  its reads are found exactly. Any other language is one this package does not
  parse and never will, so its text is read for path-shaped runs that sit inside
  a quoted string AND exist on disk: this gate asks a read question, and a path
  that does not exist cannot be read. Two conditions, both verifiable, which is
  what makes `/60`, `/`, `//` and `re.match("/api/v1", u)` stop being paths
  without anyone enumerating the shapes that produce them.

  Worth stating as a hole rather than an oversight: a program that CREATES a path
  (`open("/new", "w")`) names a file that does not exist yet and is not reported.
  Writes by a shell command are not this gate's business — the sandbox and the
  escalation dialog are — which is the same reason bash asks about reads.

  Measured on the command that produced the report: no candidates, no question.
  Beside it, `sh -c 'cat /etc/hosts'` still yields `/etc/hosts`.
- **A boundary refusal is the boundary gate's, not the reviewer's**: a call
  blocked by the workspace-boundary gate was announced as `✗ 自动审批拒绝` —
  an auto-review denial — with the reviewer's "(risk, authorization)" suffix.
  The reviewer had not seen the call; the boundary gate had, and the person
  had (or had not) answered. Two different things also shared that one
  sentence: "the user said no", which is a decision and is recorded so the
  question is not put twice, and "nobody could be asked", which is a failure
  to ask and records nothing. Reporting the second as the first blamed
  auto-review for a question nobody was asked — the confusion that started
  this. There are three boundary kinds now (granted / denied / no prompt),
  each rendered in its own words, the gates' own sentences are localized
  (`用户拒绝了本次请求`, `本次运行无人可问`) instead of leaving English in a
  Chinese conversation, and `NoteKind.Failure()` decides the error color in
  one place rather than in both front-end renderers.
- **A sub-agent's consent question reaches the person, labeled with the agent
  that asked**: the chain a child inherited was built with the driver's answer
  channels stripped out, deliberately, so a child's question would fail closed
  instead of parking a goroutine on a dialog nobody opened. That reasoning was
  wrong twice over. The person IS there — they are watching the batch — so
  failing closed turned a question they would have answered into a silent
  refusal the child then had to work around; and the hazard it named is what
  the channels' own absence already covers, since a headless run, an embedder
  with no driver and a process-isolated child all pass nothing and still fail
  closed. Both chains now come from one builder. The label is what makes
  sharing safe: a dispatch stamps its name onto the context (appending, so a
  nested child reads `task › explore`), the dialog titles the request
  `bash (from task)`, the stdin prompt and the transcript line say the same.
  Writing the test for it exposed a real hole beside it: `ToolAuth` carried
  the session read roots across a process boundary and not the write roots, so
  a session-only "allow writing /x" — which exists in this process's table and
  nowhere else — was refused by a process-isolated child that could not even
  ask. Remembered roots never had the problem (the child opens `grants.json`
  itself), which is why it went unnoticed; the snapshot now carries both and
  the RPC carries the origin label too.
- **A symlinked spelling of a whitelisted path is no longer a boundary
  question**: the read whitelist and the gates that consult it compared cleaned
  paths, and cleaning does not follow symlinks, so on macOS a literal `/tmp/x`
  did not match the whitelisted `/private/tmp` — the user was asked about a path
  the sandbox already admits, which is how a prompt becomes noise people dismiss
  without reading. `permissions.Canonical` resolves a path the way the kernel
  will (down to the deepest existing ancestor, so a target that will be created
  is still canonicalized by its parent) and `InsideSystemReadRoots` checks both
  the literal list and a resolved one. Canonicalizing the LIST itself was tried
  first and reverted: the sandbox's allow rules must name the path the kernel
  walks, and resolving them away broke `cat /etc/hosts` under seatbelt —
  measured, not assumed.
- **A contentless SSE frame no longer kills a turn with `unexpected end of
  JSON input`**: the Responses driver streams through openai-go's decoder, which
  dispatches an event on EVERY blank line — including one that terminates
  nothing (a keep-alive, a leading blank, an extra separator after the last
  event). The dispatch then carries empty data, the SDK runs `json.Unmarshal` on
  it, and Go's error for empty input is that message: an assistant message with
  no text, no tool calls and only a parse error, several times in one session
  against a third-party relay. golder's own SSE transport was already immune
  (its flush returns early on an empty payload), so the fix is a RoundTripper on
  the SDK's client that drops frames carrying no content — while leaving a frame
  WITH content byte-for-byte alone, because a blank line is what separates two
  events and removing the wrong one would merge them. The reproduction is a
  test: each case first asserts that the unfiltered stream fails with exactly
  that error, so the fix cannot be mistaken for a workaround for something else.
- **An empty completion is retried once instead of sending the user to check
  their configuration**: a gateway occasionally closes a stream with a
  `response.completed` event whose output carries no text, no reasoning summary
  and no tool call — sometimes with no usage either. The turn then ended with
  nothing, and the only thing the user saw was "note: empty response from the
  model (no content). Check that --model, --base-url and --protocol match the
  same provider", which sent them looking for a mistake that was not there. One
  retry is the honest remedy and is safe exactly here: the message is empty, so
  nothing has been shown and nothing can be duplicated. A second empty answer
  still surfaces, because a model that keeps ending its turn with nothing is not
  a problem another request fixes. The retry has its own flag rather than
  reusing the loop's first-attempt test, which the hosted-search fallback also
  needs — sharing it meant an empty first answer consumed the counter and left
  the search fallback unable to run.
- **`~/…` operands are asked about, and a multi-operand read is offered its
  finest directory**: two holes in the read-scope gate, both surfaced by one
  session. The shell expands a leading unquoted `~` itself, but the scanner read
  the word literally, so `ls ~/Desktop` resolved against the workspace, looked
  like an in-workspace read, and reached the user's home with no question asked
  — while `ls /Users/<me>/Desktop` did ask. `expandTilde` now substitutes the
  shell's own expansion for an operand whose leading tilde was written unquoted
  (`~/x` and `~/"x"` expand, `"~/x"` does not, matching bash exactly; another
  user's `~name` stays untouched), inside a shell's `-c` program too, where the
  inner shell expands it the same way. Second: a call that names several paths
  got one question built from whichever path came first, so `ls ~/work
  ~/work/proj` offered `~/work` — and the session rung of that dialog hands over
  the whole tree, so the coarse answer outlived the call. The gate now resolves
  every operand and offers the finest directory among them (the first in command
  order wins ties). The same pass found a third, silent hole: `grep` and `find`
  accept `path` as a string **or a list**, and the gate decoded only the string
  form, so an array argument failed that unmarshal and was read as "no path
  here" — a list-form search outside the workspace was never asked about. Both
  JSON shapes are read now, and their entries resolve like shell operands.
  `permissions.ReadScopeCandidate` also covers a glob operand, which had no
  directory of its own: `ls ~/work/*/*.go` offers the deepest existing directory
  above the pattern instead of a name containing `*` that no grant could match.
- **A 16-color terminal gets a readable status bar**: the bar draws a powerline
  ribbon whose fields are distinguished by their background blocks, and it assumed
  the terminal could show them. On a terminal detected as 16-color, the three
  blocks for the model, the reasoning level and the path were all approximated to
  the same blue — bright white on a blue the terminal's own theme defines, which is
  hard to read and makes the three fields indistinguishable — and the arrows
  between blocks became blue-on-blue and vanished. A block palette needs 256
  colors; below that the bar now drops the blocks entirely and colors each field's
  text with one of the sixteen ANSI colors (hue-distinct, so they stay apart on any
  theme), joined by a dim separator that needs no second color to be visible. The
  form is chosen from the terminal's reported profile (`tea.ColorProfileMsg`),
  falls back to the ribbon when nothing is reported, and both forms take their
  fields, order and truncation priorities from one place, so a narrow bar drops
  the same segments either way.
- **An exploration cell settles on `Explored` again**: a `• Explored` cell whose
  calls had all finished is folded into the committed prefix and rendered once
  from that stored wrap. The next turn's first search folds into the same
  trailing cell — which is right, the call *is* more exploring — so the cell
  re-renders as `• Exploring`; when that call finished the header never came
  back, and the cell read `Exploring` for the rest of the session. The
  completion flipped the card's state and left the committed wrap alone, and the
  prefix is only ever scanned *forward* (a folded block is never re-examined),
  so nothing repainted it. Every mutation of a card that can already be
  committed now drops the wrap — the two that were missing were a tool
  completion and the run-end close of a card whose end event never arrived — and
  the invalidation lives in one place (`invalidateCard`/`invalidateGroup`, which
  the click and the gate-verdict paths already needed) instead of being spelled
  out per caller.
- **A read outside the workspace offers the directory the call actually
  reads**: the session grant in the dialog was built from the *parent* of the
  path the call named, so `ls ~/work/harness/` offered `~/work` — one directory
  coarser than the read, and the coarser grant is the one that sticks for the
  session. The candidate is now the path itself when it is a directory (an
  `ls`/`grep`/`find` operand, a shell command's directory argument, a trailing
  separator) and the directory holding it when it is a file, so the grant
  covers exactly the tree that was asked for and nothing above it. A path that
  does not exist yet is still treated as a file, and a directory too broad to
  grant (`$HOME`, `/`, `$HOME`'s parent) still falls back to the one-read
  answer.
- **Context readings are read at their high-water mark, and never re-read after
  a compaction**: the trigger trusted the newest assistant `usage` block, but
  the gateway alternated between two accounting levels for one growing
  conversation (~87% and ~95% of a 1M window, hundreds of times in a row, plus
  one 980k spike between two 383k readings), so the status-bar percentage and
  the compaction decision flapped with it — and once a compaction did rewrite
  the history, the retained tail's readings, which describe the pre-compaction
  request, were read back as the current size and could re-trip the threshold
  forever. The estimate now takes the largest of the last four valid readings
  (a lone outlier more than 2× its neighbours is gateway noise, dropped) and
  `CompactionMessage` records `KeptCount` so post-compaction accounting
  ignores the stale tail (a checkpoint written before the field existed still
  excludes its summarized prefix, settling in at most one extra compaction).
- **`[compaction] max_context` finally does something**: the key was documented
  and parsed, but no code read it — the knob was dead. It now flows through the
  session's live config into every driver (TUI, REPL, goal loop, side thread,
  `golder -p`), is resolved per read so a `"50%"` form follows a `/model`
  switch, and can only lower the trigger point. Relatedly, headless and SDK
  runs had auto-compaction silently off: the shared `run.NewConfig` never set
  `Compaction`, so `Enabled` was false for every `golder -p` run with a known
  window; the defaults are now set at that one assembly point.
- **The TUI no longer reports a failed compaction as success**: any
  `CompactionEvent` printed `(context compacted)`, including the non-fatal
  failure case where the context is left unchanged — the REPL printed the
  error all along. The failure text now reaches the transcript.
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
