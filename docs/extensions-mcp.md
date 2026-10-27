# Golder 扩展体系设计：CLI 插件、原生插件与 MCP

> 状态：设计稿（未实现）
>
> 目标读者：golder 维护者；需要接入 lark-cli、内部 CLI、第三方 MCP server 的扩展作者。
>
> 结论先行：**lark-cli 用“声明式 CLI 插件 + Skills”接入，不经过 MCP；MCP 作为独立的纯 Go 客户端能力接入第三方生态。** 两者共用一套扩展生命周期、工具命名、权限门禁和诊断界面，但不强行合并协议。

---

## 0. 结论

| 需求 | 选择 | 原因 |
| --- | --- | --- |
| lark / 飞书 | CLI 插件 + lark-cli 自带 Skills | lark-cli 本身就是 Go 写的 CLI，官方维护 200+ 命令和 26 个 Agent Skills；没有 MCP server |
| 第三方工具生态 | MCP client（stdio + Streamable HTTP） | MCP 是跨厂商标准，但只需要“消费”能力，不需要 golder 去当 server |
| 深度集成（生命周期事件、slash command、通知） | 原生进程插件 v1/v2 | MCP 没有 golder 的事件模型；现有协议已经能表达 |
| 知识/操作手册 | Skills | 不执行代码，只教模型怎么用工具，和 CLI 插件天然互补 |
| Node 依赖 | 不引入 | MCP client 用 Go 标准库；lark-cli 可直接 `go install` 或源码构建，npm 只是安装器 |
| WASM / `.so` 插件 | 不做 | 跨平台、ABI、生命周期成本高；进程边界已经足够 |

一句话原则：**按执行边界分层，不按流行协议分层。** CLI 就是 CLI，MCP 就是 MCP，原生插件就是原生插件；对上层只暴露统一的 `AgentTool` / `Command` / `Event`。

---

## 1. 现状

### 1.1 golder 已有的扩展点

| 能力 | 位置 | 现状 |
| --- | --- | --- |
| 工具接口 | `internal/agentcore/tool.go` | `AgentTool`：Name / Description / Schema / ExecutionMode / Execute |
| 工具注册 | `internal/agenttool/registry.go` | 启动时静态注册，编译 JSON Schema，按名字查重 |
| 原生插件 | `internal/plugin` | 每个可执行文件一个插件，stdio JSON-RPC，`initialize` 返回 Manifest，支持 tools / commands / events |
| JSON-RPC 传输 | `internal/jsonrpc` | 只实现 client 方向；忽略 server 发来的 request/notification |
| 插件命令 | `internal/cli/prompts/registry.go` | 注册成 slash command，调用 `Plugin.CallCommand` |
| 插件事件 | `internal/plugin/events.go` | Agent 生命周期事件，fire-and-forget |
| 工具边界 | `internal/cli/run/toolpolicy.go` | `--allowed-tools` / `--disallowed-tools`，精确名字匹配 |
| 权限门禁 | `internal/cli/run/gates.go` + `internal/judge` | trust / workspace / sandbox / 权限模式 / LLM reviewer |
| 环境清洗 | `internal/provider/credenv.go` | `ScrubEnv` 去掉 credential-shaped 变量和 `GOLDER_*` |
| 插件目录 | `internal/golderhome/golderhome.go` | `$GOLDER_HOME/plugins` |
| 配置 | `internal/cli/config/config.go` | `config.toml`，已有 `[memory]` / `[checkpoint]` / `[compaction]` / `[redact]` 等表 |

### 1.2 现有插件协议的问题

`internal/plugin` 的设计是“可执行文件 + 自定义 JSON-RPC”，对 golder 内部很好用，但作为通用扩展协议有四个缺口：

1. **不可复用**：wire types 在 `internal/plugin`，外部 Go 插件无法 import，只能抄类型。
2. **单向 RPC**：`internal/jsonrpc` 忽略 server → client 的请求和 notification，因此无法支持 MCP 的 `tools/list_changed`、progress、sampling、elicitation。
3. **没有启用/信任模型**：`plugin.Discover` 会把目录里所有可执行文件直接拉起来；没有 `enabled` 列表，也没有项目级审批。
4. **名字冲突被静默吞掉**：`run.ToolRegistry` 对 `Register` 错误是 `_ =`，插件工具和内置工具重名时后注册者直接消失，用户看不到错误。

这些不是要推翻现有插件，而是要在它旁边补一层“扩展管理”，并把 `jsonrpc` 补成双向 peer。

### 1.3 lark-cli 是什么（关键事实）

`larksuite/cli`（`lark-cli`）不是 Node 工具，而是 **Go 写的官方 CLI**：

- 语言：Go（仓库根 `go.mod`，`main.go`），MIT。
- 安装：官方推荐 `npx @larksuite/cli@latest install`，但 npm 只是安装器/包装；也可以 `go install github.com/larksuite/cli@latest` 或源码 `make install`。**运行时不依赖 Node。**
- 能力：Messenger、Docs、Base、Sheets、Slides、Calendar、Mail、Tasks、Meetings、Wiki、Contact、Approval、OKR 等；三层命令体系（`+shortcut` / API command / raw `api`）。
- Agent 设计：26 个 Skills，`--format json` 的成功/错误 envelope，`--dry-run`，`schema` 自省，错误提示会告诉 Agent 缺什么权限。
- 认证：`lark-cli config init` + `lark-cli auth login`，token 存在 OS keychain；golder 不需要接触 Lark token。
- **没有 MCP server**：官方 MCP 是另一个 Node 项目（`lark-openapi-mcp`）。CLI 自己的扩展机制是 Go 的 **in-process plugin SDK**（`extension/platform`），用于把 lark-cli 嵌进你自己的二进制，不是 MCP，也不是进程插件。

因此，“把 lark 接进 golder”最合适的路径不是 MCP，而是：

1. 把 lark-cli 的高频命令声明成 golder 工具（CLI 插件）；
2. 把 lark-cli 的 Skills 放进 golder 的 skill 发现路径，教模型长尾用法；
3. 需要时让模型用 `bash` + `lark-cli` 作为兜底；
4. 完全不引入 Node。

### 1.4 tuios-mini 的插件模型（对比）

tuios-mini 支持插件，但它是 **herdr 插件宿主**，不是 MCP 客户端：

