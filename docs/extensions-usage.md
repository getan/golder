# Extensions：CLI、MCP、兼容插件与 Skills

Golder 的 `/extensions` 是统一管理入口，不是新的执行协议。

| 标签 | 说明 |
|---|---|
| CLI | 普通命令的声明式工具包装，调用时才执行，不需要连接 |
| MCP · stdio / http | 标准 MCP 客户端，支持 tools、prompts、resources |
| RPC · legacy | 兼容 golder 原有的自定义 stdio JSON-RPC 插件 |
| Skill | 知识和流程；保留原来的 frontmatter、按需读取与 `/skill-name` |

本功能不包含飞书集成、第三方工具安装器、市场或 MCP server。宿主和演示程序均为 Go，
不需要 Node。外部命令由用户安装，golder 不自动执行 npm/npx 或下载运行时。

## 先用一个不需要凭证的 Go MCP 服务验证

在仓库目录构建：

```sh
GOWORK=off go build -o .build/golder ./cmd/golder
GOWORK=off go build -o .build/demo-mcp ./examples/extensions/demo-mcp

./.build/golder mcp add demo --transport stdio -- "$PWD/.build/demo-mcp"
./.build/golder mcp inspect demo
./.build/golder mcp enable demo --yes
./.build/golder mcp test demo
./.build/golder mcp tools demo
```

`add` 默认禁用，不运行代码。`enable` 记住这份执行配置的信任并保存启用状态；
`test` 才启动进程、握手和发现能力，但不调用业务工具。`--yes` 是管理者明确批准，
不是模型的工具参数。交互 CLI 不加 `--yes` 时要求输入 `yes`。

信任与目录缓存存储在 `$GOLDER_HOME/extensions-state.json`，0600；配置在
`$GOLDER_HOME/config.toml`。指纹包括工作区、配置、实际可执行文件及 manifest。
换工作区、更新二进制或修改启动包络之后，需要重新检查并批准。

已保存目录可以在新会话中无 I/O 地声明工具；实际调用先经过 gate，再按需连接并
校验工具定义。完全未知的 MCP server 需要先通过 `test` 或 TUI 的 Connect 探测。

## CLI 与 REPL

```text
golder extensions list|doctor [--json]
golder plugin add <manifest-or-executable> [--id <id>]
golder plugin list|inspect|tools|logs|enable|disable|remove|test <id>
golder mcp add <id> --transport stdio -- <program> [args...]
golder mcp add <id> --transport http --url <url>
golder mcp list|inspect|tools|logs|enable|disable|remove|test <id>
```

CLI 改配置影响未来启动，不控制已经运行的另一份 golder；不启动额外管理 daemon。
`list` / `inspect` / `doctor` / `tools` 不启动第三方程序，不联网。
JSON 查询结果不混入普通日志，错误在 stderr 返回 `extension_error`。

当前会话使用：

```text
/extensions
/plugin
/mcp
/skills
/mcp inspect demo
/mcp connect demo
/mcp reconnect demo
/mcp refresh demo
/mcp tools demo
/mcp prompts demo
/mcp resources demo
/mcp logs demo
/mcp disable demo
/mcp enable demo --yes
```

slash 的 enable/disable 默认只影响本会话；`--save` 才记住信任并修改配置。
本会话批准不会因为后续目录缓存写入而变成永久批准。
MCP prompts 用 `/mcp prompt <id> <name> [JSON-string-arguments]` 预览；
resources 用 `/mcp resource <id> <uri>` 预览。预览不自动交给模型。

## TUI

`/extensions` 显示 Tool sources 与 Skills 两个分区；`/plugin`、`/mcp`、`/skills`
是同一个 picker 的过滤视图。Enter 打开详情，Esc 关闭。

- CLI：工具、Schema、启动配置、日志和禁用，没有连接按钮。
- MCP：工具、prompts、resources、连接/重连、刷新和日志。
- Legacy RPC：协议版本、工具、命令列表、元信息事件订阅和重启。
- Skill：来源、描述、预览、带参数使用、当前会话启用/禁用，没有连接状态。

