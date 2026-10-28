# 用一个扩展中心管理 CLI、MCP、兼容插件和 Skills

Status: Accepted; core implementation and usage guide included
Last updated: 2026-10-27
Discussion: 本次插件 / MCP API 与 UI 设计讨论；本文件替代最初的飞书接入草案。

## Abstract / 摘要

Golder 以统一的 Extension Service 管理执行型来源，以独立的 Skill catalog
保留现有技能发现与按需加载。UI 统一入口，不统一底层协议。CLI 用声明式 argv；
MCP 使用官方纯 Go SDK；现有自定义 stdio RPC 只作为兼容插件。
所有业务调用复用工具注册、参数验证和现有授权链。启用、信任、连接、单次授权是
四件不同的事。本轮不实现飞书或任何其他业务集成，不依赖 Node。

## Background / 背景

现有 `internal/plugin` 已实现 initialize、tools/call、commands/call、event、
shutdown；它不是 MCP。原来的 Discover 会运行插件目录中的所有可执行文件，
`run.ToolRegistry` 还会忽略注册错误。这些行为不适合新的扩展管理边界。
现有 Skills 已支持 `/skill-name`、disable-model-invocation 和正文按需读取；
不应为了插件体系重写这套机制。

## Design / 设计

### 1. 一套管理，两类目录，三种执行方式

```text
CLI / TUI / Agent SDK
          │
   Extension Service
          ├── Source catalog + Policy + Lifecycle
          │        ├── CLI adapter → 普通 CLI（按需执行）
          │        ├── Legacy RPC adapter → 现有插件进程
          │        └── MCP adapter → 官方 Go SDK → stdio / Streamable HTTP
          └── Skill catalog → 现有 Skills + 扩展包贡献的 Skills
```

| UI 类型 | 能力 | 生命周期 |
|---|---|---|
| CLI | 工具、附带的 Skills | enabled / on demand，不显示 connected |
| MCP · stdio / HTTP | tools、prompts、resources | disconnected / connecting / ready / failed |
| RPC · legacy | 工具、命令、事件 | 兼容现有 wire protocol，不建设第二个通用工具生态 |
| Skill | 操作知识和流程 | available / manual only / disabled / invalid |

Skill 依赖工具不是工具授权；附带脚本仍须经过普通执行工具的门禁。
MCP Prompt 不是 Skill，Resource 也不是 Skill。

### 2. 公共 API 不暴露 internal 或 MCP SDK 类型

- `extension`：SourceRef、ToolSpec、Call、Result、ContentBlock、Progress、诊断模型。
- `extension/plugin`：Spec、Tool handler、Command、Event handler、Serve。
- `internal/extensions`：配置、信任、受控进程、adapter、版本化快照、运行时包装。
- `agent.Tool` 保留；编译期自定义工具不需要改成插件。

```go
type SourceRef struct { Kind, ID string }
type ToolSpec struct {
    Name, Description string
    InputSchema, OutputSchema json.RawMessage
    Hints ToolHints // 声明，不是授权
}
type Call struct { ID, Tool string; Arguments json.RawMessage }
type Result struct {
    Content []ContentBlock
    StructuredContent json.RawMessage
    IsError bool
}
type ToolHandler func(context.Context, Call, func(Progress)) (Result, error)
```

业务失败使用 IsError，基础设施失败使用 error。取消不承诺撤销远端副作用。
外部结果不能授予权限、改系统提示或终止整个宿主会话。
公共插件 SDK 提供 Serve(ctx, Spec)，不提供 Host.Exec/ReadCredentials 等万能后门。
RPC v1 保持 string content，SDK 在边界适配；新工具优先采用标准 MCP。

宿主管理支持 List / Inspect / Connect / Disconnect / Refresh / Catalog / Close。
管理不是模型工具。网络操作异步执行；UI 不接触裸 RPC client。
不提供绕过 gate 的 UI “直接调用工具”按钮。

### 3. 来源身份、展示名和模型名字分离

- 来源：`plugin:demo`、`mcp:team-docs`。
- 原始工具名：保留 server 返回值，不通过 sanitize 后的名字反向猜测。
- 模型名：`plg__demo__echo`、`mcp__team_docs__search`，最多 64 字符；
  变形或截断可能冲突时附稳定 hash，不依赖发现顺序。
- 显示名：`team-docs / search`，显示 CLI/MCP/legacy 类型标签。
- 禁止覆盖内置工具；无效 schema 单工具隔离；外部 schema 不解析远程引用。