- 插件 = 一个目录 + `herdr-plugin.toml`，兼容 herdr 0.9.3 格式。
- 清单是声明式的：`[[startup]]`、`[[actions]]`、`[[events]]`、`[[panes]]`、`[[link_handlers]]`、`[[build]]`，每个命令是 argv 数组。
- 宿主是 daemon：子进程脱离 pane 运行，输出有上限（64 KiB）、日志保留 200 条、全局最多 32 个并发、单插件 8 个、默认 10 分钟超时、超时杀进程组。
- 发现不等于执行：只有 `[plugins] enabled` 里的 id 才运行；发现插件不运行任何代码。
- 信任边界：`enable` / `disable` / `link` / `unlink` 必须从 tuios pane 外执行；新增插件要等 `config apply` 或重启，移除立即生效；插件以用户权限运行，回调走 herdr socket，默认 pane 权限。
- 没有 `.so`、WASM、进程内 ABI，也没有 MCP client。
- `tuios mcp` 曾经存在（MCP **server**，把 tuios 暴露给别的 harness），但在 commit `ef1c3abc`（M2 “prune the browser-only surface”）里连同 `internal/mcp` 一起删除了；README / docs / e2e 还留着引用，属于文档漂移。

对 golder 的启示：

1. **声明式 CLI 插件是被验证过的模型**，尤其适合 lark 这类“命令本身就是产品”的工具。
2. **发现 ≠ 启用** 必须抄；这是插件安全的第一道边界。
3. **MCP server 是另一个方向**，tuios-mini 已经用删除证明了它的维护成本；golder v1 不做 server。

---

## 2. 目标与非目标

### 2.1 目标

- 一个统一的扩展管理层，聚合三类来源：CLI 插件、原生进程插件、MCP server。
- CLI 插件：零代码接入任意 CLI，声明式 manifest，argv 级参数渲染，无 shell。
- MCP：纯 Go client，支持 stdio 与 Streamable HTTP，支持 tools / prompts / resources / progress / cancellation / list_changed。
- 所有外部工具都经过同一套：名字命名空间、Schema 校验、工具策略、权限门禁、超时/输出上限、诊断。
- 配置集中在 `config.toml`，支持启用列表、环境变量白名单、credential 引用、项目级审批。
- 先交付 lark 可用路径，再交付 MCP。

### 2.2 非目标

- 不做 MCP server（`golder mcp serve`）。
- 不做 WASM / `.so` / Go plugin 动态加载。
- 不把原生插件协议改成 MCP；两者并存。
- 不在 v1 支持 MCP sampling / elicitation / completions。
- 不为 lark 自己实现 OAuth；认证完全交给 lark-cli。
- 不自动安装/更新第三方 MCP server 或 CLI。
- 不做“MCP 工具市场”。

---

## 3. 总体架构

### 3.1 四层扩展

```
┌─────────────────────────────────────────────────────────────┐
│ golder runtime                                              │
│  AgentContext.Tools / SlashRegistry / EventNotifier          │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│ internal/extensions.Manager                                 │
│  - 统一发现、启动、关闭、状态、刷新                          │
│  - 工具命名空间、策略过滤、冲突检测                          │
└───────┬───────────────────┬───────────────────┬─────────────┘
        │                   │                   │
┌───────▼────────┐  ┌───────▼────────┐  ┌───────▼─────────────┐
│ CLI plugin     │  │ Native plugin  │  │ MCP client          │
│ (golder-       │  │ (JSON-RPC      │  │ (stdio / HTTP)      │
│  plugin.toml)  │  │  over stdio)   │  │                     │
└───────┬────────┘  └───────┬────────┘  └───────┬─────────────┘
        │                   │                   │
   exec.CommandContext   jsonrpc.Peer         jsonrpc.Peer
   (one-shot CLI)        (long-lived)         (long-lived)
```

第五层是 **Skills**：不执行，只提供知识。lark-cli 的 Skills 和 CLI 工具是互补关系，不是替代关系。

### 3.2 为什么不把所有东西都做成 MCP

| 维度 | MCP | 原生插件 | CLI 插件 |
| --- | --- | --- | --- |
| 工具调用 | 有 | 有 | 有 |
| slash command | prompts（语义不同） | 有 | 可加 |
| Agent 生命周期事件 | 无 | 有 | 无 |
| 长驻进程/流式 | 有 | 有 | 无（一次性） |
| 接入既有 CLI | 需要写 MCP wrapper | 需要写插件 | 直接声明 |
| 任意语言 | 有 SDK | JSON-RPC | 任意 CLI |
| 安全边界 | 进程 | 进程 | 每次 exec |

MCP 适合“别人已经写好的 server”；CLI 插件适合“已经有一个好 CLI”；原生插件适合“需要深度嵌入 golder 生命周期”。强行统一会让三者都变差。

### 3.3 统一模型

新增 `internal/extensions`，只做聚合，不实现具体协议：

```go
package extensions

type Kind string

const (
    KindCLI    Kind = "cli"
    KindNative Kind = "native"
    KindMCP    Kind = "mcp"
)

// Source 是一个已加载的扩展来源。实现必须并发安全。
type Source interface {
    ID() string
    Kind() Kind
    Tools() []agentcore.AgentTool
    Commands() []Command
    Status() Status
    Close() error
}

// Command 是统一的 slash command 处理器，屏蔽 native/MCP prompt 的差异。
type Command struct {
    Name        string
    Description string
    ArgumentHint string
    SourceID    string
    Invoke      func(ctx context.Context, args json.RawMessage) (CommandResult, error)
}

type CommandResult struct {
    Prompt        string
    Notifications []Notification
}

type Manager struct {
    sources []Source
    filter  func(toolName string) bool // --allowed-tools / --disallowed-tools
}

func (m *Manager) Tools() []agentcore.AgentTool
func (m *Manager) Commands() []Command
func (m *Manager) Refresh(ctx context.Context) (changed bool)
func (m *Manager) Close() error
```

现有 `plugin.Manager` 保留为 native source 的实现细节；`run.Env` 增加 `Extensions *extensions.Manager`，同时保留 `Env.Plugins` 给事件通知器（或用 `Extensions.Native()` 取）。迁移分两步：