来源详情把 enabled、trust、runtime、authentication 分开。`configured` 仅表示配置了
环境/认证资料，不声称服务器已经认证成功。工具数区分可用、策略阻止和无效 Schema。
服务端 read-only hint 只作提示，不表示 golder 已授权。

新来源先打开 Review，显示执行程序/参数、manifest 或 URL 及网络上限，再明确批准。
耗时连接由可取消的后台操作完成。运行中可观察及禁用，其他变更先结束当前 run。
禁用立即撤销旧工具对象的执行资格；对已经发出的操作只尝试取消，不承诺撤回。

MCP Prompt/Resource 在界面中先预览；选择 Attach to next message 只填入输入框，
仍需用户发送。不会自动把 server instructions 或全部 prompts 注入系统提示。

## 声明式 CLI 插件

`golder-plugin.toml`：

```toml
[plugin]
id = "local-tools"
version = "1.0.0"
skills = "skills" # 可选，相对于 manifest

[[tools]]
name = "echo"
description = "Echo a text argument."
command = ["/opt/tools/echo-json", "--text", "{{text}}"]
input_schema = '{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}'
```

参数必须独占 argv 元素，不拼 shell，不允许替换 executable。独立 argv 不等于
目标程序不会解析选项；manifest 作者仍应遵守目标 CLI 的 `--` 等规则。
每次调用执行一个进程，输出有界、超时/取消杀进程组，失败不会自动重放。

包提供的 Skills 标记 Provided by；禁用整个包会撤下其贡献的 Skills。独立 Skill
优先，不会被包删除。禁用一个 Skill 不改变工具权限。`--no-skills` 仍禁用技能发现。

## RPC 兼容 SDK 与 Agent SDK

`extension` 提供协议无关的 ToolSpec、Call、Result、Progress、ContentBlock。
`extension/plugin` 提供原有 v1 协议的 `Serve` / `ServeIO`；示例在
`examples/extensions/demo-plugin`。新跨宿主工具优先写标准 MCP，而不是扩展旧协议。

原插件目录中的可执行文件只发现、不再自动启动。需要注册并批准。元信息事件
不会包含对话正文、工具参数或工具结果正文。为避免绕过门禁，旧插件动态 slash
commands 本阶段只展示，不直接执行；可以把需要的操作暴露为受控工具。

嵌入 `agent` SDK 时使用 `agent.WithExtensions()` 显式选择已批准的用户来源。
默认 SDK 不继承外部来源；启用后调用使用非交互 gate，无 reviewer/认证时失败关闭。
编译期 `agent.Tool` / `WithCustomTools` 保持原用法，不要求安装插件。

## 安全与当前边界

- 启用来源不等于批准每次业务操作。所有模型工具调用仍经过注册、Schema 校验和 gate。
- `--allowed-tools` / `--disallowed-tools` 也约束刷新后发现的工具；deny 优先。
- 默认顺序调用，不因扩展自报只读而自动并行或放行。
- 本地进程使用清洗环境、私有 HOME 和 OS 沙箱；沙箱不可用时不裸启动。
- 网络为批准的固定启动上限，不能从工具参数中提高；HTTP 每次业务调用也需要网络授权。
- HTTP 仅 HTTPS 或回环 HTTP，不跟随重定向；凭证通过环境或 `${credential:name}` 引用。
- 无效 Schema 单工具隔离，外部 Schema 不允许远程取定义；不做 permissive 回退。
- 不重放失败调用。连接中断/超时可能发生在远端写入之后，必须先核实结果。
- 当前不加载项目声明，不自动 OAuth，不支持 sampling/elicitation，也不做 MCP server。
- 子 Agent 暂不自动继承外部来源，以免扩大父会话权限。

完整设计与取舍见 [extensions-mcp.md](extensions-mcp.md)。