目录快照携带 revision；同一轮的模型声明和执行目录保持一致。刷新在轮次边界
发布；禁用立即撤销执行资格，旧工具对象也不能继续调用。重连不重放业务调用。
传输中断后写操作结果未知必须明确报告，不显示为“未执行”。

### 4. 配置声明不等于执行

用户配置位于 `$GOLDER_HOME/config.toml`。新来源默认 disabled。

```toml
[plugins.demo]
enabled = false
runtime = "rpc"
command = "/opt/tools/demo-plugin"
args = []

[plugins.local-tools]
enabled = false
runtime = "cli"
manifest = "/opt/tools/local-tools/golder-plugin.toml"

[mcp.servers.team-docs]
enabled = false
transport = "stdio"
command = "/opt/tools/team-docs-mcp"
args = ["serve"]
allowed_tools = ["search", "fetch"]
call_timeout = "60s"
network = false

[mcp.servers.remote]
enabled = false
transport = "http"
url = "https://example.com/mcp"

[mcp.servers.remote.headers]
Authorization = "${credential:remote-auth-header}"
```

信任单独存在保护的 state root 中，绑定执行配置指纹，不因 enabled 改变而失效；
命令、参数、manifest 内容、工作目录、凭证引用、网络包络或 URL 改变需重新批准。
进程使用 scrubbed env 和 OS 沙箱；启动包络固定，不能被一次工具批准永久拓宽。
不支持沙箱的平台/环境不能静默回退裸执行。HTTP 仅 HTTPS，回环开发地址可 HTTP；
不跟随重定向，认证引用不回显。日志有界、脱敏、过滤终端控制字符。

配置编辑保留无关表和注释，写前验证、原子落盘、0600。项目内扩展配置本轮不自动
加载；不会因为 clone 仓库或工作区 trust 就执行第三方程序。

CLI manifest 使用显式 JSON Schema 和 argv 元素占位符，不做 shell 模板：

```toml
[plugin]
id = "demo"
version = "1.0.0"
skills = "skills" # 可选，相对 manifest 目录，不得逃逸

[[tools]]
name = "echo"
description = "Echo a text argument."
command = ["/opt/tools/echo-json", "--text", "{{text}}"]
input_schema = '{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}'
```

占位符必须占据整个 argv 元素，替换值不能改变可执行文件；不用 `sh -c`。
独立 argv 防止 shell 注入，不承诺目标 CLI 不把 `-` 开头的值解析为选项。
清单作者应使用目标 CLI 的 `--` 或 `--flag=value` 等明确约定。

### 5. 发现不启动，首次探测显式执行

list / inspect / doctor 仅检查配置和已保存目录，不启动代码，不联网。
test / connect 才会启动第三方代码、握手和发现能力，必须已批准。
保存的目录绑定配置指纹；新会话可从有效缓存声明工具，首次调用经 gate 后按需连接。
连接后再次比对 Schema；发生变化时不执行旧版本调用，刷新后让模型重试。
这避免在 first-run workspace trust 之前从 SetupEnv 执行第三方程序。

状态分别记录 configured enabled、trust、runtime、authentication；不把“有 token”
显示成“已认证成功”。数量区分 discovered / available / policy blocked / invalid。

### 6. MCP 只消费协商支持的能力

使用 `github.com/modelcontextprotocol/go-sdk`，锁定版本。支持 tools、prompts、
resources、progress、取消、目录变更；不声明 sampling、elicitation 或自动 OAuth。
未知协议版本拒绝，不猜测降级。Roots 不是沙箱，不自动公开 home。
server instructions 不自动注入系统提示。annotations 只作提示，不作为自动批准。
输出保留 text、image、structuredContent，其他媒体/二进制为有界占位。
Prompts/Resources 经用户选择读取、预览；只有用户明确附加/使用才进入对话。
不把所有 MCP prompts 注册到根 slash 菜单。

### 7. CLI 和 slash 使用同一管理语义

```text
golder extensions list|doctor [--json]
golder plugin list|inspect|tools|logs|enable|disable|remove|test <id>
golder plugin add <manifest-or-executable> [--id <id>]
golder mcp list|inspect|tools|logs|enable|disable|remove|test <id>
golder mcp add <id> --transport stdio -- <program> [args...]
golder mcp add <id> --transport http --url <url>
```

enable 新信任需要确认，自动化需显式 --yes；test 不调用业务工具，但启动本身有风险。
CLI 修改影响之后启动；不创建跨进程控制 daemon。当前会话使用 slash/TUI。

```text
/extensions    总览
/plugin        CLI + legacy 过滤视图
/mcp           MCP 过滤视图
/skills        技能目录
/mcp tools <id>、/mcp prompts <id>、/mcp resources <id>
/mcp connect|disconnect|refresh|logs|disable <id>
```