1. 先让 `extensions.Manager` 包装现有 `plugin.Manager`，前端不改。
2. 再把 `prompts.RegisterPluginCommands` 改成 `prompts.RegisterCommands(reg, extensions)`，统一处理 plugin / MCP prompts。

---

## 4. CLI 插件

### 4.1 目录布局

复用 `$GOLDER_HOME/plugins`，但用目录区分两类：

```
$GOLDER_HOME/plugins/
  my-native-plugin              # 可执行文件，现有 native 插件，保持兼容
  lark/
    golder-plugin.toml          # CLI 插件清单
    skills/                     # 可选：随插件分发的 Skills
      lark-im/SKILL.md
```

- 现有 `plugin.Discover` 只看目录下的可执行文件，会跳过子目录，所以 `lark/` 不会干扰 native 插件。
- 新增 `cliplugin.Discover` 只看子目录里的 `golder-plugin.toml`。
- 额外目录：`[plugins] dirs = [...]`，支持 `~` 展开。
- 项目级：`.golder/plugins/<id>/golder-plugin.toml`，需要项目信任 + 一次性批准（见 §8.4）。

### 4.2 Manifest

设计目标：**用参数声明生成 JSON Schema 和 argv，不写模板字符串，不走 shell。**

```toml
[plugin]
id = "lark"
name = "Lark/Feishu CLI"
version = "0.1.0"
description = "Official Lark/Feishu CLI, exposed as golder tools."
command = "lark-cli"                 # 可执行文件；用于 doctor / PATH 检查
timeout = "60s"                      # 默认工具超时
max_output_bytes = 262144            # stdout 上限
env_passthrough = ["HOME", "PATH", "TMPDIR", "LANG", "LC_*", "XDG_*"]
working_dir = "workspace"            # workspace | home | plugin
skills = "skills"                    # 可选：相对目录，加入 skill 发现

[[tools]]
name = "lark_im_send"
description = "Send a text message to a Lark chat. Only use when the user explicitly asks to send."
command = ["lark-cli", "im", "+messages-send"]
read_only = false
timeout = "30s"

[tools.args.chat_id]
flag = "--chat-id"
type = "string"
required = true
description = "Chat ID, usually oc_..."

[tools.args.text]
flag = "--text"
type = "string"
required = true
description = "Message text."

[[tools]]
name = "lark_docs_fetch"
description = "Fetch a Lark document as Markdown."
command = ["lark-cli", "docs", "+fetch"]
read_only = true

[tools.args.doc]
flag = "--doc"
type = "string"
required = true
description = "Document token or URL."

[tools.args.format]
flag = "--format"
type = "string"
enum = ["pretty", "json", "markdown"]
default = "pretty"
```

生成的 JSON Schema（示意）：

```json
{
  "type": "object",
  "properties": {
    "chat_id": {"type": "string", "description": "Chat ID, usually oc_..."},
    "text": {"type": "string", "description": "Message text."}
  },
  "required": ["chat_id", "text"],
  "additionalProperties": false
}
```

### 4.3 参数渲染规则

每个 `tools.args.<name>` 支持：

| 字段 | 含义 |
| --- | --- |
| `flag` | 对应的命令行开关，如 `--chat-id`；省略则为位置参数 |
| `position` | 位置参数序号（从 1 开始），与 `flag` 二选一 |
| `type` | `string` / `integer` / `number` / `boolean` / `array` / `object` |
| `required` | 是否必填 |
| `default` | 缺省值 |
| `enum` | 枚举 |
| `format` | `json` 时把值 marshal 成紧凑 JSON，作为单个 argv |
| `repeat` | 数组时每个元素重复一次 flag（默认 true） |
| `omit_if_false` | 布尔 false 时省略（默认 true） |

渲染规则：

- 只做 argv 追加，**绝不拼 shell**；`exec.CommandContext(program, argv...)`。
- 字符串原样作为单个 argv，哪怕以 `-` 开头也不会被当成开关。
- 布尔 true → 追加 flag（如果配置了 flag），false → 省略。
- 数组 → 默认重复 flag；`repeat = false` 时用逗号连接（仅显式配置）。
- `format = "json"` → `json.Marshal(value)` 后作为一个 argv。
- 缺失 required / 类型不符 / enum 不匹配 → 在 exec 前返回字段级校验错误，不启动进程。
- 复杂场景用 `raw_argv` 兜底（见下），但仍是 argv 数组，不是 shell 字符串。

`raw_argv` 示例：

```toml
[[tools]]
name = "lark_api"
description = "Raw Lark OpenAPI call. Disabled by default."
command = ["lark-cli", "api"]
raw_argv = ["{{method}}", "{{path}}", "--params", "{{params_json}}"]
enabled = false
```

`raw_argv` 的占位符只能替换成单个 argv 元素；不做求值、不做管道、不做重定向。

### 4.4 执行、输出、错误

- 每次工具调用一个进程：`exec.CommandContext`，超时用 context + 进程组 kill。
- 工作目录默认 workspace；`working_dir = "home"` 时用 `$HOME`；`"plugin"` 用清单目录。
- 环境：`provider.ScrubEnv(os.Environ(), allow)`，`allow` 来自 `env_passthrough` + manifest `env` + 运行时注入的 credential 引用。
- stdout：最多 `max_output_bytes`，超出截断并附 `[output truncated at N bytes]`；继续 drain 防止子进程阻塞。
- stderr：保留尾部 4 KiB，成功后不返回；失败时附在错误里。
- 退出码：0 → 成功；非 0 → `IsError = true`，content 包含 stdout + stderr 尾部 + exit code。
- `output = "json"`：如果 stdout 是合法 JSON，压缩后返回；非法 JSON 原样返回并标注。
- `read_only = true` → `ToolExecutionParallel`；否则 `ToolExecutionSequential`。
- 工具结果永远不把 stderr 的完整内容塞进上下文；只保留尾部。

### 4.5 lark 工具面

第一阶段只做 10–15 个高频、边界清晰的工具，不做 200 个命令的镜像：

