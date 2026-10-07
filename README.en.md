# golder

**English** | [简体中文](README.md)

[![CI](https://github.com/getan/golder/actions/workflows/ci.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/ci.yml)
[![Build](https://github.com/getan/golder/actions/workflows/build.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/build.yml)
[![Release](https://github.com/getan/golder/actions/workflows/release.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/release.yml)

A terminal AI coding assistant written in Go: read and write files, run commands, search code, and fetch web pages — closing the loop from understanding a request to shipping the change. Starts in a full-screen TUI by default, with a line-based REPL and a headless mode for scripts.

<p align="center">
  <video src="https://github.com/user-attachments/assets/f02bc1ec-c178-425a-92de-f73af1e862a9" autoplay loop muted playsinline width="900" poster="docs/assets/demo-poster.jpg"></video>
</p>

<p align="center">
  <sub>Recorded for real, nothing staged: red <code>go test</code> → autonomous fix → all green (28s, 2× speed) · <a href="https://golder-cli.pages.dev/en/">Website</a></sub>
</p>

## Features

- **TUI first**: run `golder` for the full-screen TUI (transcript, tool cards, status bar, arrow-key pickers); `--no-tui` falls back to the line-based REPL, and both front-ends share the exact same command surface; `golder -p "..."` runs headless for scripts and CI.
- **Built-in toolset**: file reading, patch editing, ripgrep code search, shell sessions, todo lists, web fetch, web search, and more (see [Built-in tools](#built-in-tools)).
- **Many providers**: defaults to the OpenCode gateway, with 40+ built-in gateways (OpenAI / Anthropic / OpenRouter / DeepSeek / Ollama, …) and any OpenAI-compatible endpoint.
- **Sessions and branches**: `/resume` switches sessions, `/fork` branches from any historical message, `/clone` duplicates the current session, `/tree` navigates the branch tree, `/export` `/import` round-trip session archives, and `/rewind` rolls back files and conversation together.
- **Approvals and sandboxing**: four permission modes (read-only / ask / auto / full-access); sandbox-tier calls run isolated automatically — `sandbox-exec` on macOS, `bubblewrap` (bwrap) on Linux (must be installed). The sandbox is a whitelist: reads are limited to the workspace, the system runtime and two git config files (`~/.gitconfig`, `~/.config/git/*`), so everything else under `$HOME` (shell rc files, SSH/GPG keys, `~/.config/*`, credentials, history) is unreadable; writes default to the workspace, the temp dir, and explicitly granted `writable_roots` (press `w` in the approval dialog to admit one path for the session, or `/permissions writable add <path>` to persist it to `~/.config/golder/permissions.toml`; `[permissions] writable_roots` in `config.toml` seeds the same list); the network is off by default. A toolchain living under `$HOME` is re-admitted with `GOLDER_SANDBOX_READABLE`. When a denial blocks a legitimate command the model can request escalation (`require_escalated`) with a justification for review or approval; an escalation that conflicts with a should-be-contained verdict is raised to you instead of being silently re-contained.
- **Long-task support**: `/compact` context compaction, context-budget tools, autonomous `/goal` runs, `/btw` side questions, sub-agent dispatch, persistent memory and `/dream` consolidation.
- **Per-provider proxy routing**: `/proxy` selects which providers use a proxy, independently; unselected providers connect directly and never inherit the shell's `HTTP_PROXY` / `HTTPS_PROXY`.
- **Skills and templates**: skills under `~/.agents/skills` are registered as `/commands` (none ship built-in; an absent directory means zero extra commands); prompt templates and project `AGENTS.md` load automatically.

## Install

### One-liner (Linux / macOS)

```bash
curl -fsSL https://golder-cli.pages.dev/install.sh | sh
```

The script is served from the site mirror (more reliable from mainland China) and verifies the archive's sha256; raw fallback: <https://raw.githubusercontent.com/getan/golder/master/install.sh>.

`GOLDER_VERSION` picks a version and `GOLDER_INSTALL_DIR` sets the install directory (default `/usr/local/bin`, falling back to `~/.local/bin` when not writable). On Windows, download the `.zip` from [Releases](https://github.com/getan/golder/releases).

### Build from source

Requires Go 1.27 or newer:

```bash
git clone https://github.com/getan/golder.git
cd golder
go build ./cmd/golder      # produces ./golder
go install ./cmd/golder    # installs to $GOPATH/bin
```

## Configuration

### API keys at a glance (important)

| Purpose | Environment variable | Notes |
|---------|----------------------|-------|
| Default gateway | `OPENCODE_API_KEY` | The `opencode-go` subscription gateway (default provider) |
| Zen gateway | `OPENCODE_ZEN_API_KEY` | `opencode-zen`, billed separately with its own key |
| Web search (first) | `TAVILY_API_KEY` | Tavily, LLM-optimized results |
| Web search (second) | `EXA_API_KEY` | Exa, returns highlight snippets |
| Other gateways | `<PROVIDER>_API_KEY` | e.g. `DEEPSEEK_API_KEY`, `XAI_API_KEY`; full list in `golder --help` |

Web search picks a backend automatically in the order **Tavily > Exa > DuckDuckGo**: when neither key is set it falls back to keyless DuckDuckGo (which needs a proxy from mainland China).

### OpenCode (default)

golder defaults to the `opencode-go` gateway with model `deepseek-v4.1-flash` and thinking level `max`. Set the key and go:

```bash
export OPENCODE_API_KEY=...
golder
```

If your network cannot reach `opencode.ai` directly, point golder at a proxy; **the proxy exit does not have to be in the US** — any working route is fine:

```bash
export GOLDER_PROXY=http://127.0.0.1:7897
```

#### Muse / Grok regional and opt-in requirements (important)

- **Only `muse-spark-*` and `grok-*` require a US egress IP**; other models served through the gateway (DeepSeek, Qwen, MiniMax, GLM, Kimi, …) connect directly with no proxy needed.
- **Muse Contributor models require the "allow data sharing to improve the model" opt-in**, otherwise calls fail with `This model collects data used to improve its quality and requires explicit opt in`. The toggle lives on your OpenCode workspace's Go page:

  ```text
  https://opencode.ai/auth                                       # sign in
  https://opencode.ai/workspace/<your-workspace-id>/go           # enable the data-sharing toggle
  https://opencode.ai/docs/go/                                   # Go plan models and pricing
  https://dev.meta.ai/docs/pricing-rate-limits#contributor-tier  # Meta contributor-tier terms
  ```

- golder automatically uses the Responses protocol for Muse models; no manual setting needed.

Calling Muse through a US-exit proxy:

```bash
export GOLDER_PROXY=http://127.0.0.1:7897    # node must be a US exit
golder -m muse-spark-1.3-contributor
```

### Proxy routing (/proxy)

`/proxy` opens an interactive picker: arrow keys select a provider, Enter toggles and saves, Esc exits. Or type it directly:

```text
/proxy url http://127.0.0.1:7897
/proxy openai on
/proxy anthropic on
/proxy deepseek off
```

The proxy address and the provider selection are independent. `GOLDER_PROXY` overrides the saved address but does not proxy every provider; unselected providers connect directly and never inherit `HTTP_PROXY` / `HTTPS_PROXY`. Enabling a provider without an address warns you to configure one. Until the first explicit selection is saved, OpenCode's historical default holds: a configured address routes the OpenCode gateways.

The selection is saved in `~/.config/golder/proxy.toml` (honors `XDG_CONFIG_HOME`), applies immediately to chat and model-listing requests, and survives restarts:

```toml
url = "http://127.0.0.1:7897"
providers = ["openai", "anthropic"]
```

### OpenAI relays

Set the relay address via `OPENAI_BASE_URL`; whether it goes through the proxy is controlled separately by `/proxy`:

```bash
export OPENAI_BASE_URL=https://relay.example.com/v1
golder --provider openai
```

Chat uses that address, model discovery requests `/models` under the same base, and the cache is keyed by the effective address. `/provider` and `--help` list each provider's API-key and `*_BASE_URL` variable names; the picker's detail pane shows the effective address, its config source, and proxy status. `BASE_URL` is optional — unset means the provider's official default.

### Config file

The global config lives at `~/.config/golder/config.toml`, with keys mirroring the CLI flags (flags win when both are set):

```toml
provider = "opencode-go"
model = "deepseek-v4.1-flash"
thinking_level = "max"            # off | minimal | low | medium | high | xhigh | max
# base_url = "https://..."        # override the provider's default endpoint
# api_key = "..."                 # prefer an env var or a credential reference
# credential = "my-key"           # references an entry in ~/.golder/.credentials.yaml
# [permissions]
# writable_roots = ["~/.cache/go-build", "/Volumes/KIOXIA/rust-target"]  # extra sandbox read+write roots
```

Environment variables override file values: `GOLDER_MODEL`, `GOLDER_PROVIDER`, `GOLDER_THINKING_LEVEL`, `GOLDER_PERMISSIONS`; CLI flags take highest precedence. A project can also carry `.golder/config.json` overriding `model` / `provider` / `thinkingLevel` / `hooks` — loaded only when the directory is trusted.

### Other providers

Select any built-in gateway with `--provider <name> -m <model>`; API keys are read from the `<PROVIDER>_API_KEY` convention (e.g. `DEEPSEEK_API_KEY`, `ZAI_API_KEY`). The full list is in `golder --help`. For custom endpoints, specify protocol and address directly:

```bash
golder --provider deepseek -m deepseek-v4-flash -p "explain this code"
golder -P openai -u https://my-gateway.example.com/v1 -m my-model -k "$MY_KEY" -p "..."
golder -m ollama/qwen2.5-coder -u http://localhost:11434/v1 -p "..."   # local Ollama
```

### Models and reasoning levels

`/model` lists the current gateway's models and annotates the reasoning levels each supports; in the TUI, picking a model flows into a level picker (Esc cancels the whole switch), and `/model <n|id> <level>` does both in one step; `/think <level>` changes only the current level.

`/provider` lists every built-in gateway with the environment variables it needs and whether one is already configured (ready ones first); `/provider <name>` switches to it using the gateway's default model. In the TUI, `/provider` is an arrow-key picker.

Level metadata comes from models.dev (a gateway's own `/models` only lists ids). Both the model list and the level table are cached on disk in `~/.golder` for 24 hours, so each gateway is queried about once a day rather than once per session.

## Built-in tools

Tools are rooted at the working directory; `--no-tools` disables them all, and `--allowed-tools` / `--disallowed-tools` provide tool-level admission (deny wins, sub-agents inherit, `--approve` cannot widen it).

| Tool | Description |
|------|-------------|
| `read` | Read a text file by path, with offset/limit, line numbers in the output |
| `view_image` | Attach a local image (PNG/JPEG/GIF/WebP, ≤8MiB) to the model as an image block |
| `apply_patch` | Add/update/move/delete multiple files in one patch call, returning a per-file diff |
| `grep` / `find` | ripgrep-backed content search / filename lookup, skipping `.gitignore`d and binary files |
| `bash` | Run shell commands with streaming output; a command that outlives the wait window becomes a session with a `bash_id`; `tty=true` for interactive programs |
| `write_stdin` | Poll a bash session, write input, or send an interrupt (`\u0003`) |
| `todo` | Structured task list (pending / in_progress / completed) |
| `webfetch` / `websearch` | Fetch a page as Markdown / search the web (backend auto-selected: Tavily / Exa / DuckDuckGo) |
| `task` | Dispatch a sub-agent (inherits the parent's tool boundary) |
| `memory_search` | Search persistent memory (BM25 full-text index) |
| `get_context_remaining` / `new_context` | Check the remaining context budget / request a fresh context window |
| `schedule_create` / `schedule_list` / `schedule_delete` | Session-local reminders that return to the conversation when due (not persisted) |

During a `/goal` run, two extra goal-control tools (`goal_complete` / `goal_blocked`) are mounted, visible only inside the autonomous loop.

## Slash commands

Typing `/` in the TUI opens a categorised menu (arrow keys to pick); `/help` prints the same grouped list. The TUI and the REPL expose exactly the same commands.

| Category | Commands |
|----------|----------|
| General | `/help` `/status` `/exit` |
| Session | `/resume` `/compact` `/fork` `/clone` `/tree` `/rewind` `/export` `/import` |
| Model | `/model` `/provider` `/proxy` `/think` |
| Memory | `/memory` `/dream` |
| Permissions | `/permissions` `/trust` |
| Modes | `/goal` `/btw` `/remote-control` |
| Extensions | Skills, plugins and prompt templates, tagged by source |

Notes:

- `/compact` immediately summarizes the context once; `/status` includes the session id, message count, creation time, and context usage (the former `/session` is folded in).
- `/resume`, `/fork`, `/tree` and `/rewind` are arrow-key pickers in the TUI and numbered selections in the REPL.
- Every skill directory under `~/.agents/skills` adds one `/command`; a fresh machine without that directory shows an empty Extensions group. Use `GOLDER_SKILLS_DIR` to relocate it or `--no-skills` to disable it entirely.

## Usage

```bash
# Interactive TUI (no -p and stdout is a terminal)
golder

# Headless: print only the final answer text
golder -p "read the README and summarize this repo"

# Line-delimited JSON events (the first event carries session_id) for scripts
golder -p "list all Go files" --output-format stream-json

# Resume the most recent session (or switch sessions with /resume)
golder --continue
golder --resume <session-id>
golder -l                     # list all sessions

# Session export / self-update
golder session export <session-id> --format md --output talk.md
golder update
```

### Permission modes

Approval for mutating tools (`bash` / `apply_patch`) is governed by four modes, starting at `auto`; switch at runtime with `/permissions`, or set `--permissions <mode>` / `GOLDER_PERMISSIONS`:

| Mode | Behavior |
|------|----------|
| `read-only` | Read-only; mutating calls are refused |
| `ask` | The current model reviews each mutating call: low risk passes, everything else asks you (denied when there is no input) |
| `auto` (default) | The model reviews every mutating call: low risk passes, sandbox-tier calls are isolated, high risk is denied — each with a one-line reason |
| `full-access` | No review, no sandbox; only the static hard-deny list (`sudo`, `rm -rf /`, …) remains |

The first launch in a directory asks whether to trust it; `--approve` grants one run up front. For OS-level isolation see [docs/sandboxing.md](docs/sandboxing.md).

### Common flags

| Flag | Description |
|------|-------------|
| `-p, --print` | The prompt for headless mode (positional args work too) |
| `-m, --model` / `--provider` | Choose model and gateway |
| `-a, --approve` | Trust the working directory for this run, skipping per-call confirmation |
| `--thinking-level` | Reasoning level: `off` / `low` / `medium` / `high` / `xhigh` / `max` |
| `--no-tools`, `--allowed-tools`, `--disallowed-tools` | Tool-level admission control |
| `--no-skills`, `--no-prompt-templates` | Disable skill / prompt-template discovery |
| `--no-tui` | Use the line-based REPL instead of the full-screen TUI |

See `golder --help` for the complete list.

## Directories and environment variables

| Path / variable | Purpose |
|-----------------|---------|
| `~/.config/golder/config.toml` | Global configuration |
| `~/.config/golder/proxy.toml` | Proxy address and provider selection saved by `/proxy` |
| `~/.golder/sessions` | Session storage (JSONL) |
| `~/.golder/.credentials.yaml` | Named API-key credentials (0600 recommended) |
| `~/.agents/skills` | Skills directory (registered as `/commands`; absent means no extension commands) |
| `GOLDER_HOME` | Override the `~/.golder` base directory |
| `GOLDER_SKILLS_DIR` | Override the skills discovery directory (default `~/.agents/skills`) |
| `GOLDER_PROXY` | Proxy address for selected providers; takes precedence over the `/proxy`-saved address |
| `GOLDER_SANDBOX` | Sandbox-tier execution: `off` / `auto` (default, sandbox tier only) / `enforce` (every bash command sandboxed, fail-closed without a runner) |
| `GOLDER_SANDBOX_NETWORK` | Network inside the sandbox: off by default (egress must be granted explicitly); set to `on` to allow |
| `GOLDER_SANDBOX_READABLE` | Extra sandbox read roots (colon/comma-separated absolute paths), e.g. `/Volumes/KIOXIA:$HOME/miniconda3` |
| `OPENAI_BASE_URL` and other `<PROVIDER>_BASE_URL` | Override a provider's chat and model-listing endpoint |
| `TAVILY_API_KEY` / `EXA_API_KEY` | Web-search backend credentials (Tavily / Exa) |
| `~/.golder/model-catalog.json`, `reasoning-catalog.json` | 24h caches of the model list / reasoning levels |
| `OPENCODE_API_KEY`, `OPENCODE_ZEN_API_KEY` and other `<PROVIDER>_API_KEY` | Provider API keys (`opencode-go` uses `OPENCODE_API_KEY`; `opencode-zen` only `OPENCODE_ZEN_API_KEY`) |

## License

[MIT](LICENSE)