查询命令支持 JSON；失败为结构化 code/message，不混入机器输出。

### 8. TUI 复用 picker，来源和 Skills 分区

```text
Extensions
  Tool sources
  › local-tools     [CLI]           enabled       4 tools
    team-docs       [MCP · stdio]   disconnected  8 / 24 tools
    old-audit       [RPC · legacy]  running       1 command
  Skills
    code-review     [Skill]         available     user
    release-flow    [Skill]         manual only   user
```

Enter 打开详情；类型标签和文字状态始终存在，不只依赖颜色。
CLI：工具、命令映射、启动包络、禁用；没有 reconnect。
MCP：工具、prompts、resources、连接/重连、日志、禁用。
legacy：旧协议版本、命令、事件订阅、重启、日志、禁用。
Skill：来源、说明、预览、使用；没有 connect/restart。
包提供 Skills 时显示 Provided by；禁用包撤下其贡献，不删除独立 Skill。
运行中管理只允许观察和撤销，连接/刷新等变更等当前 run 结束。
耗时操作通过 tea.Cmd 回传，不能卡 UI；大 Schema/日志限长展示。

调用继续用现有工具事件卡片，名字和描述携带来源；失败区分 not executed、
business error 和 outcome unknown。Skill 使用只在明确调用时记录，不猜测归因。

## Rationale / 取舍

- 不自写 MCP wire 栈：纯 Go 不等于重做标准实现；SDK 藏在 adapter 后。
- 不扩展 legacy 为第三套生态：已有插件兼容，新通用工具优先 MCP。
- 不让 Skill 实现进程接口：知识目录和执行目录生命周期不同。
- 不创建市场/自动下载/飞书功能：先验证平台契约。
- 不把协议自报 read-only 变成权限：扩展可撒谎，现有门禁仍是唯一执行授权入口。

## Compatibility / 兼容性

现有插件 wire 保留，但自动执行扫描的行为主动收紧：目录中可执行文件仅列出为
未批准 legacy 来源，需显式注册、启用。动态插件命令不能绕过门禁直接 RPC，
应作为受控工具调用或在本阶段拒绝执行并说明原因。默认事件不广播正文。
Skills 的目录、frontmatter、按需读取和 `/skill-name` 行为保持兼容。
子 Agent 不自行加载更宽配置，本阶段不自动继承外部来源；这是保守子集。

## Implementation / 交付与验收

1. 公共类型和 SDK；配置、信任、命名、Schema 校验、目录快照。
2. CLI / legacy / MCP adapters；受控进程、缓存与取消、CLI 管理。
3. runtime/executor 集成；TUI/REPL 统一管理，Skills 展示和包归属。
4. fake Go 插件与 MCP server 测试；回归 --no-tools、--no-skills、工具策略和权限模式。

验收覆盖：发现无执行、配置变化撤销信任、禁用旧对象不可执行、Schema 不出网、
命名稳定、失败隔离、无副作用重放、日志脱敏、窄屏、UI 异步、Skill 不授予工具权限。
尚未落地的能力必须在最终交付说明中明确列出，不能把设计状态当实现状态。

### 当前实现对应关系

| 设计项 | 实现位置 / 状态 |
|---|---|
| 公共类型、legacy 插件 SDK | `extension`、`extension/plugin`；提供纯 Go 示例 |
| 配置、指纹信任、目录缓存、受控进程 | `internal/extensions`；session approval 与 remembered approval 分开保存 |
| CLI / legacy / MCP adapters | CLI argv、legacy v1、官方 Go SDK v1.8.0（stdio + HTTP） |
| 启动和轮次接线 | SetupEnv 只发现；runtime SyncTools 在轮次边界发布完整目录 |
| CLI / REPL / TUI | `extensioncmd` 共享命令；TUI picker 分工具来源和 Skills，耗时操作异步 |
| Skills | 保留现有加载和 slash，增加包归属、预览和会话禁用，不授予工具权限 |
| Legacy events | 有界、best-effort 元信息事件；不投递正文、参数和结果内容 |
| Legacy dynamic commands | 只显示声明；不直接执行，避免旧 slash 回调绕过 gate |
| Agent SDK | `agent.WithExtensions()` 显式 opt-in，默认不继承机器上的外部来源 |
| 项目声明、自动 OAuth、sampling / elicitation、MCP server | 本轮不提供 |

模型工具名发生字符变形或截断时会带稳定 hash；精确允许列表以 `tools` 输出中的
`model_name` 为准，不从展示名猜测 wire name。完整操作示例见
[extensions-usage.md](extensions-usage.md)。