| 工具 | lark-cli 命令（示意） | 读/写 |
| --- | --- | --- |
| `lark_auth_status` | `lark-cli auth status` | 读 |
| `lark_im_send` | `lark-cli im +messages-send --chat-id ... --text ...` | 写 |
| `lark_im_search` | `lark-cli im +messages-search --query ...` | 读 |
| `lark_docs_create` | `lark-cli docs +create --title ... --markdown ...` | 写 |
| `lark_docs_fetch` | `lark-cli docs +fetch --doc ... --format pretty` | 读 |
| `lark_docs_update` | `lark-cli docs +update --doc ... --mode append --markdown ...` | 写 |
| `lark_sheets_read` | `lark-cli sheets +read --spreadsheet-token ...` | 读 |
| `lark_sheets_write` | `lark-cli sheets +write ...` | 写 |
| `lark_calendar_agenda` | `lark-cli calendar +agenda` | 读 |
| `lark_calendar_create` | `lark-cli calendar +create ...` | 写 |
| `lark_task_create` | `lark-cli task +create ...` | 写 |
| `lark_task_list` | `lark-cli task +list ...` | 读 |
| `lark_wiki_search` | `lark-cli wiki +search ...` | 读 |

具体子命令以 `lark-cli <domain> --help` 和 `lark-cli schema` 为准；manifest 是用户可编辑的，不把命令名硬编码进 golder。

`lark_api`（raw API）默认 `enabled = false`。它能力太强，应该由用户显式打开，并且每次调用都走审批。

### 4.6 Skills 协同

- 清单里的 `skills = "skills"` 指向插件目录下的 skill 树，扩展管理器把其中的 `SKILL.md` 注册到 `runtime.Skill` 发现结果里。
- 也可以让用户把 lark-cli 官方 skills 安装到 `~/.agents/skills`，golder 已经能发现。
- Skill 负责“怎么拼参数、有哪些坑、权限怎么补”；CLI 工具负责“可校验、可门禁、可审计的执行”。
- 系统提示里给一行摘要：`Lark/Feishu: lark_docs_fetch, lark_im_send, ... (run /mcp for status)`，不把 26 个 skill 全文塞进 prompt。

### 4.7 认证

- golder 不读取、不存储、不转发 Lark token。
- 用户先运行 `lark-cli config init` / `lark-cli auth login`；golder 只调用 `lark-cli auth status` 检查。
- `lark_auth_status` 未登录时返回可操作错误：`lark-cli is not logged in: run lark-cli auth login --recommend`。
- 如果用户拒绝 Node，连安装都不想要：`go install github.com/larksuite/cli@latest`，或源码 `make install`。

---

## 5. MCP 客户端（纯 Go）

### 5.1 传输

#### stdio

- `exec.CommandContext(command, args...)`，stdin/stdout 是 MCP 消息，stderr 是日志。
- MCP stdio 使用 **newline-delimited JSON**，不是 LSP 的 `Content-Length` 帧。
- 读循环：`bufio.Scanner`，单行上限 16 MiB；空行跳过；非法 JSON 行记录并跳过（可能是 server 的普通日志写到了 stdout，属于 server bug）。
- 写：`json.Marshal` + `\n`，用 mutex 串行化。
- 进程组：Unix 下 `Setpgid`，Close/超时杀整个进程组；Windows 用 `Process.Kill`。
- 子进程环境：`provider.ScrubEnv` + 配置的 `env` / `env_passthrough`。

#### Streamable HTTP

- `POST` JSON-RPC 到配置的 `url`；`Accept: application/json, text/event-stream`。
- 响应可能是 `application/json` 单条，也可能是 `text/event-stream`（SSE）。
- SSE 解析：自己写一个小 parser（`event:` / `data:` / `id:` / 空行分帧），不引第三方依赖。
- `Mcp-Session-Id`：从响应头保存，后续请求带上；404 表示 session 失效，重新 initialize。
- `MCP-Protocol-Version`：initialize 之后后续请求带上协商版本。
- GET 用于 server → client 流（可选）；DELETE 用于终止 session（best-effort）。
- 只允许 `https://`；`http://` 仅允许 localhost / 127.0.0.1 / ::1（本地开发）。
- 重定向：不跟随跨 origin 重定向，避免 Authorization 头泄漏。
- 静态 header 从配置/env/credential 引用解析；不把 header 写日志。
- OAuth 2.1 + PKCE 放 phase 3；v1 只支持静态 bearer token。

### 5.2 握手与能力

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "initialize",
  "params": {
    "protocolVersion": "2025-06-18",
    "capabilities": {
      "roots": {"listChanged": false}
    },
    "clientInfo": {"name": "golder", "version": "..."}
  }
}
```

- 支持版本：`2025-06-18`、`2025-03-26`、`2024-11-05`；server 返回未知版本时记录警告，按最高兼容版本继续。
- 收到 result 后发 `notifications/initialized`。
- 记录 `serverInfo`、`capabilities`、`instructions`；`instructions` 只用于 `/mcp info`，不自动注入系统提示。
- v1 **不声明** sampling / elicitation / completions；server 发来这些请求时返回 `-32601 Method not found`，并记录一次诊断。
- `roots/list` 返回 workspace root（`file://<cwd>`），绝不返回 `$HOME`；`roots` capability 只在配置 `roots = true` 时声明（默认 true）。
- `ping` 返回 `{}`。

### 5.3 工具发现与命名

- `tools/list` 支持 cursor 分页，直到 `nextCursor` 为空。
- 缓存 `tools/list` 结果；`notifications/tools/list_changed` 标记 dirty，在下一个 turn 边界刷新（见 §9.2）。
- 工具名：`mcp__<server>__<tool>`，例如 `mcp__github__create_issue`。
  - server slug：小写，非 `[a-z0-9_]` 替换为 `_`，最长 16 字符。
  - tool 部分：同样 sanitize。
  - 总长超过 64（OpenAI function name 限制）时，截断 + 8 位 hash。
  - 保留 `displayName -> wireName` 映射；重名时追加 hash 并告警。
- 描述：`[MCP server: github] ` 前缀 + 原始描述 + 可选 annotations 摘要；总长上限 4 KiB，控制字符剥离。
- Schema：
  - 原样交给 `agenttool.ToolRegistry` 编译；失败时降级为 `{"type":"object"}`，工具仍可用，但记录警告。
  - 根不是 object 时包一层 `{"type":"object","properties":{"value":<原 schema>}}`？**不做**：MCP 工具参数按规范应是 object；不是则记录并降级为 permissive。
  - 去掉 `$schema` / `$id` 等 provider 不认的顶层字段；保留 `$defs` / `$ref`（Anthropic/OpenAI 支持，Gemini 的 sanitizer 会处理）。
  - schema 大小上限 256 KiB；超过则降级。
