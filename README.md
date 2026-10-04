# golder

[![CI](https://github.com/getan/golder/actions/workflows/ci.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/ci.yml)
[![Release](https://github.com/getan/golder/actions/workflows/release.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/release.yml)

用 Go 编写的终端 AI 编码助手：读写文件、执行命令、检索代码、抓取网页，在对话中完成从理解需求到改好代码的闭环。支持交互式 TUI 与无头脚本两种运行方式。

## 特性

- **两种模式**：直接 `golder` 进入全屏 TUI；`golder -p "..."` 无头执行，适合脚本与 CI。
- **内置工具集**：文件读取、补丁编辑、代码检索、Shell 会话、任务清单、网页抓取等（见[内置工具](#内置工具)）。
- **多 Provider**：默认 OpenCode 网关，同时内置 OpenAI / Anthropic / OpenRouter / DeepSeek / Ollama 等 40+ 网关，也可指向任意 OpenAI 兼容端点。
- **会话续跑**：`--resume` / `--continue` 续跑历史会话，`-l` 列出全部会话；TUI 恢复完整对话。
- **审批与沙箱**：四档权限模式（只读 / 每次询问 / 自动审批 / 完全放行）；macOS 下沙箱档调用自动经 `sandbox-exec` 隔离执行。
- **长任务支持**：上下文自动压缩、上下文预算工具、子 Agent 派发、持久记忆。
- **技能与模板**：`~/.agents/skills` 下的技能注册为 `/命令`；提示词模板、项目级 `AGENTS.md` 自动装载。

## 安装

### 一键安装（Linux / macOS）

```bash
curl -fsSL https://raw.githubusercontent.com/getan/golder/master/install.sh | sh
```

可用 `GOLDER_VERSION` 指定版本、`GOLDER_INSTALL_DIR` 指定安装目录（默认 `/usr/local/bin`，无写权限时回退 `~/.local/bin`）。Windows 请从 [Releases](https://github.com/getan/golder/releases) 下载 `.zip` 解压。

### 从源码构建

需要 Go 1.27 或更高版本：

```bash
git clone https://github.com/getan/golder.git
cd golder
go build ./cmd/golder      # 生成 ./golder
go install ./cmd/golder    # 安装到 $GOPATH/bin
```

## 配置

### OpenCode（默认）

golder 默认走 `opencode-go` 网关，模型 `deepseek-v4.1-flash`，推理档位 `max`。只需设置 API Key：

```bash
export OPENCODE_API_KEY=...
golder
```

> `muse-spark-1.3-contributor` 也在这个网关上，但需要**美国出口 IP** 才能调用；不在美国网络时用 `GOLDER_PROXY` 指向美国节点：
>
> ```bash
> export GOLDER_PROXY=http://127.0.0.1:7897    # 代理出口需在美国
> golder -m muse-spark-1.3-contributor
> ```

如果访问 `opencode.ai` 本身就需要代理，也设置 `GOLDER_PROXY`：

```bash
export GOLDER_PROXY=http://127.0.0.1:7897
```

> `GOLDER_PROXY` 用于 opencode-zen / opencode-go 这类指定出口的网关；其余 Provider 遵循 Go 标准库的 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 环境变量。

### 配置文件

全局配置放在 `~/.config/golder/config.toml`，键名与 CLI flag 对应（命令行传入时优先）：

```toml
provider = "opencode-go"
model = "deepseek-v4.1-flash"
thinking_level = "max"            # off | minimal | low | medium | high | xhigh | max
# base_url = "https://..."        # 覆盖 Provider 默认端点
# api_key = "..."                 # 建议改用环境变量或 credential 引用
# credential = "my-key"           # 引用 ~/.golder/.credentials.yaml 中的条目
```

也可以用环境变量覆盖：`GOLDER_MODEL`、`GOLDER_PROVIDER`、`GOLDER_THINKING_LEVEL`、`GOLDER_PERMISSIONS`。命令行 flag 优先级最高。项目级还可以放一份 `.golder/config.json` 覆盖 `model` / `provider` / `thinkingLevel` / `hooks`——仅在目录被信任时加载。

### 其他 Provider

任何内置网关都可以用 `--provider <name> -m <model>` 选中，API Key 按 `<PROVIDER>_API_KEY` 约定从环境变量读取（如 `DEEPSEEK_API_KEY`、`ZAI_API_KEY`），完整列表见 `golder --help`。自定义端点直接指定协议与地址：

```bash
golder --provider deepseek -m deepseek-v4-flash -p "解释这段代码"
golder -P openai -u https://my-gateway.example.com/v1 -m my-model -k "$MY_KEY" -p "..."
golder -m ollama/qwen2.5-coder -u http://localhost:11434/v1 -p "..."   # 本地 Ollama
```

### 模型与推理档位

交互式里 `/model` 列出当前网关的模型并标注每个模型支持的推理档位；TUI 中选中模型后会接着让你选档位（Esc 取消整次切换），也可用 `/model <序号|id> <档位>` 一步切换；`/think <档位>` 只调当前档位。

`/provider` 列出全部内置网关：每个网关需要哪些环境变量、当前有没有配好（已配好的排在最前），`/provider <名称>` 直接切换网关（自动用该网关的默认模型）。TUI 里 `/provider` 是方向键选择器。

档位元数据来自 models.dev（网关自身的 `/models` 只返回模型 id，不含档位）。模型列表与档位表都以 24 小时磁盘缓存存在 `~/.golder` 下，因此每个网关大约一天只请求一次，而不是每次会话都请求。

## 内置工具

工具根植于当前工作目录；`--no-tools` 整体禁用，`--allowed-tools` / `--disallowed-tools` 做工具级准入（黑名单优先，子 Agent 继承，`--approve` 不可绕过）。

| 工具 | 说明 |
|------|------|
| `read` | 按路径读取文本文件，支持 offset/limit，输出带行号 |
| `view_image` | 读取本地图片（PNG/JPEG/GIF/WebP，≤8MiB）作为图片块附加给模型 |
| `apply_patch` | 一次补丁调用增删改移多个文件，返回逐文件 diff |
| `grep` / `find` | ripgrep 引擎的内容检索 / 文件名查找，自动跳过 `.gitignore` 与二进制文件 |
| `bash` | 执行 shell 命令，流式输出；未在等待窗口内结束则转为会话并返回 `bash_id`；`tty=true` 支持交互式程序 |
| `write_stdin` | 轮询 bash 会话输出、写入输入或发送中断（`\u0003`） |
| `todo` | 结构化任务清单（pending / in_progress / completed） |
| `webfetch` / `websearch` | 抓取网页转 Markdown / 联网搜索（按凭证自动选择后端） |
| `task` | 派发子 Agent（继承父级工具边界） |
| `memory_search` | 检索持久化记忆 |
| `get_context_remaining` / `new_context` | 查看剩余上下文预算 / 主动开启新上下文窗口 |
| `schedule_create` / `schedule_list` / `schedule_delete` | 会话内定时提醒：到点后作为新消息回到对话（不持久化） |

## 使用

```bash
# 交互式 TUI（不带 -p 且 stdout 为终端时自动进入）
golder

# 无头模式：只输出最终回答文本
golder -p "读取 README 并总结这个仓库"

# 逐行 JSON 事件（首个事件带 session_id），便于脚本消费
golder -p "列出所有 Go 文件" --output-format stream-json

# 续跑最近的会话（TUI 或 REPL 内可用 /resume 切换历史会话）
golder --continue
golder --resume <session-id>
golder -l                     # 列出全部会话
```

### 权限模式

改动型工具（`bash` / `apply_patch`）的审批由四档模式控制，启动时默认 `auto`，运行中用 `/permissions` 切换，也可用 `--permissions <mode>` 或 `GOLDER_PERMISSIONS` 指定：

| 模式 | 行为 |
|------|------|
| `read-only` | 只读；改动型调用直接拒绝 |
| `ask` | 改动型调用先经当前模型审查，低风险放行、其余询问你（无输入时拒绝） |
| `auto`（默认） | 模型审查每次改动型调用：低风险放行、需隔离的进沙箱、高危拒绝，并给出一行理由 |
| `full-access` | 不做审查、不套沙箱；仅保留静态硬拒名单（`sudo`、`rm -rf /` 等） |

首次在某个目录启动时会询问是否信任该目录；`--approve` 可为单次运行直接授权。需要操作系统级隔离时参见 [docs/sandboxing.md](docs/sandboxing.md)。

### 常用开关

| 参数 | 说明 |
|------|------|
| `-p, --print` | 无头模式的 prompt（位置参数等价） |
| `-m, --model` / `--provider` | 选择模型与网关 |
| `-a, --approve` | 本次运行信任工作目录，跳过逐次确认 |
| `--thinking-level` | 推理档位：`off` / `low` / `medium` / `high` / `xhigh` / `max` |
| `--no-tools`、`--allowed-tools`、`--disallowed-tools` | 工具级准入控制 |
| `--no-skills`、`--no-prompt-templates` | 关闭技能 / 提示词模板发现 |
| `--no-tui` | 交互模式下改用行式 REPL（不使用全屏 TUI） |

完整参数见 `golder --help`。

## 目录与环境变量

| 路径 / 变量 | 用途 |
|-------------|------|
| `~/.config/golder/config.toml` | 全局配置 |
| `~/.golder/sessions` | 会话存储（JSONL） |
| `~/.golder/.credentials.yaml` | 命名的 API Key 凭据（建议 0600 权限） |
| `~/.agents/skills` | 技能目录（注册为 `/命令`） |
| `GOLDER_HOME` | 覆盖 `~/.golder` 基础目录 |
| `GOLDER_PROXY` | opencode-zen / opencode-go 网关的 HTTP 代理 |
| `~/.golder/model-catalog.json`、`reasoning-catalog.json` | 模型列表 / 推理档位的 24h 缓存 |
| `OPENCODE_API_KEY`、`OPENCODE_ZEN_API_KEY` 等 `<PROVIDER>_API_KEY` | 各 Provider 的 API Key（`opencode-go` 用 `OPENCODE_API_KEY`，`opencode-zen` 只用 `OPENCODE_ZEN_API_KEY`） |

## 许可证

[MIT](LICENSE)
