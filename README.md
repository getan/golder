# golder

[English](README.en.md) | **简体中文**

[![CI](https://github.com/getan/golder/actions/workflows/ci.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/ci.yml)
[![Build](https://github.com/getan/golder/actions/workflows/build.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/build.yml)
[![Release](https://github.com/getan/golder/actions/workflows/release.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/release.yml)

用 Go 编写的终端 AI 编码助手：读写文件、执行命令、检索代码、抓取网页，在对话中完成从理解需求到改好代码的闭环。默认进入全屏 TUI，也支持行式 REPL 与无头脚本。

https://github.com/user-attachments/assets/f02bc1ec-c178-425a-92de-f73af1e862a9

<p align="center">
  <sub>真实录制、无剪辑造假：<code>go test</code> 红 → 模型自主修复 → 全绿（28 秒 · 2× 快放） · <a href="https://golder-cli.pages.dev">官网</a></sub>
</p>

## 特性

- **TUI 优先**：直接 `golder` 进入全屏 TUI（转录、工具卡片、状态栏、↑↓ 选择菜单）；`--no-tui` 切回行式 REPL，两端命令与行为完全对齐；`golder -p "..."` 无头执行，适合脚本与 CI。
- **内置工具集**：文件读取、补丁编辑、ripgrep 代码检索、Shell 会话、任务清单、网页抓取、联网搜索等（见[内置工具](#内置工具)）。
- **多 Provider**：默认 OpenCode 网关，内置 OpenAI / Anthropic / OpenRouter / DeepSeek / Ollama 等 40+ 网关，也可指向任意 OpenAI 兼容端点。
- **会话与分支**：`/resume` 切换历史会话（默认只看当前项目，`--all` 看全部），`/fork` 从任意历史消息分叉，`/clone` 复制当前会话，`/tree` 浏览分支树，`/export` `/import` 做会话存档往返，`/rewind` 把文件与对话一起回滚。
- **审批与沙箱**：四档权限模式（只读 / 每次询问 / 自动审批 / 完全放行）；沙箱档调用自动隔离执行——macOS 用 `sandbox-exec`，Linux 用 `bubblewrap`（bwrap，需已安装）。沙箱走**白名单**：可读仅限项目 + 系统运行时 + 两个 git 配置（`~/.gitconfig`、`~/.config/git/*`），`$HOME` 其余内容（shell rc、SSH/GPG 密钥、`~/.config/*`、凭据、历史）一律读不到；写默认限项目、临时目录与显式授权的 `writable_roots`（审批弹窗里按 `w` 可对某路径本会话放行；`/permissions writable add <path>` 持久化到 `~/.config/golder/permissions.toml`，也可在 `config.toml` 的 `[permissions] writable_roots` 手写）；网络默认关闭。工具链在 `$HOME` 内时用 `GOLDER_SANDBOX_READABLE` 追加读白名单。被拒时模型可携理由申请提权（`require_escalated`），由审查层或你拍板；提权请求与"应当收容"冲突时升级给你拍板，而不是静默收容。
- **长任务支持**：`/compact` 上下文压缩、上下文预算工具、`/goal` 自主目标循环、`/btw` 侧线问答、子 Agent 派发、持久记忆与 `/dream` 记忆整理。
- **工作区外读取需确认**：读取类工具（`read` / `grep` / `find` / `ls` / `view_image`）默认只读工作区，越界会先问你——弹窗可选「只读这一次」或「本会话允许读该目录」；没有可问的人时（headless、子 agent）失败关闭，并在消息里给出路径与配置办法。这一步**不受权限模式影响**（完全放行关掉的是审查与沙箱，不是知情同意）。常用目录可预授权：`config.toml` 的 `[permissions] readable_roots` 或 `GOLDER_READABLE_ROOTS`（同时放宽沙箱读白名单）；凭据路径（`~/.ssh`、`~/.zshrc` 等）永远不可授权。
- **按 Provider 的代理路由**：`/proxy` 单独选择哪些 provider 走代理，互不影响；未选中的 provider 直连，且不继承 shell 的 `HTTP_PROXY` / `HTTPS_PROXY`。
- **技能与模板**：`~/.agents/skills` 下的技能注册为 `/命令`（无内置技能，目录不存在时零命令）；提示词模板、项目级 `AGENTS.md` 自动装载。

## 安装

### 一键安装（Linux / macOS）

```bash
# 一、站点镜像（对多数大陆网络更稳）
curl -fsSL https://golder-cli.pages.dev/install.sh | sh

# 二、GitHub 原始地址（pages.dev 不通时改用，如阿里云 ECS 等大陆云主机）
curl -fsSL https://raw.githubusercontent.com/getan/golder/master/install.sh | sh
```

两条取到的是同一份脚本（站点部署时从主仓库镜像），按自己的网络二选一；若两条都不通，可给 raw 地址套镜像前缀，例如 `curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/getan/golder/master/install.sh | sh`。

脚本会校验下载包的 sha256；下载归档时先直连 GitHub，失败后自动回退到公益镜像（`ghfast.top`、`ghproxy.net`、`gh-proxy.com`、`gh.zwy.one`）。连上后 15 秒无数据、或平均速度低于 50KB/s 持续 15 秒（龟速滴流），都会判死并切换下一个源。校验文件（`checksums.txt`）走信任阶梯：优先直连 GitHub；直连不可用时改用镜像副本，但要求各应答来源对同一归档给出一致的 sha256，不一致立即中止（单个镜像无法同时伪造归档和校验文件）；受限网络下只有一个镜像可用时继续安装但会明确告警。`GOLDER_MIRROR` 可覆盖镜像列表（空格分隔的 URL 前缀，前缀拼在原 URL 前），设为 `off` 只用直连。

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

### API Key 一览（重要）

| 用途 | 环境变量 | 说明 |
|------|----------|------|
| 默认网关 OpenCode | `OPENCODE_API_KEY` | `opencode-go` 订阅网关，默认 provider |
| Zen 网关 | `OPENCODE_ZEN_API_KEY` | `opencode-zen`，与 Go 分开计费、独立 Key |
| 联网搜索（首选） | `TAVILY_API_KEY` | Tavily，面向 LLM 优化的结果 |
| 联网搜索（次选） | `EXA_API_KEY` | Exa，返回 highlights 摘要 |
| 其他网关 | `<PROVIDER>_API_KEY` | 如 `DEEPSEEK_API_KEY`、`XAI_API_KEY`；见 `golder --help` 全表 |

联网搜索后端按 **Tavily > Exa > DuckDuckGo** 自动选择：两者都没配时，用无需 Key 的 DuckDuckGo 兜底（大陆网络访问 DuckDuckGo 需要代理）。

### OpenCode（默认）

golder 默认走 `opencode-go` 网关，模型 `deepseek-v4.1-flash`，推理档位 `max`。只需设置 API Key：

```bash
export OPENCODE_API_KEY=...
golder
```

若本机网络无法直接访问 `opencode.ai`，配置代理即可；**代理出口不要求美国**，普通线路能连通就行：

```bash
export GOLDER_PROXY=http://127.0.0.1:7897
```

#### Muse / Grok 的区域与开关要求（重要）

- **只有 `muse-spark-*` 和 `grok-*` 需要美国出口 IP**；其他国产模型（DeepSeek、Qwen、MiniMax、GLM、Kimi 等）直连即可，不需要代理。
- **Muse Contributor 模型必须开通"允许使用数据改进模型"的开关**，否则调用会报 `This model collects data used to improve its quality and requires explicit opt in`。开关在 OpenCode 工作台的 Go 页面：

  ```text
  https://opencode.ai/auth                                   # 登录
  https://opencode.ai/workspace/<你的 workspace-id>/go       # 打开数据分享开关
  https://opencode.ai/docs/go/                               # Go 订阅模型与定价
  https://dev.meta.ai/docs/pricing-rate-limits#contributor-tier   # Meta Contributor 条款
  ```

- golder 检测到 muse 模型会自动使用 Responses 协议，无需手动设置。

用美国出口代理调用 Muse 的示例：

```bash
export GOLDER_PROXY=http://127.0.0.1:7897    # 代理节点需为美国出口
golder -m muse-spark-1.3-contributor
```

### 代理路由（/proxy）

`/proxy` 打开代理选择器：方向键选择 provider，Enter 开关并自动保存，Esc 退出。也可以直接输入：

```text
/proxy url http://127.0.0.1:7897
/proxy openai on
/proxy anthropic on
/proxy deepseek off
```

代理地址与 provider 选择相互独立。`GOLDER_PROXY` 优先于保存的地址，但不会让所有 provider 走代理；未选中的 provider 直接连接，不继承 `HTTP_PROXY` / `HTTPS_PROXY`。显式启用代理却未设置地址时，会提示配置地址。尚未保存选择时保留 OpenCode 的历史默认行为：设置地址才走代理。

选择保存在 `~/.config/golder/proxy.toml`（支持 `XDG_CONFIG_HOME`），立即用于后续聊天和模型列表请求，重启后继续有效。例如：

```toml
url = "http://127.0.0.1:7897"
providers = ["openai", "anthropic"]
```

### OpenAI 中转站

通过 `OPENAI_BASE_URL` 设置地址，是否走代理由 `/proxy` 单独控制：

```bash
export OPENAI_BASE_URL=https://relay.example.com/v1
golder --provider openai
```

聊天使用该地址，模型列表请求同一个地址下的 `/models`，缓存也按实际地址区分。`/provider` 列表及 `--help` 显示各 provider 的 API Key 和 `*_BASE_URL` 变量名；选择器的详情区显示实际地址、配置来源和代理状态。`BASE_URL` 是可选项，不设置时使用官方默认地址。

### 配置文件

全局配置放在 `~/.config/golder/config.toml`，键名与 CLI flag 对应（命令行传入时优先）：

```toml
provider = "opencode-go"
model = "deepseek-v4.1-flash"
thinking_level = "max"            # off | minimal | low | medium | high | xhigh | max
# base_url = "https://..."        # 覆盖 Provider 默认端点
# api_key = "..."                 # 建议改用环境变量或 credential 引用
# credential = "my-key"           # 引用 ~/.golder/.credentials.yaml 中的条目
# [permissions]
# writable_roots = ["~/.cache/go-build", "/Volumes/KIOXIA/rust-target"]  # 沙箱额外可写（同时可读）路径
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

交互式里 `/model` 列出当前网关的模型并标注每个模型支持的推理档位；TUI 中选中模型后会接着让你选档位（Esc 取消整次切换），也可用 `/model <序号|id> <档位>` 一步切换。只调当前档位用 `/model think <档位>`（不带参数则显示当前档位），因为档位属于模型，入口统一在 `/model` 下。

`/provider` 列出全部内置网关：每个网关需要哪些环境变量、当前有没有配好（已配好的排在最前），`/provider <名称>` 直接切换网关（自动用该网关的默认模型）。TUI 里 `/provider` 是方向键选择器。

档位元数据来自 models.dev（网关自身的 `/models` 只返回模型 id，不含档位）。模型列表与档位表都以 24 小时磁盘缓存存在 `~/.golder` 下，因此每个网关大约一天只请求一次，而不是每次会话都请求。

## 内置工具

工具根植于当前工作目录；`--no-tools` 整体禁用，`--allowed-tools` / `--disallowed-tools` 做工具级准入（黑名单优先，子 Agent 继承，`--approve` 不可绕过）。

| 工具 | 说明 |
|------|------|
| `read` | 按路径读取文本文件，支持 offset/limit，输出带行号 |
| `view_image` | 读取本地图片（PNG/JPEG/GIF/WebP，≤8MiB）作为图片块附加给模型 |
| `apply_patch` | 一次补丁调用增删改移多个文件，返回逐文件 diff |
| `grep` / `find` | ripgrep 引擎的内容检索 / 文件名查找；`path` 支持单个目录或目录列表，`exclude` 排除、`glob`/`type` 圈定，`limit` 限条数（默认 1000）；自动跳过 `.gitignore` 与二进制文件 |
| `bash` | 执行 shell 命令，流式输出；未在等待窗口内结束则转为会话并返回 `bash_id`；`tty=true` 支持交互式程序 |
| `write_stdin` | 轮询 bash 会话输出、写入输入或发送中断（`\u0003`） |
| `todo` | 结构化任务清单（pending / in_progress / completed） |
| `webfetch` / `websearch` | 抓取网页转 Markdown / 联网搜索（按凭证自动选择后端：Tavily / Exa / DuckDuckGo） |
| `task` | 派发子 Agent（继承父级工具边界） |
| `memory_search` | 检索持久化记忆（BM25 全文索引） |
| `get_context_remaining` / `new_context` | 查看剩余上下文预算 / 主动开启新上下文窗口 |
| `schedule_create` / `schedule_list` / `schedule_delete` | 会话内定时提醒：到点后作为新消息回到对话（不持久化） |

`/goal` 运行期间还会额外挂载 `goal_complete` / `goal_blocked` 两个目标控制工具，仅在自主循环内可见。

## 斜杠命令

TUI 输入 `/` 会按下面的分类弹出菜单（↑↓ 选择）；`/help` 输出同一份分组列表。两类界面（TUI 与 REPL）命令完全对齐。

| 分类 | 命令 |
|------|------|
| General | `/help` `/status` `/exit` |
| Session | `/resume` `/compact` `/fork` `/clone` `/tree` `/rewind` `/export` `/import` |
| Model | `/model` `/provider` `/proxy` |
| Memory | `/memory` `/dream` |
| Permissions | `/permissions` `/trust` |
| Modes | `/goal` `/btw` `/remote-control` |
| Extensions | 技能、插件、提示词模板按来源列在这里 |

要点：

- `/compact` 立即对上下文做一次摘要压缩；`/status` 内含会话 id、消息数、创建时间与上下文用量（原 `/session` 已并入）。
- **Ctrl+C 的一致行为**：运行中按一次即请求中断（会打印 `(interrupting…)`，随后以同一句 `Interrupted.` 结算）；同一次运行内再按不会重复提示，也不会提前退出。空闲时按一次清掉草稿（并提示已丢弃），输入框为空时第一次按下只"待命"（提示再按一次），第二次才退出——与 codex 一样，不设"运行中双击强退"，避免误触丢掉未落盘的一轮。Esc 与 Ctrl+C 的中断行为一致。
- **resume 会用当前二进制重建系统提示**：续跑旧会话时，工具用法指南、环境块（工作目录、日期、rg 是否可用）、AGENTS.md 与技能清单都按**现在**重新生成，不再沿用会话文件里冻结的那份（旧会话里那份的日期停在创建当天，AGENTS.md 的改动也不生效）；而你自己写的部分——`--system-prompt` 的替换文本与 `--append-system-prompt` 的追加块——会原样保留，本次启动显式传入的 flag 优先。会话文件里仍记录创建时的原样 prompt，`/export --include-system-prompt` 看到的是它。
- `/resume`、`/fork`、`/tree`、`/rewind` 在 TUI 中都是方向键选择器，REPL 中输入序号选择。`/resume` 默认只列**当前项目**的会话（项目 = 启动目录所在仓库的根，子目录与根共享一份历史），列表里每条都带运行目录；`/resume --all` 列出所有项目，被过滤掉或超出范围时列表会给出提示并指到 `--all`。`--resume <id>` 与 `/resume <id>` 始终全局精确命中，不受项目过滤影响。
- 技能目录 `~/.agents/skills` 里每多一个技能就多一条 `/命令`；全新用户没有该目录时，Extensions 组为空。可用 `GOLDER_SKILLS_DIR` 换目录、`--no-skills` 全关。

## 使用

```bash
# 交互式 TUI（不带 -p 且 stdout 为终端时自动进入）
golder

# 无头模式：只输出最终回答文本
golder -p "读取 README 并总结这个仓库"

# 逐行 JSON 事件（首个事件带 session_id），便于脚本消费
golder -p "列出所有 Go 文件" --output-format stream-json

# 续跑本项目最近的会话（本项目没有会话时退回全局最近一条，并在 stderr 说明来源目录）
golder --continue
golder --resume <session-id>
golder -l                     # 列出全部项目的会话（含运行目录）

# 会话导出 / 更新二进制
golder session export <session-id> --format md --output talk.md
golder update
```

`golder update` 的版本检查走站点接口（大陆可达、无速率限制），失败回退 GitHub API；下载 release 时与安装脚本同一条「直连 → 公益镜像」回退链，归档按 release 的 `checksums.txt` 校验（坏包自动换下一个源），而 `checksums.txt` 本身优先直连 GitHub，只有镜像应答时要求各来源一致（单源可用时告警后继续）；`GOLDER_MIRROR=off` 可禁用镜像。

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
| `~/.config/golder/proxy.toml` | `/proxy` 保存的代理地址和 provider 选择 |
| `~/.golder/sessions` | 会话存储（JSONL） |
| `~/.golder/.credentials.yaml` | 命名的 API Key 凭据（建议 0600 权限） |
| `~/.agents/skills` | 技能目录（注册为 `/命令`；不存在则无扩展命令） |
| `GOLDER_HOME` | 覆盖 `~/.golder` 基础目录 |
| `GOLDER_SKILLS_DIR` | 覆盖技能发现目录（默认 `~/.agents/skills`） |
| `GOLDER_PROXY` | 为选中的 provider 提供代理地址，优先于 `/proxy` 保存的地址 |
| `GOLDER_SANDBOX` | 沙箱档执行：`off` / `auto`（默认，仅沙箱档）/ `enforce`（全部 bash 进沙箱，无 runner 则 fail-closed） |
| `GOLDER_SANDBOX_NETWORK` | 沙箱内是否放行网络：默认关闭（出口需显式授予）；设为 `on` 恢复 |
| `GOLDER_SANDBOX_READABLE` | 追加沙箱读白名单（冒号/逗号分隔的绝对路径），如 `/Volumes/KIOXIA:$HOME/miniconda3` |
| `GOLDER_READABLE_ROOTS` | 追加读取类工具（read/grep/find/ls/view_image）的免询问目录（冒号/逗号分隔），等同 `[permissions] readable_roots`；同时放宽沙箱读白名单 |
| `GOLDER_MIRROR` | 自更新 / 安装脚本的下载镜像：空格分隔的 URL 前缀，或 `off` 禁用（默认直连 GitHub 失败后回退公益镜像） |
| `OPENAI_BASE_URL` 等 `<PROVIDER>_BASE_URL` | 覆盖该 provider 的聊天和模型列表 API 地址 |
| `TAVILY_API_KEY` / `EXA_API_KEY` | 联网搜索后端凭证（Tavily / Exa） |
| `~/.golder/model-catalog.json`、`reasoning-catalog.json` | 模型列表 / 推理档位的 24h 缓存 |
| `OPENCODE_API_KEY`、`OPENCODE_ZEN_API_KEY` 等 `<PROVIDER>_API_KEY` | 各 Provider 的 API Key（`opencode-go` 用 `OPENCODE_API_KEY`，`opencode-zen` 只用 `OPENCODE_ZEN_API_KEY`） |

## 许可证

[MIT](LICENSE)