- 执行模式：默认 `ToolExecutionSequential`。MCP annotations 是 **server 声明**，不可信，不能据此自动批准；`parallel = true` 的 server 配置才允许标注 `readOnlyHint` 的工具并行。

### 5.4 调用与结果映射

`tools/call`：

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "tools/call",
  "params": {
    "name": "create_issue",
    "arguments": {...},
    "_meta": {"progressToken": "call_abc"}
  }
}
```

结果映射：

| MCP content | golder |
| --- | --- |
| `text` | `TextContent` |
| `image` | `ImageContent`（base64 + mime）；超过 5 MiB 或 provider 不支持图片 → 占位文本 |
| `audio` | 占位文本 `[audio: mime, N bytes]`（agentcore 无 audio block） |
| `resource` text | `TextContent`，带 `[resource uri]` 头 |
| `resource` blob | 占位文本 + 大小；不把 base64 塞上下文 |
| `resource_link` | `TextContent`，带 URI/name |
| `structuredContent` | 追加一个 `[structuredContent]` JSON 文本块（压缩、限长） |
| `isError: true` | `AgentToolResult.IsError = true` |

其它：

- `content` 为空 → `(no content)`。
- 总输出上限 `max_output_bytes`（默认 1 MiB），超出截断并附说明。
- JSON-RPC error → `IsError = true`，文本包含 server / tool / code / message；不把 `data` 原样塞入（可能很大）。
- 传输错误 → `IsError = true`，附“server crashed / connection lost”提示；不自动重放非幂等调用。
- `progress` notification → `onUpdate`，节流到 10/s；partial 内容是 text。
- context cancel → 发 `notifications/cancelled {requestId, reason}`，然后返回取消错误；server 可能继续执行，记录警告。

### 5.5 生命周期

- 启动时并行 initialize 所有启用的 server，单 server 超时 `startup_timeout`（默认 15s）。
- 一个 server 失败不影响其它 server，也不阻塞 golder 启动；状态记 `failed`，`/mcp` 可见。
- 工具调用时如果连接已死：尝试一次重连（指数退避，最多 3 次，冷却 30s）；仍失败则返回错误。
- `tools/list_changed` / `prompts/list_changed` / `resources/list_changed`：标记 dirty，turn 边界刷新。
- Close：关闭 stdin → 等 2s → 杀进程组；HTTP 发 DELETE（best-effort）。
- stderr：独立 goroutine 读，环形缓冲 64 KiB，脱敏后写 diagtrace；`/mcp logs <server>` 可看尾部。

### 5.6 v1 不做的 MCP 能力

| 能力 | v1 | 原因 |
| --- | --- | --- |
| sampling | 不支持 | 会消耗用户 token、泄漏对话；需要单独授权模型 |
| elicitation | 不支持 | 可被恶意 server 用来钓鱼；TUI 表单也复杂 |
| completions | 不支持 | 价值低 |
| resources 订阅 | 不支持 | 先做 list/read 工具 |
| prompts | 支持（slash command） | 成本低，收益明确 |
| OAuth 2.1 | phase 3 | 静态 token 先够用 |
| tasks（实验） | 不支持 | 规范未稳定 |

---

## 6. 原生插件 v1/v2

### 6.1 v1 保持兼容

现有 `internal/plugin` 的协议不变：

- `initialize` → Manifest
- `tools/call`
- `commands/call`
- `event` notification
- `shutdown` notification

但补三件事：

1. `internal/jsonrpc` 支持 server → client request / notification（MCP 和未来 native v2 共用）。
2. 外部 SDK：把 wire types 和 server loop 移到 `pkg/plugin`，`internal/plugin` 只保留 client/manager；插件作者不用 import `internal`。
3. 冲突检测：扩展管理器在注册前检查工具名，重复时明确报错并禁用该插件，而不是静默丢工具。

### 6.2 v2（可选）

新增字段，不破坏 v1：

```json
{
  "protocolVersion": "2",
  "name": "lark-native",
  "capabilities": {
    "tools": true,
    "commands": true,
    "events": ["agent_start", "tool_execution_end"],
    "contentBlocks": true,
    "listChanged": true
  },
  "tools": [...]
}
```

- `protocolVersion` 缺省视为 v1。
- `contentBlocks` 允许 `tools/call` 返回 text/image/resource，而不只是 string。
- `listChanged` 允许插件发 `notifications/tools/list_changed`。
- 仍然不支持 native 插件加载 `.so`；它永远是进程。

### 6.3 什么时候用 native，而不是 CLI/MCP

- 需要收到 `agent_start` / `turn_end` / `tool_execution_*` 事件并做出反应（通知、审计、状态同步）。
- 需要注册 slash command，且命令逻辑不是简单 CLI。
- 需要长驻连接/流式更新（例如 lark 事件订阅），CLI 一次性进程不合适。

---

## 7. 配置

### 7.1 `config.toml`

```toml
# 全局开关
[extensions]
enabled = true

# CLI / native 插件
[plugins]
enabled = ["lark"]                 # 只加载列出的 id；空表示不加载任何插件
dirs = ["~/.golder/plugins"]       # 额外目录

# MCP 全局默认
[mcp]
enabled = true
startup_timeout = "15s"
tool_timeout = "120s"
max_output_bytes = 1048576
roots = true

[mcp.servers.github]
transport = "stdio"                # stdio | http
command = "github-mcp-server"
args = ["stdio"]
enabled = true
env_passthrough = ["GITHUB_TOKEN"] # 显式放过；否则 ScrubEnv 会删掉
# env = { GITHUB_TOKEN = "${credential:github}" }
tool_prefix = "github"
allowed_tools = ["search_*", "get_*"]   # 服务器级 glob
disallowed_tools = ["delete_*"]
timeout = "60s"
parallel = false
sandbox = "none"                   # none | inherit（phase 3）

