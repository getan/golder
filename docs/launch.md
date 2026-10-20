# Launch notes

Current state: **v1.4.0**, Homebrew tap live, README refreshed, CI green.

The goal of the first launch is not stars. It is ten people who install it,
three who report a real problem, and one who opens a useful issue.

## One-liner

> golder is a terminal AI coding agent written in Go: one static binary,
> sandboxed tools by default, and secrets redacted before they reach the model.

## Show HN

**Title**

```
Show HN: golder – a terminal coding agent in Go with sandboxed tools
```

**First comment (post it yourself)**

```
Author here. golder is a terminal coding agent written in Go.

What is different:
- one static binary; no Node, Python or package-manager runtime
- approved shell calls run under seatbelt (macOS) or bubblewrap (Linux)
- tool output, prompts, compaction and memory are scanned in-process, and
  credential-shaped values are replaced before the provider sees them
- TUI, line REPL and headless mode share one tool surface
- 19 conversation gateways plus any OpenAI-compatible endpoint

Install: brew install getan/tap/golder
Repo: https://github.com/getan/golder

I would especially like feedback on the sandbox UX and redaction false
positives. Happy to answer anything about the Go implementation.
```

Post Tuesday–Thursday, 8–10am US Eastern. Stay in the thread for the day.

## X / Twitter

**1/3**

```
golder v1.4.0 is out: a terminal coding agent in Go.

One binary. Sandboxed tools. 19 gateways. Secrets redacted before they reach
the model.

brew install getan/tap/golder
```

Attach the 14-second GIF (`docs/assets/demo.gif`).

**2/3**

```
Why another coding agent?

- no Node/Python runtime: one static binary
- seatbelt/bwrap by default, not as an opt-in
- redaction is in-process: no proxy, no sidecar
- TUI, REPL and -p share the same commands
```

**3/3**

```
Full demo + docs: https://golder-cli.pages.dev/?utm_source=x&utm_medium=social

If you try it, tell me what broke. Issue reports are more useful than stars.
```

## V2EX

**Node:** 分享创造

**Title**

```
[分享] golder：用 Go 写的终端 AI 编码助手，默认沙箱 + 本地密钥脱敏
```

**Body**

```
写了一个终端里的 AI 编码助手，Go 单二进制，MIT。

三个和常见方案不太一样的地方：

1. 单二进制：不需要 Node/Python，brew 或 install.sh 装完就能跑
2. 默认沙箱：获准的 shell 调用走 sandbox-exec / bubblewrap，读限工作区，
   网络默认关闭
3. 本地凭据脱敏：tool output、prompt、compaction、memory 在发给模型前扫描，
   key 换成占位符；只有写文件时才还原

安装：
  brew install getan/tap/golder
  或 curl -fsSL https://golder-cli.pages.dev/install.sh | sh

官网（含 26 秒真实会话录像）：
https://golder-cli.pages.dev/?utm_source=v2ex&utm_medium=social

想听听大家对沙箱粒度和误报率的意见，特别是平时会 cat .env / env 的场景。
```

## Reddit

### r/golang

**Title**

```
I built a terminal coding agent in Go: one binary, seatbelt/bwrap sandbox, no Node runtime
```

Focus the body on architecture, not promotion:

- why the loop is a small state machine instead of a framework
- tool execution: parallel batches, per-call approval, sandbox argv construction
- redaction: Aho-Corasick anchors + per-rule regex + entropy, not 20 full scans
- what you would do differently

Ask for criticism of the design. Link the repo once.

### r/commandline

**Title**

```
golder: a TUI-first coding agent that also works as a plain CLI
```

Lead with the workflow: TUI, `-p`, stream-json, sessions, `/rewind`.

Only post to `r/LocalLLaMA` after confirming the Ollama path end to end.

## Lobsters

**Title**

```
golder: a terminal AI coding agent in Go with sandboxed tools
```

Tags: `go`, `ai`, `cli`. Use the technical angle, not the launch wording.

## Seven-day schedule

| Day | Action |
|---|---|
| 1 | X thread + V2EX + 掘金; answer every reply |
| 2 | Show HN (Tue–Thu, 8–10am ET) + Lobsters |
| 3 | r/golang; fix the first real issues |
| 4 | r/commandline; submit to Go Weekly / Golang Weekly / Console.dev |
| 5 | awesome-go / awesome-ai-agents / awesome-cli-apps PRs |
| 6–7 | Write the redaction deep-dive; publish on dev.to + 掘金 + 知乎 |

Do not post the same link to five subreddits on the same day.

## Measurement

- GitHub → Insights → Traffic → **Referrers**: which channel actually sends people
- GoatCounter (set `PUBLIC_GOATCOUNTER` in the site repo): site visits and paths
- Star History: trend, not absolute numbers
- Release download count and Homebrew installs
- Issue quality: a concrete bug report beats ten stars

Use UTM links for every off-GitHub channel:

```
https://golder-cli.pages.dev/?utm_source=hn&utm_medium=social
https://golder-cli.pages.dev/?utm_source=reddit&utm_medium=social
https://golder-cli.pages.dev/?utm_source=v2ex&utm_medium=social
```

## Avoid

- buying stars or asking for stars in the first sentence
- mass-posting the same text to unrelated communities
- claiming "secure" without linking the sandbox and redaction docs
- arguing with HN criticism; answer the technical point and move on
