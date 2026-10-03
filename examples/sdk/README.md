# golder SDK examples · golder SDK 示例

Runnable examples for embedding a **golder** coding agent in your own Go program
via the public [`github.com/getan/golder/agent`](../../agent) package.

本目录是将 **golder** 编码智能体嵌入到你自己的 Go 程序中的可运行示例，使用公共
[`github.com/getan/golder/agent`](../../agent) 包。

> Issue #554

---

## Why a dedicated SDK package · 为什么单独提供 SDK 包

All of golder's implementation lives under `internal/`, which Go forbids other
modules from importing. The `agent` package is the supported, importable surface:
every exported type is a Go primitive (`string`, `[]string`, `bool`, `func`), so
your code never depends on a golder internal type and golder can evolve its internals
without breaking you.

golder 的实现代码全部位于 `internal/` 下，Go 禁止其他模块导入。`agent` 包是官方支持、
可被外部导入的接口层：所有导出类型都是 Go 基本类型（`string`、`[]string`、`bool`、
`func`），因此你的代码不会依赖 golder 的任何内部类型，golder 也可以在不破坏你的前提下
演进其内部实现。

## Install · 安装

```go
import "github.com/getan/golder/agent"
```

## Run · 运行

Set the API key for your provider, then run any example. 先为你的服务商设置 API
Key，再运行任意示例：

```sh
export ANTHROPIC_API_KEY=sk-...
go run ./examples/sdk/01-minimal
```

## Examples · 示例列表

| # | Directory · 目录 | Shows · 演示内容 |
|---|---|---|
| 01 | [`01-minimal`](01-minimal) | Smallest program: one prompt, one reply. · 最小程序：一次提问、一次回答。 |
| 02 | [`02-streaming`](02-streaming) | Stream the reply token-by-token with a callback. · 通过回调逐块流式输出回复。 |
| 03 | [`03-model-thinking`](03-model-thinking) | Choose a model and set the reasoning-effort level. · 选择模型并设置推理强度（thinking）等级。 |
| 04 | [`04-system-prompt`](04-system-prompt) | Customize behavior with a system prompt. · 用系统提示词定制智能体行为。 |
| 05 | [`05-tools`](05-tools) | Allowlist / denylist tools; inspect the set. · 工具白名单/黑名单，并查看生效的工具集。 |
| 06 | [`06-conversation`](06-conversation) | Multi-turn memory in one session; `Reset`. · 单会话内的多轮记忆；以及 `Reset`。 |
| 07 | [`07-provider`](07-provider) | Target a custom / named provider endpoint. · 指向自定义或具名的服务商端点。 |

## Minimal usage · 最小用法

```go
sess, err := agent.New(
    agent.WithModel("claude-opus-4-8"),
    agent.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
)
if err != nil {
    log.Fatal(err)
}
defer sess.Close()

reply, err := sess.Prompt(context.Background(), "Say hello in one word.")
fmt.Println(reply)
```

## ⚠️ Tools run automatically · 工具会自动执行

By default a session has golder's full built-in tool set and executes tool calls
**without any confirmation prompt** (equivalent to the CLI's `--approve`). The
agent can read, modify, and delete files under its working directory and run
shell commands. Only send prompts you trust, and run where you are willing to let
the agent make changes. Constrain it with `WithTools` (allowlist),
`WithDisallowedTools` (denylist — always wins), or `WithoutTools` (no tools).

默认情况下，会话拥有 golder 的全部内置工具，并且**不经任何确认**就执行工具调用
（等价于 CLI 的 `--approve`）。智能体可以读取、修改、删除其工作目录下的文件，
并执行 shell 命令。请只发送你信任的提示词，并在你允许智能体做出改动的目录下运行。
可用 `WithTools`（白名单）、`WithDisallowedTools`（黑名单，始终优先生效）或
`WithoutTools`（不启用任何工具）来加以约束。

## Defaults · 默认值

| Aspect · 方面 | Default · 默认 | Override · 覆盖方式 |
|---|---|---|
| Tools · 工具 | on, auto-executed · 开启并自动执行 | `WithTools` / `WithDisallowedTools` / `WithoutTools` |
| Skills · 技能 | off · 关闭 | `WithSkills` |
| Memory · 记忆 | off · 关闭 | `WithMemory` |
| Thinking · 推理强度 | `medium` | `WithThinkingLevel` |

Skills and memory are off by default so an embedded session is hermetic — it does
not read or write the machine's shared golder state unless you opt in.

技能与记忆默认关闭，从而让嵌入式会话保持“隔离”——除非你显式开启，否则不会读写本机
共享的 golder 状态。

## API reference · API 参考

Full docs: `go doc github.com/getan/golder/agent`. 完整文档见
`go doc github.com/getan/golder/agent`。

- `New(opts ...Option) (*Session, error)` — build a session; validates provider, tools, and thinking level up front, no network call. · 构建会话；预先校验服务商、工具与推理等级，不发起网络请求。
- `(*Session) Prompt(ctx, prompt) (string, error)` — one turn, returns final text. · 单轮对话，返回最终文本。
- `(*Session) Stream(ctx, prompt, onText) (string, error)` — same, with incremental output. · 同上，但支持增量输出。
- `(*Session) Reset()` — clear conversation history. · 清空对话历史。
- `(*Session) ToolNames() []string` / `Model()` / `Provider()` — introspection. · 内省查询。
- `(*Session) Close() error` — release resources. · 释放资源。

A `Session` keeps conversation state across calls and is **not** safe for
concurrent use — drive it from one goroutine, or create one per goroutine.

`Session` 会跨调用保留对话状态，且**不是**并发安全的——请在单个 goroutine 中使用，
或为每个 goroutine 创建各自的会话。