[mcp.servers.linear]
transport = "http"
url = "https://mcp.linear.app/mcp"
headers = { Authorization = "Bearer ${credential:linear}" }
enabled = false
```

解析规则：

- `[plugins] enabled` 一旦存在，CLI 插件只加载列出的 id；native 插件为兼容现有行为，可以继续自动加载，但输出一条 deprecation 警告（或加 `[plugins] legacy_autoload = true` 过渡）。
- `[mcp.servers.<id>]` 的 id 必须匹配 `^[a-z0-9][a-z0-9_-]*$`，最长 32。
- `${VAR}` 从环境变量解析；缺失 → 启动该 server 失败，错误里只写变量名，不写值。
- `${credential:name}` 从 `$GOLDER_HOME/.credentials.yaml` 解析（复用 `provider.ResolveCredentialReference`）。
- `env` 的值同样支持 `${...}`；解析后的值不写日志。
- 布尔/时长字段有默认值；非法值启动时报错，不静默忽略。
- 未知字段：报 warning（不 fail），便于版本兼容。

### 7.2 项目级配置（phase 3）

```
<repo>/.golder/mcp.toml
<repo>/.golder/plugins/<id>/golder-plugin.toml
```

- 只有 workspace trusted 时才加载。
- 首次加载时展示一次性批准：server 名、command/args 或 URL、工具数量、读/写标注；批准 hash 存 `trust.json`（或新建 `extensions.json`）。
- 配置变更（command/args/url 改变）→ hash 变化 → 重新批准。
- 项目级不能覆盖同名用户级 server；冲突报错。
- 项目级永远不能自动启用 native 可执行文件。

### 7.3 优先级

```
CLI flag > 项目级 > 用户级 config.toml > 默认值
```

工具边界 `--allowed-tools` / `--disallowed-tools` 在扩展加载后、注册前应用（保持现有语义）。MCP 的动态新增工具必须再次经过同一个 filter，不能因为后来 `tools/list_changed` 就绕过。

---

## 8. 安全与权限

### 8.1 信任层级

| 来源 | 信任前提 | 默认 |
| --- | --- | --- |
| 用户级 CLI 插件 | 用户在 `config.toml` 显式 `enabled` | 关闭 |
| 用户级 MCP stdio | 用户显式 `enabled` | 关闭 |
| 用户级 MCP HTTP | 用户显式 `enabled` + URL | 关闭 |
| 项目级任何扩展 | workspace trusted + 一次性批准 | 关闭 |
| native 插件（legacy） | 可执行文件在插件目录 | 兼容期自动，后续改显式 |

**发现 ≠ 执行**。扫描清单、显示列表、`plugin list` 不启动任何进程；只有 `enabled` 才启动。

### 8.2 进程环境

- 使用 `provider.ScrubEnv`：删除所有 `*_API_KEY`、`*_TOKEN`、`*_SECRET`、`*PASSWORD*`、`GOLDER_*`。
- 只放行 `env_passthrough` 明确列出的变量（支持 `LC_*` 这类尾部 glob）。
- `env` / `headers` 里的 secret 用 `${credential:...}` 引用，不写明文。
- 子进程工作目录默认 workspace，不是 `$HOME`。
- 日志、错误、diagtrace 统一走 redactor；不记录 args（可能含消息内容）、不记录 env、不记录 header。

### 8.3 MCP annotations 不是授权

MCP `ToolAnnotations`（`readOnlyHint`、`destructiveHint`、`idempotentHint`、`openWorldHint`）是 **server 自己的声明**，恶意 server 可以撒谎。因此：

- 只在审批摘要/工具描述里展示，标注 “server-declared”。
- 绝不因为 `readOnlyHint: true` 就 `Allow`。
- 默认不据此并行；只有用户配置 `parallel = true` 且 server 可信时才启用。
- `destructiveHint: true` 在审批 UI 里显示警告。
- 未知 MCP 工具仍然走现有 judge：静态 floor → LLM reviewer；没有 reviewer 就 fail closed。

### 8.4 项目级审批

项目级扩展是供应链攻击面（clone 一个仓库就带来一个 MCP server）。流程：

1. 发现 `.golder/mcp.toml` / `.golder/plugins/*/golder-plugin.toml`。
2. 若 workspace 未 trusted → 不加载，只提示。
3. 若 trusted 但 hash 未批准 → 在 TUI/REPL 显示清单，要求用户确认。
4. 批准后写 hash（sha256 of canonical config + 解析后的 command/url）。
5. 任何 command/args/url 变化 → 重新确认。
6. 非交互模式（headless / `--print`）默认拒绝项目级扩展，除非显式 `--approve-project-extensions`（新增 flag）。

### 8.5 提示注入与资源上限

- MCP tool description / prompt / resource 都是不可信文本；加来源标签，限长，剥控制字符。
- 不自动把 server `instructions` 注入系统提示。
- 工具输出有字节上限；image/audio/resource blob 有单独上限。
- schema 有大小上限；编译失败降级而不是崩溃。
- 一个 server 的失败/超时不影响其它 server；单 server 调用并发有上限（默认 4）。
- 工具名冲突、server 名冲突都在启动时明确报错，不静默覆盖。

---

## 9. 运行时接入

### 9.1 `run.SetupEnv`

现状：`SetupEnv` 在 `internal/cli/run/run.go` 里发现 native 插件，把 `m.Tools()` 追加到 `tools`，把 `m` 放进 `Env.Plugins`。

改为：

```go
var ext *extensions.Manager
if !noTools {
    ext, err = extensions.Load(extensions.LoadOptions{
        Cwd:      cwd,
        Config:   fileConfig,
        Stderr:   os.Stderr,
        Filter:   policyAllows, // 由 ToolPolicy 生成
        Trusted:  Trusted(cwd),
    })
    if err != nil { ... }       // 单个来源失败不返回整体错误
    tools = append(tools, ext.Tools()...)
}
```

然后：

- `ValidateToolPolicy(tools, policy)` 仍然在完整集合上跑。
- `ApplyToolPolicy` 仍然在注册前过滤。
- `Env.Extensions = ext`；`Env.Plugins` 保留为 `ext.Native()` 的快捷方式，或直接删掉并改前端（第二阶段）。
- `defer ext.Close()` 由 `cmd/golder/main.go` 的三个入口统一处理（现有 `defer env.Plugins.Close()` 的位置）。

### 9.2 工具注册与动态刷新

现状：`ToolRegistry` 只有 `Register`，重复名字报错，但没有 `Remove/Replace`；`run.ToolRegistry` 忽略错误。

需要：

```go
func (r *ToolRegistry) Register(tool agentcore.AgentTool) error // 已有
func (r *ToolRegistry) Remove(name string) bool
func (r *ToolRegistry) Replace(tool agentcore.AgentTool) error
func (r *ToolRegistry) Names() []string
```

- `run.ToolRegistry` 改为收集错误；启动时把失败的工具名和原因打到 stderr / diagtrace，不能静默。
- 扩展工具注册前做一次全局冲突检查（builtin + memory + task + plugins + MCP）。
- 动态刷新：`extensions.Manager.Refresh(ctx)` 返回 changed；在每个 turn 边界（`PrepareNextTurn` 或现有的 turn update 路径）：
  1. 取 `ext.Tools()`；
  2. 与当前扩展工具集合 diff；
  3. `Registry.Remove/Replace`；
  4. 用 `TurnUpdate.Tools` 把新的 `[]AgentTool` 交给 `AgentContext`。
- 如果新增工具不在 `--allowed-tools` 白名单里，直接不注册；不要让 `list_changed` 成为绕过边界的通道。
- 不在一批工具调用执行到一半时改 registry；turn 边界足够。

### 9.3 命令与 UI

- `prompts.BuildSlashRegistry` 接收 `extensions.Manager`，把 native plugin commands 和 MCP prompts 都注册成 slash command。
- MCP prompt 命名：`mcp-<server>-<prompt>`，sanitize 成 `[a-z0-9-]`；冲突时加 hash。
- MCP prompt 调用：`prompts/get` → 把 text content 拼成 prompt；image/resource 转占位或文本；返回后注入下一轮用户消息。
- 新增 slash：
  - `/mcp`：列出 server、状态、工具数、错误摘要。
  - `/mcp tools <server>`：列出工具及读/写标注。
  - `/mcp logs <server>`：stderr 环形缓冲尾部。
  - `/mcp reconnect <server>`。
  - `/plugin` / `/plugin list`：CLI/native 插件状态。
- 新增 CLI 子命令（非交互）：
  - `golder mcp list [--json]`
  - `golder mcp test <server>`
  - `golder plugin list [--json]`
  - `golder plugin test <id>`
- 启动时只打一行摘要：`golder: 2 MCP servers, 1 CLI plugin, 23 tools`；详细状态在 `/mcp`。

### 9.4 可观测性

- `diagtrace.Event("extension", "load kind=%s id=%s ok=%t duration=%s")`
- `diagtrace.Event("mcp", "call server=%s tool=%s duration=%s isError=%t bytes=%d")`（不记录参数）
- 状态快照：

```go
type Status struct {
    ID        string
    Kind      Kind
    State     string // starting | ready | failed | closed
    Tools     int
    Commands  int
    LastError string
    Version   string
}
```

- `tui-latency.md` 的 `setup`/`plugin` 阶段扩展为 `extension` 阶段；每个 source 一行。

---

## 10. 实施计划

### P0：基础修正（1–2 人日）

- `internal/jsonrpc` 增加双向 peer：server → client request、notification handler；保持现有 plugin client 行为。
- `ToolRegistry` 增加 `Remove/Replace/Names`；`run.ToolRegistry` 不再吞注册错误。
- `internal/extensions` 骨架：`Source`、`Manager`、`Status`、`Load`；先包装现有 `plugin.Manager`。
- `run.Env` 增加 `Extensions`；三个入口统一 Close。
- 测试：重复工具名、双向 RPC、handler 并发、Close 幂等。

### P1：CLI 插件 + lark（2–3 人日）

- `internal/cliplugin`：manifest 解析、目录发现、参数渲染、exec、输出上限、超时、进程组 kill。
- `plugins.lark` 清单 + 10–15 个工具 + lark skills 目录。
- `extensions.Manager` 聚合 CLI/native；`Env.Tools` 包含 lark 工具。
- `golder plugin list/test`、启动 warning、`/plugin`。
- 测试：argv 渲染（含 `-` 开头参数、数组、bool、JSON）、env 清洗、超时、非零退出、输出截断、命令注入。
- 验收：`lark_auth_status` 未登录时给可操作错误；`lark_docs_fetch` 可用；`--disallowed-tools lark_im_send` 真的移除；`--no-tools` 不启动 lark。

### P2：MCP stdio（3–4 人日）

- `internal/mcp`：protocol types、peer、stdio transport、client、tool adapter、manager。
- initialize / tools/list / tools/call / notifications / progress / cancellation / list_changed。
- 配置 `[mcp]` + `[mcp.servers.*]`；启用列表；server 级 allow/deny。
- 测试：fake MCP server（Go test helper）覆盖握手、分页、错误、崩溃、超时、取消、progress、list_changed。
- 验收：一个 fake server 的工具可用；server 崩溃只影响自己；`--allowed-tools` 过滤；启动不被坏 server 卡住。

### P3：MCP HTTP / prompts / resources / UI（3–4 人日）

- Streamable HTTP + SSE parser + session header；静态 bearer；localhost http 例外。
- prompts → slash command；resources → list/read 工具；`/mcp` 系列命令。
- 项目级 `.golder/mcp.toml` + 审批 hash。
- OAuth 2.1 + PKCE（可拆到 P4）。

### P4：native v2 + SDK + 动态刷新（可选，3–4 人日）

- `pkg/plugin` 公共 SDK；Go 示例插件。
- v2 capabilities、content blocks、list_changed。
- 动态刷新 turn 边界接线。
- native 插件命名空间/显式 enable 的迁移。

### 不建议现在做的

- `golder mcp serve`（MCP server）。
- WASM 插件。
- lark 的 Go 原生插件（除非事件订阅成为刚需）。
- 把所有 200+ lark 命令都变成工具。

---

## 11. 测试计划

### 单元

- JSON-RPC peer：请求/响应/通知/双向 request/并发/关闭。
- CLI manifest：必填、类型、enum、默认值、flag/position、数组、JSON、raw_argv。
- argv 渲染：`--text "-rf"` 仍是单个 argv；不产生 shell 元字符解释。
- env：credential-shaped 变量被删；`env_passthrough` 生效；`${credential:...}` 缺失报错。
- 输出：超限截断、stderr 尾部、非零退出、非法 JSON。
- MCP：initialize 版本协商、tools/list 分页、content 映射、isError、progress、cancel、list_changed。
- 名字：64 字符截断、hash 稳定、冲突报错。
- 安全：恶意 schema、超大 schema、恶意 description、重名 server、跨 origin redirect。

### 集成

- `internal/mcp/testserver`：一个可执行 Go 测试 server，支持脚本化的 initialize/tools/list/tools/call/notifications；测试通过 stdio 启动它。
- `internal/cliplugin/testcli`：一个 Go 测试 CLI，模拟 lark 的 `{ok, data, error}` envelope，验证成功/失败/超时/大输出。
- 端到端：在临时 `GOLDER_HOME` 放 lark manifest + fake CLI，跑 `golder --print`，断言工具调用与门禁行为。

### 回归

- 现有 native 插件测试全部保留；`plugin` 协议不变。
- `--no-tools`、`--allowed-tools`、`--disallowed-tools`、`--approve` 的行为不回归。
- 现有 `ToolRegistry` 的 schema 校验/repair 行为不变。

---

## 12. 决策记录

| ID | 决策 | 理由 |
| --- | --- | --- |
| D1 | MCP 只做 client | golder 是 agent host；server 维护成本高，tuios-mini 已删除自己的 server |
| D2 | lark 用 CLI 插件，不用 MCP | lark-cli 是 Go CLI + Skills；官方 MCP 是 Node，违背约束 |
| D3 | CLI 插件参数用声明式 args，不做 shell 模板 | 避免注入；schema/argv 都由声明生成 |
| D4 | MCP 工具名 `mcp__server__tool` | 防冲突、可读、与生态惯例一致 |
| D5 | annotations 只做提示，不做授权 | server 可撒谎；权限必须由 golder 自己判 |
| D6 | 发现 ≠ 启用；项目级要审批 | 供应链安全 |
| D7 | 动态工具在 turn 边界刷新 | 不改变正在进行的 assistant turn；实现简单 |
| D8 | native 插件协议不合并进 MCP | MCP 没有 golder 的事件/命令模型 |
| D9 | 不做 MCP server / WASM / Node | 控制复杂度与依赖 |
| D10 | lark 认证完全交给 lark-cli | golder 不碰 token；keychain 由 CLI 管理 |

---

## 13. 待确认问题

1. 工具名是否接受 `mcp__server__tool`？如果担心模型不熟悉，可改成 `server_tool`，但冲突风险更高。
2. lark 第一批到底要哪些动作？上面的 10–15 个是建议，需要按实际工作流裁剪。
3. 项目级 `.golder/mcp.toml` 是否要在 P2 就做，还是等 P3？
4. native 插件是否要在本轮改成显式 `enabled`（有兼容成本，但安全模型更一致）？
5. 是否需要 `golder mcp serve` 给别的 agent 用？（建议不做，除非有明确消费者。）

---

## 附录 A：CLI 插件完整示例（lark）

```toml
[plugin]
id = "lark"
name = "Lark/Feishu CLI"
version = "0.1.0"
description = "Official Lark/Feishu CLI exposed as golder tools."
command = "lark-cli"
timeout = "60s"
max_output_bytes = 262144
env_passthrough = ["HOME", "PATH", "TMPDIR", "LANG", "LC_*", "XDG_*"]
working_dir = "workspace"
skills = "skills"

[[tools]]
name = "lark_auth_status"
description = "Check whether lark-cli is configured and logged in."
command = ["lark-cli", "auth", "status"]
read_only = true
timeout = "15s"

[[tools]]
name = "lark_im_send"
description = "Send a text message to a Lark chat. Only use when the user explicitly asks to send."
command = ["lark-cli", "im", "+messages-send"]
read_only = false
timeout = "30s"

[tools.args.chat_id]
flag = "--chat-id"
type = "string"
required = true
description = "Chat ID, usually oc_..."

[tools.args.text]
flag = "--text"
type = "string"
required = true
description = "Message text."

[[tools]]
name = "lark_docs_fetch"
description = "Fetch a Lark document as Markdown."
command = ["lark-cli", "docs", "+fetch"]
read_only = true

[tools.args.doc]
flag = "--doc"
type = "string"
required = true
description = "Document token or URL."

[tools.args.format]
flag = "--format"
type = "string"
enum = ["pretty", "json", "markdown"]
default = "pretty"

[[tools]]
name = "lark_docs_create"
description = "Create a Lark document from Markdown."
command = ["lark-cli", "docs", "+create"]
read_only = false

[tools.args.title]
flag = "--title"
type = "string"
required = true
description = "Document title."

[tools.args.markdown]
flag = "--markdown"
type = "string"
required = true
description = "Document body in Markdown."
```

## 附录 B：MCP server 配置示例

```toml
[mcp]
enabled = true
startup_timeout = "15s"
tool_timeout = "120s"
max_output_bytes = 1048576

[mcp.servers.filesystem]
transport = "stdio"
command = "mcp-server-filesystem"
args = ["--root", "/workspace"]
enabled = true
allowed_tools = ["read_*", "list_*", "search_*"]
disallowed_tools = ["write_*", "delete_*"]
parallel = true

[mcp.servers.linear]
transport = "http"
url = "https://mcp.linear.app/mcp"
headers = { Authorization = "Bearer ${credential:linear}" }
enabled = false
tool_timeout = "60s"
```

## 附录 C：错误文案约定

| 场景 | 文案 |
| --- | --- |
| CLI 不存在 | `lark-cli not found: install it with "go install github.com/larksuite/cli@latest" or set plugins.lark.command` |
| 未登录 | `lark-cli is not logged in: run "lark-cli auth login --recommend"` |
| MCP 启动失败 | `MCP server "github" failed to start: <reason> (run /mcp logs github)` |
| MCP 调用失败 | `MCP tool github/create_issue failed: <code> <message>` |
| Schema 无效 | `MCP server "x": tool "y" schema is invalid, arguments will not be validated: <reason>` |
| 名字冲突 | `tool name collision: "mcp__x__y" is already registered by <source>` |
| 项目级未批准 | `project extension .golder/mcp.toml is not approved; run golder with a TTY to review it` |

