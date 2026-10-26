# golder

[English](README.en.md) | **简体中文** | [官网](https://golder-cli.pages.dev/)

[![CI](https://github.com/getan/golder/actions/workflows/ci.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/ci.yml)
[![Build](https://github.com/getan/golder/actions/workflows/build.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/build.yml)
[![Release](https://github.com/getan/golder/actions/workflows/release.yml/badge.svg)](https://github.com/getan/golder/actions/workflows/release.yml)

用 Go 编写的终端 AI 编码助手：读写文件、执行命令、检索代码、抓取网页，在对话中完成从理解需求到改好代码的闭环。默认用 `golder workspace`：自带分屏终端，agent、shell 和浏览器在同一个面板里；已经有习惯的终端复用器时，直接在它的 pane/window 里跑单 agent 的 `golder`。也支持行式 REPL 与无头脚本。

**单二进制、默认沙箱、密钥不进入模型上下文。**

## 安装

```bash
brew install getan/tap/golder
# 或
curl -fsSL https://golder-cli.pages.dev/install.sh | sh
```

macOS / Linux 一条命令装好，不需要 Node/Python。Windows 从 [Releases](https://github.com/getan/golder/releases) 下载 `.zip`；镜像、sha256 校验、从源码构建等细节见[安装细节](#安装细节)。

**Linux 强依赖 bubblewrap（`bwrap`）来提供默认沙箱**；macOS 使用系统自带的 `sandbox-exec`。安装脚本发现 Linux 缺少 `bwrap` 时，会用 root 或 `sudo` 调用系统包管理器安装，支持 Debian/Ubuntu（apt）、Fedora/RHEL/Rocky/AlmaLinux（dnf/yum，需软件源提供该包）、Arch/Manjaro（pacman）、openSUSE（zypper）和 Alpine（apk）。已有 `bwrap` 则直接复用；没有权限、包不可用或发行版不支持时，会中止并提示手动安装，不会静默跳过依赖。直接下载二进制或从源码构建也需自行安装，例如 `sudo apt-get install bubblewrap`。

安装了包不等于沙箱可用：Linux 还需允许创建相应的 namespace；容器或 AppArmor 等宿主策略可能阻止它。golder 会在运行时探测，安装脚本不会替你放宽宿主安全策略。

## 推荐终端

推荐在 [Ghostty](https://github.com/ghostty-org/ghostty) 里使用 golder，TUI 也主要在 Ghostty 上测试，因此在那里的表现最接近日常开发时的样子。其他现代终端同样可以运行，但测试覆盖以 Ghostty 为主；内置浏览器 pane 仍需要终端支持相应的图形协议。

**SSH / 低带宽终端**：普通 agent TUI 检测到 `SSH_TTY` 或 `SSH_CONNECTION` 时省略大 logo，使用 ASCII 装饰的纯文本状态栏，关闭状态栏滚动与工作指示器动画（耗时统计仍每秒更新）。同时不发送启动时的颜色／Kitty 能力查询，Markdown 使用默认暗色配色，避免终端回包握手拖住早期输入。恢复会话先绘制最新一块内容，再按预算补齐首屏；旧消息保留，滚动时继续加载。

本地仍保留原有 logo、颜色与键盘能力协商；可用 `golder --no-animations` 或 `GOLDER_NO_ANIMATIONS=1 golder` 单独关闭动画，用 `GOLDER_NO_TERMINAL_QUERIES=1 golder` 单独关闭自动查询。保留动画时，第一个按键或粘贴会立即结束 logo 入场动画，该次输入照常处理，不会被吞掉。显式剪贴板操作、真实光标与中文输入法定位不受查询开关影响。

流式 token 计数和耗时只在 spinner 定时 tick 时发布，不再被每个 token 带着刷新。SSH／禁用动画时，正文增量按 150ms 合并显示（本地普通模式为 50ms），减少终端写入；输入、审批和最终结果不等待这个合并定时器。真实光标仍保留，供中文输入法定位。排查方法见 [TUI 延迟与刷新](docs/tui-latency.md)。

`golder workspace --no-animations` 控制外层工作区动画；要同时关闭首个 agent 的动画，可用 `GOLDER_NO_ANIMATIONS=1 golder workspace --no-animations`，或启动时传 `golder workspace --no-animations -- --no-animations`。已运行的 agent 不会因重新 attach 改变设置。

终端鼠标/颜色等回包在慢链路上分段到达时，输入解码器会保留完整协议帧，不再因短超时把 `65;59M`、`rgb:…` 等尾部当成输入。升级后需重新连接 workspace 客户端；已运行的内层 golder 也需重启/恢复会话才能载入新解码器。正常输入或粘贴的同样文本不会被过滤。详见[终端输入与恢复边界](docs/terminal-input.md)。

## 快速开始：golder workspace

普通用户第一条命令就是它：`golder workspace` 打开一个自带的分屏终端，第一格是 agent；`Ctrl+b c` 再加一个 shell，浏览器按需。`Ctrl+b d` 只断开客户端，daemon 和 pane 继续在后台跑。

```bash
golder workspace                         # 打开全局 default；没有运行则启动
golder workspace -s coding               # 打开另一套命名会话
golder workspace new trading -C ~/trade  # 严格新建，名字已存在就报错
golder workspace attach coding          # 只连接正在运行的会话，不创建
golder workspace list                    # 全局列表，与当前目录无关
golder workspace list --json
golder workspace status coding
golder workspace stop coding            # 结束这套 daemon 和所有 pane，保留记录
golder workspace rm coding              # 删除已停止会话的记录和默认状态目录
golder workspace --shell                 # 启动时首个 pane 改为 shell
golder workspace -- --model NAME         # 启动时给首个 agent 指定模型
```

macOS / Linux 上不必另装 `tuios` 或 `kitty-browse`。默认 `Ctrl+b d` 脱离客户端后，后台 daemon 与 pane 继续运行；再次运行 `golder workspace` 可重新连接。已有 tuios/herdr 中的普通 `golder` 行为不变；嵌套工作区默认拒绝，需要明确 `--nested`。

工作区采用**焦点即输入**：点击或切换到 pane 后直接输入，不需要在“窗口管理模式”和“终端模式”之间切换。`Ctrl+b` 后接方向键切换 pane，接 `c` 新建 pane、`P` 打开命令面板、`?` 查看帮助；命令执行后即可继续输入。普通 `Esc`、`Tab`、`Ctrl+c` 和字母交给 pane 内的程序；前缀等待期间的 `Esc` / `Ctrl+c` 只取消命令，不透传给 shell。

拖动标题栏移动 pane，拖动边框/分隔线调整大小；普通内容拖动始终选文字，原有的 `Alt` / `Ctrl` 拖动移动手势仍然可用。界面升级后，先脱离旧客户端，再用新版 `golder workspace` 重新连接即可使用新交互，无需为了升级界面而结束已有 pane。

**名字选会话，目录只决定进程从哪里启动。** 同一用户、同一个 `GOLDER_HOME` 下，在不同目录运行 `golder workspace` 都进入 `default`；`coding`、`trading` 各有独立 daemon、配置、状态和日志。首次启动使用当前目录（可用 `-C/--cwd` 指定）；已停止会话重启使用保存的启动目录，`-C` 可显式更新。正在运行时 `-C`、`--shell` 和 agent 参数会报错，不能借 attach 修改现有 pane。连接、查询、停止不要求原项目目录仍存在。会话名使用小写字母、数字、`-`、`_`，以字母或数字开头，最多 64 字节。

agent、shell、浏览器入口都来自同一个 golder 二进制，但**每个命名会话**的后台 daemon 是独立进程。浏览器仍需本机 Chromium 和支持相应图形协议的终端，不会自动下载 Chromium。`--headless` 输出一行 JSON readiness；新启动时持续运行 daemon，已有 daemon 时仅报告 readiness 并退出，不等同于可见工作区。状态与自有 harness 放在 `$GOLDER_HOME/workspace/sessions/<name>/`，不读写个人 tuios 配置。`stop` 不会恢复运行中的任务；下次启动是新进程，不自动复活旧 PTY。`rm` 只删除该命名目录，不删除外置的 `--state-dir`/`--config`，也不删除项目或聊天历史。工作区中的 pane 仍按各自 cwd 确定项目和 agent 权限；命名会话不提供 git/worktree 隔离。详见 [命名会话与兼容规则](docs/workspace-sessions.md)。

旧的项目级会话不会自动搬家或被认领为 `default`。`list` 将其标为 `legacy`，显示 `<project-id>/<session>`；用 `golder workspace attach --legacy <project-id>/<session>` 连接，`status`/`stop` 同样支持该参数。没有旧 owner 记录的会话无法可靠确定 endpoint，会显示 `unreachable`，不会猜测或启动替代进程。

### 已经有习惯的终端复用器时（tuios / herdr / tmux / zellij / screen）

这些复用器已经提供了自己的 server/client 与 detach/reattach，**这时才推荐直接用单 agent 的 `golder`**：在它们的 pane/window 里跑普通 `golder`，保持/恢复会话交给宿主，不必再套一层 workspace。

```bash
# 在 tuios / herdr pane，或 tmux / zellij / screen window 里
golder
```

检测到已经在这些宿主里时，`golder workspace` 默认会拒绝嵌套（除非显式 `--nested`，通常没有这个必要）。tuios/herdr 的 rail/Inbox 状态上报属于 host-compat，和 workspace 的 daemon/client 是两套独立机制，按需配置即可。

### 进程与连接关系

`golder workspace` 不是把 agent 跑在客户端里，而是三层：

```text
终端 client（golder workspace）
        │ Unix socket
workspace daemon（后台常驻，拥有 pane）
        ├─ agent pane：普通 golder 进程
        ├─ shell pane
        └─ browser pane
```

- `golder workspace` 默认启动 daemon，再在前台 attach 一个 client。`Ctrl+b d` 只断开 client，daemon 和 pane 继续运行；再次 `golder workspace` 是 attach 回同一个 daemon，不是启动新 agent。`golder workspace stop` 才会结束 daemon 和所有 pane；`--headless` 只启动 daemon，不 attach client。
- 同一个 daemon 可以同时挂多个 client。比如 Mac 上开着 `coding`，iPad SSH 到同一台 Mac、同一用户和 `GOLDER_HOME` 后执行 `golder workspace attach coding`，就是再挂一个 client，两边看到并操作同一组 pane；无需重复项目路径或自定义 socket，endpoint 已保存在会话记录里。Unix socket 是本机的，换机器或换用户连不到。
- agent pane 里的 `golder` 是普通 agent 进程，负责模型调用、工具执行和会话持久化；daemon 负责 pane 生命周期和终端 I/O，client 负责画面与输入。
- 不带 workspace 的直接 `golder` 不创建 daemon/client，它就是当前终端里的一个普通 agent 进程。没有外层复用器时，终端断开，进程就结束。

## 亮点

- **默认用法是自带终端工作区**：`golder workspace` 一个面板里跑 agent、shell 和浏览器——左边 agent 改代码，右边 shell 跑测试，焦点即输入；`browser` 通过 kitty-browse 的控制 socket 操作页面（snapshot/click/fill/wait/console/network），`workspace` 预设（`debug-fullstack`、`data-analysis`、`review`）一次起好后端、前端和浏览器，再用 `snapshot` 把所有 pane 的上下文一次交给模型。`visual`（图表 / Mermaid / TML 面板）已下架，原因见 CHANGELOG 的 Removed 一节。
- **多 Provider 热切换，会话不绑定厂商**：`/provider` 随时换网关，`/resume` 在当前 provider 上继续同一份上下文；内置 19 个会话网关，覆盖 OpenAI Chat / Responses、Anthropic Messages、Gemini 四种线上协议，也可指向任意兼容端点。
- **轻量 agent + 真正的安全边界**：和 pi 同一量级的单二进制，但多了逐调用审批门与 OS 沙箱（`sandbox-exec` / `bubblewrap`）——读限工作区、写限项目与临时目录、网络默认关闭；密钥和凭据路径读不到。
- **比 codex-cli 更容易定制**：纯 Go，单语言、单二进制，没有 Node/Python 运行时；加一个工具、一个 provider 或一条审批规则，都在同一个 `go build` 里完成。
- **常驻内存约 50–100 MB**：同类里的第一梯队，长时间挂着跑也不占机器。
- **规划中：MCP 与插件生态**：把外部工具和数据源接进来，扩展不必改核心。

<p align="center">
  <a href="https://golder-cli.pages.dev/?utm_source=github&utm_medium=readme#demo">
    <img src="docs/assets/demo.gif" width="92%" alt="golder workspace 真实会话：左边 agent 改代码，右边 shell 里 go test 红 → 全绿">
  </a>
</p>

<p align="center">
  <sub>真实会话、无剪辑造假：左边 agent 自己读码打补丁，右边 shell 里 <code>go test</code> 从红到绿（12 秒 GIF，<a href="https://golder-cli.pages.dev/?utm_source=github&utm_medium=readme#demo">完整 37 秒录像</a>）</sub>
</p>

<table>
<tr>
<td width="50%"><img src="docs/assets/demo-tools.png" alt="分屏工作区：左 pane 的补丁 diff 卡片，右 pane 里红着的 go test"></td>
<td width="50%"><img src="docs/assets/demo-done.png" alt="收尾：agent 逐条给出验证结果，shell 里复跑全绿"></td>
</tr>
</table>

## 更多特性

- **工作区优先，单 agent 也完整**：`golder workspace` 打开自带分屏终端（agent / shell / 浏览器）；已经有习惯的复用器时直接跑 `golder`，全屏 TUI（转录、工具卡片、状态栏、↑↓ 选择菜单）一样完整；`--no-tui` 切回行式 REPL，`golder -p "..."` 无头执行，适合脚本与 CI。
- **内置工具集**：文件读取、补丁编辑、ripgrep 代码检索、Shell 会话、任务清单、网页抓取、联网搜索等（见[内置工具](#内置工具)）。
- **tuios pane 工具**：在 tuios pane 内额外提供一个 `tuios` facade（`list` / `capture` / `wait` / `run` / `new` / `focus` / `close`），可管理当前 session 的所有 pane，等待走 daemon 事件而非轮询；见 [docs/tuios-tools.md](docs/tuios-tools.md)。
- **凭据脱敏**：tool output、用户 prompt、assistant 文本、compaction 摘要、memory 与 session 落盘前统一扫描；覆盖 AWS/GCP/Azure/GitHub/GitLab/Slack/Stripe/OpenAI/Anthropic/JWT/PEM/DB URL 等规则，支持 `off | observe | redact | block` 四档，审计只记规则名与 hash。
- **多 Provider**：默认 OpenCode 网关，内置 19 个网关（OpenAI / Anthropic / OpenRouter / DeepSeek 等），也可指向任意 OpenAI 兼容端点。
- **会话与分支**：`/resume` 切换历史会话（默认只看当前项目，`--all` 看全部），`/fork` 从任意历史消息分叉，`/clone` 复制当前会话，`/tree` 浏览分支树，`/export` `/import` 做会话存档往返，`/rewind` 把文件与对话一起回滚。
- **审批与沙箱**：四档权限模式（只读 / 每次询问 / 自动审批 / 完全放行）；**获准执行的 `bash` 调用一律在沙箱内跑**（macOS 用 `sandbox-exec`，Linux 用 `bubblewrap`，需已安装），与命令内容无关——原生文件工具（`read`/`grep`/`find`/`ls`/`view_image`/`apply_patch`）也由一次性沙箱 worker 执行。沙箱走**白名单**：可读仅限项目 + 系统运行时 + 两个 git 配置（`~/.gitconfig`、`~/.config/git/*`），`$HOME` 其余内容（shell rc、SSH/GPG 密钥、`~/.config/*`、凭据、历史）一律读不到；写默认限项目、临时目录与显式授权的写根；**网络默认关闭，且按次授权**——需要联网的调用声明 `network:"on"`，授权门在同一次审批里决定（批了仍在文件沙箱内，不必脱离沙箱）。**路径授权可在弹窗里一键记住**：读授权按 `a`（本会话）/`s`（记住）；写确认弹窗（`apply_patch` 写工作区外）按 `y`/`a`/`s`；越界申请与重准入弹窗里的写行是 `w`（本会话）/`s`（记住）。记住档直接写入 `~/.golder/grants.json`。同一份文件可用 `/boundary readable|writable list|add|rm` 查看与维护，无法编辑文件的机器（脚本、容器）用 `GOLDER_READABLE_ROOTS` 环境变量。工具链在 `$HOME` 内时用 `GOLDER_SANDBOX_READABLE` 追加读白名单。确实需要**文件**访问而沙箱挡不住时，模型可申请 `require_escalated`——这只是**同一次评审**里的又一个能力问题，不是第二轮审批：**`auto` 下档位就是全部答案**——`allow`/`confirm` 直接在沙箱外跑并播报，`contain` 收容，`deny` 不跑；`ask` 下弹窗把 `run outside the sandbox` 与其他能力一起列给你，`y`/`a`/`s` 即批准；`enforce` 永远收容，没人可问则收容或失败关闭。**命令被沙箱拒绝时**：只要输出或理由点得出路径，`auto` 由评审自动决定是否补授（不弹窗），`ask` 弹一次路径授权，批准后原命令在沙箱内自动重跑。
- **长任务支持**：`/compact` 上下文压缩、上下文预算工具、`/goal` 自主目标循环、`/btw` 侧线问答、子 Agent 派发、持久记忆与 `/dream` 记忆整理。
- **工作区外读取需确认**：读取类工具（`read` / `grep` / `find` / `ls` / `view_image`，以及 `bash` 命令里能识别出的文件操作数）默认只读工作区，越界会先问你——弹窗可选「只读这一次」「本会话允许读该目录」或「记住（跨会话，写入 `grants.json`）」；没有可问的人时失败关闭，并在消息里给出路径与配置办法；TUI/REPL 里的子 agent 会把问题送进同一个弹窗，标题标出 `(from task)` 之类的来源——人就坐在那儿看着，唯一真正问不到人的是 headless 与进程隔离的子 agent。这一步**不受权限模式影响**（完全放行关掉的是审查与沙箱，不是知情同意）。常用目录可预授权：`GOLDER_READABLE_ROOTS` 环境变量，或 `/boundary readable add <路径>`（写入 `grants.json`，重启后仍生效；同时放宽沙箱读白名单）；凭据路径（`~/.ssh`、`~/.zshrc` 等）永远不可授权。
- **按 Provider 的代理路由**：`/proxy` 单独选择哪些 provider 走代理，互不影响；未选中的 provider 直连，且不继承 shell 的 `HTTP_PROXY` / `HTTPS_PROXY`。
- **技能与模板**：`~/.agents/skills` 下的技能注册为 `/命令`（无内置技能，目录不存在时零命令）；提示词模板、项目级 `AGENTS.md` 自动装载。
- **在 tuios / herdr 里就是一个 agent**：跑在 [tuios](https://github.com/Gaurav-Gosain/tuios) 窗口或 [herdr](https://github.com/herdrdev/herdr) pane 里时，golder 自己上报状态，于是它出现在 rail/sidebar 上（working / 等待你 / 空闲 / 完成），审批与提问进 Inbox，daemon 重启后会带着 `golder --resume <会话>` 回到原会话。**两个宿主都不用改、也都不用装**：tuios 需要一份 harness manifest，`golder host-compat install` 写进 `~/.config/tuios/harnesses/`；herdr 什么也不需要，自报即识别。在 tuios 的 Inbox 里可以直接按键替你批准/拒绝——那条路依赖 manifest 里的屏幕规则，所以**改了审批弹窗的文案要同步改那份 manifest**（见 `internal/hostcompat/README.md`）。`golder host-compat status` 说明当前宿主与缺什么；`GOLDER_HOST_REPORT=off` 一键关闭上报。

## 安装细节

### Homebrew（macOS / Linux）

```bash
brew install getan/tap/golder
```

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

选择保存在 `~/.golder/proxy.toml`，立即用于后续聊天和模型列表请求，重启后继续有效。例如：

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

全局配置放在 `~/.golder/config.toml`，键名与 CLI flag 对应（命令行传入时优先）：

```toml
provider = "opencode-go"
model = "deepseek-v4.1-flash"
thinking_level = "max"            # 全局回退；按 provider+model 的默认见 [thinking_defaults]
# base_url = "https://..."        # 覆盖 Provider 默认端点
# api_key = "..."                 # 建议改用环境变量或 credential 引用
# credential = "my-key"           # 引用 ~/.golder/.credentials.yaml 中的条目
```

也可以用环境变量覆盖：`GOLDER_MODEL`、`GOLDER_PROVIDER`、`GOLDER_THINKING_LEVEL`、`GOLDER_PERMISSIONS`。命令行 flag 优先级最高。项目级还可以放一份 `.golder/config.json` 覆盖 `model` / `provider` / `thinkingLevel` / `hooks`——仅在目录被信任时加载。

### tuios shell integration（zsh / bash，推荐）

推荐在 tuios pane 里运行 golder。要完整使用 tuios 的窗口与命令管理，尤其是 `tuios run`、`wait-for command-finished`、`capture-pane --last-command`，pane 的 shell 必须发送 OSC 133 命令标记；否则这些操作会报 `no_shell_integration`，golder 内置 `tuios` 的 `run` 也只能走降级路径。只使用 `list`、`capture`、`wait`（如 `window-output` / `window-idle`）时不需要这些标记。

把对应配置追加到 shell rc 文件末尾，并放在 Starship、Powerlevel10k、oh-my-zsh 等 prompt 初始化之后。

#### zsh

```zsh
# ~/.zshrc: mark commands for tuios run (OSC 133)
autoload -Uz add-zsh-hook
_tuios_osc133_precmd()  { local s=$?; print -n "\e]133;D;$s\a\e]133;A\a"; }
_tuios_osc133_preexec() { print -n "\e]133;C\a"; }
add-zsh-hook precmd _tuios_osc133_precmd
add-zsh-hook preexec _tuios_osc133_preexec
PS1+=$'%{\e]133;B\a%}'
```

#### bash

```bash
# ~/.bashrc: mark commands for tuios run (OSC 133)
# Needs bash 4.4 or newer. Older bash, such as macOS /bin/bash 3.2, ignores
# PS0, so it marks prompts and never commands, and run refuses the pane.
PROMPT_COMMAND='printf "\e]133;D;%s\a\e]133;A\a" "$?"'${PROMPT_COMMAND:+";$PROMPT_COMMAND"}
PS0='\e]133;C\a'
PS1+='\[\e]133;B\a\]'
```

macOS 自带的 `/bin/bash` 是 3.2，不支持这套 `PS0` 配置；请安装 bash 4.4+（例如 `brew install bash`）并把登录 shell 切到它，或直接用 zsh。

新增的 tuios pane 会自动生效；已经打开的 pane 需要重载：

```bash
source ~/.zshrc      # zsh；也可以 exec zsh
source ~/.bashrc     # bash
```

然后验证：

```bash
tuios doctor shell
```

对应当前 pane 的 `marks_commands` 应该是 `true`。旧 pane 在重载前通常是 `false`。

这里的 shell integration 和 `golder host-compat install` 是两件事：前者让 tuios 能识别命令边界，后者把 golder 的状态、审批和提问写进 tuios 的 harness manifest，供 rail 与 Inbox 使用。推荐两者都配置。

### 其他 Provider

可切换的内置网关都能用 `--provider <name> -m <model>` 选中，API Key 按 `<PROVIDER>_API_KEY` 约定从环境变量读取（如 `DEEPSEEK_API_KEY`、`ZAI_API_KEY`），完整列表见 `golder --help`（其中标注了唯一的例外：`typesafe` 是审查器用的决策模型，不接会话，只能用 `/proxy` 路由）。自定义端点直接指定协议与地址：

```bash
golder --provider deepseek -m deepseek-v4-flash -p "解释这段代码"
golder -P openai -u https://my-gateway.example.com/v1 -m my-model -k "$MY_KEY" -p "..."
golder -m ollama/qwen2.5-coder -u http://localhost:11434/v1 -p "..."   # 本地 Ollama
```

### 模型与推理档位

交互式里 `/model` 列出当前网关的模型并标注每个模型支持的推理档位；TUI 中选中模型后会接着让你选档位（Esc 取消整次切换），也可用 `/model <序号|id> <档位>` 一步切换。只调当前档位用 `/model think <档位>`（不带参数则显示当前档位），因为档位属于模型，入口统一在 `/model` 下。

**默认档位按 provider + model 保存**：`/model default <id|n> [档位]`（以及 TUI 选择器里在档位行按 `d`）把它写进 `config.toml` 的 `[thinking_defaults]`，键是 `provider/model`。给一个网关的某个模型设默认档位，不会改变同网关的其他模型，也不会影响其他 provider；`/provider` 切换只采用新网关默认模型自己的档位（没有保存过就保持当前会话档位），选择器里的 `(default)` 也只标记当前 provider+model 的这一项。

```toml
[thinking_defaults]
"opencode-go/deepseek-v4.1-flash" = "max"
"commandcode/deepseek/deepseek-v4.1-flash" = "high"
```

顶层的 `thinking_level` 仍作为未保存 per-model 档位时的全局回退；第一次保存 per-model 档位时，旧的全局值会迁移到 `[model_defaults]` 里每个已保存的 provider/model，然后删除顶层键。`--thinking-level` / `GOLDER_THINKING_LEVEL` 仍然优先于配置里的默认值。

`/provider` 列出全部内置网关：每个网关需要哪些环境变量、当前有没有配好（已配好的排在最前），`/provider <名称>` 直接切换网关（自动用该网关的默认模型）。审查器的决策模型（`typesafe`）单独列在最后一节，并拒绝切换：它回答的是审查器的类型化提问，不是聊天模型，给它路由请用 `/proxy`。TUI 里 `/provider` 是方向键选择器，同样不包括决策模型。

**默认 provider（新会话从哪个网关开始）**：`/provider default <名称>` 把它写进 `~/.golder/config.toml` 的 `provider` 行（`/provider default` 查看、`/provider default off` 清除）；如果配置文件里已经钉了 `model`，那一行会一起换成新网关自己的默认模型，避免把某个网关的模型 id 发给另一个网关。列表里带 `(default)` 的就是它，`(current)` 是当前会话用的那个——两者可以不同。TUI 选择器里按 `d` 直接把高亮的一行设为默认，不用退出列表。改文件只动这两行，注释和其余设置原样保留。

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
| `task` | 派发子 Agent；`context=fresh`（默认，带项目规则/git status 但不带父对话）或 `context=fork`（继承父对话与系统提示）；工具边界仍继承父级 |
| `memory_search` | 检索持久化记忆（BM25 全文索引） |
| `get_context_remaining` / `new_context` | 查看剩余上下文预算 / 主动开启新上下文窗口 |
| `schedule_create` / `schedule_list` / `schedule_delete` | 会话内定时提醒：到点后作为新消息回到对话（不持久化） |

`task` 的 `context` 由 agent 根据任务自行选择：独立 fan-out 用 `fresh`，继续父会话已经讨论过的工作用 `fork`。两者共享 cwd、文件树和父级授权边界，都保留完整系统指令（包括自定义 `--system-prompt`）；`fresh` 不带父对话，Git 简报最多读取 40 行/8KiB，超时或取消就跳过。`fork` 使用派发时的实时 model/provider、推理档位和压缩配置，但预算独立，不会改写父会话；继承历史已达阈值时会在第一次请求前压缩，最终仍只把报告带回父会话。

委派需要明确授权：只有用户或适用的 `AGENTS.md`/已加载的技能指示明确要求子 agent、委派或并行 agent 工作时才派；"更深入""更彻底""研究/调查""详细分析"都不算授权，拿不准就自己做。获准后也先做本地规划：立即阻塞下一步的活留给自己，只把具体、有界、能与你本地工作并行且真正推进主线的 sidecar 任务交出去；你自己能独立做完的整件事不算这种任务。不要重复劳动，同一未决问题不要反复派。并行编辑要划分不重叠的文件范围，父 agent 也不要同时修改子任务负责的文件；存在重叠时串行处理，或显式建立独立 worktree 后集成。子任务运行期间做有意义的非重叠工作，不要反射式等待，也不要重做它的事。父 agent 负责审查结果、集成改动和核对测试；这些提示不改变工具、审批或沙箱权限。

**持久记忆的每轮自动召回（默认关）**：除 `memory_search` 工具外，还有一条自动通道——把最近一条用户消息当查询做 BM25，命中就以 `<system-reminder>` 注入当轮请求（只进请求，不进历史、不上转写）。它默认**关闭**，因为注入是每轮都付的上下文成本：`/memory recall on|off` 开关（写 `config.toml` 的 `[memory] recall`，当前会话立即生效）。打开后有守卫：查询至少 2 个单元（英文词 / 中文二字组——"继续""ok"不算），命中至少共享 2 个单元；索引对账默认最多每 30 秒一次（`recall_reconcile_seconds`，`0` 为每轮、负数关闭），因为那一步是线性遍历记忆文件。`/memory` 报告会显示策略和最近一次决定（是否注入、查询原文、命中的文件），"为什么突然想起这个"因此有据可查。

`/goal` 运行期间还会额外挂载 `goal_complete` / `goal_blocked` 两个目标控制工具，仅在自主循环内可见。

## 斜杠命令

TUI 输入 `/` 会按下面的分类弹出菜单（↑↓ 选择）；`/help` 输出同一份分组列表。两类界面（TUI 与 REPL）命令完全对齐。

| 分类 | 命令 |
|------|------|
| General | `/help` `/status` `/exit` |
| Session | `/resume` `/compact` `/fork` `/clone` `/tree` `/rewind` `/export` `/import` |
| Model | `/model` `/provider` `/proxy` |
| Memory | `/memory` `/dream` |
| Permissions | `/permissions` `/trust` `/boundary` `/grants` |
| Modes | `/goal` `/btw` `/remote-control` |
| Extensions | 技能、插件、提示词模板按来源列在这里 |

要点：

- `/compact` 立即对上下文做一次摘要压缩；`/status` 内含会话 id、消息数、创建时间与上下文用量（原 `/session` 已并入）。
- **Ctrl+C 的一致行为**：运行中按一次即请求中断（会打印 `(interrupting…)`，随后以同一句 `Interrupted.` 结算）；同一次运行内再按不会重复提示，也不会提前退出。空闲时按一次清掉草稿（并提示已丢弃），输入框为空时第一次按下只"待命"（提示再按一次），第二次才退出——与 codex 一样，不设"运行中双击强退"，避免误触丢掉未落盘的一轮。Esc 与 Ctrl+C 的中断行为一致。
- **Ctrl+Z 挂起**：任何状态下按 Ctrl+Z 都把终端交还给它自己的作业控制（bubbletea 释放终端并发出 SIGTSTP），`fg` 回来时重新按当前尺寸排版——与 codex 及别的终端 UI 一致。
- **`!command` 直通**：输入框里以 `!` 开头的一行在本地直接执行——不调用模型、不走审批、不进沙箱——并以 codex 的 user 角色 `<user_shell_command>` 记录进入对话（命令、退出码、耗时，以及掐头去尾的输出），所以模型看到的正是你跑了什么、回来了什么。TUI 会先亮出一张流式 `• Running …` 卡片，结算成 `• You ran …`；运行途中落下的记录会先寄存、等这轮跑完再并入，不与持有上下文的主循环抢写。TUI 与 REPL 都有，↑ 召回的是你敲的那行原文，只有 `!` 时打印用法提示。
- 四个命令各答一件事：`/trust` 只说本目录能不能跑副作用工具，`/boundary` 只说工作区外哪些路径可读/可写，`/permissions` 只管审批档位，`/grants` 是整张表本身。`/grants list` 列出**全部**条目：按路径排序的每一条已保存的信任决定（当前目录标出）、本会话才有的授权、以及记下的拒绝，末尾写明它读的是哪个文件；`/boundary list` 打印两类路径授权（可读/可写）的来源与所在文件、以及怎么改。`/trust status` 仍然只回答"这个目录适用什么"。
- **resume 会用当前二进制重建系统提示**：续跑旧会话时，工具用法指南、环境块（工作目录、日期、rg 是否可用）、AGENTS.md 与技能清单都按**现在**重新生成，不再沿用会话文件里冻结的那份（旧会话里那份的日期停在创建当天，AGENTS.md 的改动也不生效）；而你自己写的部分——`--system-prompt` 的替换文本与 `--append-system-prompt` 的追加块——会原样保留，本次启动显式传入的 flag 优先。会话文件里仍记录创建时的原样 prompt，`/export --include-system-prompt` 看到的是它。
- `/resume`、`/fork`、`/tree`、`/rewind` 在 TUI 中都是方向键选择器，REPL 中输入序号选择。`/resume` 默认只列**当前项目**的会话（项目 = 启动目录所在仓库的根，子目录与根共享一份历史），列表里每条都带运行目录；`/resume --all` 列出所有项目，被过滤掉或超出范围时列表会给出提示并指到 `--all`。`--resume <id>` 与 `/resume <id>` 始终全局精确命中，不受项目过滤影响。
- 技能目录 `~/.agents/skills` 里每多一个技能就多一条 `/命令`；全新用户没有该目录时，Extensions 组为空。可用 `GOLDER_SKILLS_DIR` 换目录、`--no-skills` 全关。

## 使用

```bash
# 推荐：自带分屏终端工作区（agent / shell / 浏览器，Ctrl+b d 断开后继续跑）
golder workspace

# 已经有习惯的终端复用器（tuios / herdr / tmux / zellij / screen）：
# 直接在它的 pane/window 里跑单 agent，不必再套 workspace
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

# 在 tuios / herdr 里被认出来（见上「特性」）
golder host-compat status      # 当前宿主、以及缺什么
golder host-compat install     # 写 tuios 的 harness manifest（herdr 不需要）
golder host-compat uninstall   # 只删 golder 自己写的那份
```

`golder update` 的版本检查走站点接口（大陆可达、无速率限制），失败回退 GitHub API；下载 release 时与安装脚本同一条「直连 → 公益镜像」回退链，归档按 release 的 `checksums.txt` 校验（坏包自动换下一个源），而 `checksums.txt` 本身优先直连 GitHub，只有镜像应答时要求各来源一致（单源可用时告警后继续）；`GOLDER_MIRROR=off` 可禁用镜像。

### 批准之后的信封（envelope）

四档模式回答"准不准跑"，信封回答"在哪跑"；两者正交，并且**修订**了上面"获准执行的 bash 调用一律在沙箱内跑"的说法：

- 整条命令可完整枚举且不碰凭据的批准调用（`git push`、`go test`、`ls`、`cargo build` 等）直接在宿主上跑，卡片/REPL 标 `·unconfined`，结果 `details.envelope` 为 `bare`；
- 含变量/替换/进程替换、解释器或包装器（`bash`/`python`/`make`/`docker`/`env` 等）、凭据路径的仍收容（`sandbox`）；
- 原生文件工具（`read`/`grep`/`find`/`ls`/`view_image`/`apply_patch`）由 `toolworker` 的一次性沙箱执行时也标 `·sandbox`；`full-access`/Direct/无 runner 退回进程内时标 `·unconfined`；`todo`/`websearch`/`webfetch` 等不走 OS 沙箱的工具没有标记；
- `GOLDER_SANDBOX=enforce` 仍把每一条都收进沙箱，优先于信封；`full-access` 不变。

网络是另一个维度，并且**按次**决定：没有持久化的网络白名单，也没有单独的 `/network` 命令。需要联网的调用由模型在 `bash` 参数里声明 `network:"on`；这条能力请求和外部路径、沙箱外运行一起进入同一次能力决策。`auto` 下评审的 Allow/Confirm 直接携带声明的网络，Contain 只在评审同时授予网络时保留；`ask` 在同一个弹窗里问；`read-only` 拒绝；`full-access` 不问。信封只决定执行位置，不改变网络授权。

### 权限模式

改动型工具（`bash` / `apply_patch`）的审批由四档模式控制，启动时默认 `auto`，运行中用 `/permissions` 切换，也可用 `--permissions <mode>` 或 `GOLDER_PERMISSIONS` 指定：

| 模式 | 行为 |
|------|------|
| `read-only` | 只允许工作区内读取；写入、联网、沙箱外运行直接拒绝 |
| `ask` | 评审先过滤安全调用；其余调用把所有所需能力（外部路径、网络、沙箱外）合并成一次弹窗问你 |
| `auto`（默认） | 评审（JEV/local 优先，必要时 LLM）同时决定风险与能力扩张；`allow`/`confirm` 自动授予全部所需能力，包括沙箱拒绝后的路径补授，不打断 |
| `full-access` | 不审查、不询问、不套沙箱；仅保留静态硬拒名单（`sudo`、`rm -rf /` 等） |

边界、网络和沙箱不再各自弹窗：它们是同一次能力请求的不同维度。完整矩阵与实现见 [docs/permissions.md](docs/permissions.md)。旧版“边界任何档位都问、网络另问一次”的描述已被本表取代。

首次在某个目录启动时会询问是否信任该目录；`--approve` 可为单次运行直接授权。未信任目录里也可以只放行读取（`read` / `grep` / `find` / `ls` / `view_image`，本会话 × 本目录）：TUI 弹窗选 `Allow reads in this folder (this session)`，REPL 按 `a`；命令与联网读取仍逐次询问，`/trust off` 会连读授权一起撤销。**首启信任弹窗的按键**：`s` 记住、`p` 本会话、`n` 记住不信任、Esc 取消（不落盘）；**运行中的信任问句**另一套键：`y` 仅这一次、`a` 本会话允许本目录的读取、`p` 本会话允许全部工具——运行中这一问不落盘，持久化是启动选择器的职责。需要操作系统级隔离时参见 [docs/sandboxing.md](docs/sandboxing.md)。

**网络授权（统一能力模型）**：网络是能力请求的一维，不再单独弹第二次窗，也不再有“记住的 host/command 规则”。`auto` 的 `allow`/`confirm` 直接授予本次调用声明的网络；`ask` 把它和外部路径、沙箱外运行合并进同一个弹窗；`read-only` 拒绝，`full-access` 不问。一次批准只覆盖这一次调用；旧的 `network_grants.json` 与 `network.toml` 不再读取，可以删除。

### 常用开关

| 参数 | 说明 |
|------|------|
| `-p, --print` | 无头模式的 prompt（位置参数等价） |
| `-m, --model` / `--provider` | 选择模型与网关 |
| `-a, --approve` | 本次运行信任工作目录，跳过逐次确认 |
| `--thinking-level` | 推理档位：`off` / `minimal` / `low` / `medium` / `high` / `xhigh` / `max` |
| `--no-tools`、`--allowed-tools`、`--disallowed-tools` | 工具级准入控制 |
| `--no-skills`、`--no-prompt-templates` | 关闭技能 / 提示词模板发现 |
| `--no-tui` | 交互模式下改用行式 REPL（不使用全屏 TUI） |
| `--no-animations` | 关闭普通 TUI 的装饰动画；SSH 下默认关闭 |
| `--debug` / `GOLDER_DEBUG=1` | 打开诊断渲染（当前包括默认隐藏的 allow 审批行） |

完整参数见 `golder --help`。

## 目录与环境变量

| 路径 / 变量 | 用途 |
|-------------|------|
| `~/.golder/config.toml` | 全局配置（`provider`/`model` 可用 `/provider default <名称>` 写入） |
| `~/.golder/proxy.toml` | `/proxy` 保存的代理地址和 provider 选择 |
| `~/.golder/grants.json` | 唯一授权表（信任决定与路径读写授权） |
| `~/.golder/sessions` | 会话存储（JSONL） |
| `~/.golder/.credentials.yaml` | 命名的 API Key 凭据（建议 0600 权限） |
| `~/.agents/skills` | 技能目录（注册为 `/命令`；不存在则无扩展命令） |
| `GOLDER_HOME` | 覆盖 `~/.golder` 基础目录 |
| `GOLDER_SKILLS_DIR` | 覆盖技能发现目录（默认 `~/.agents/skills`） |
| `GOLDER_PROXY` | 为选中的 provider 提供代理地址，优先于 `/proxy` 保存的地址 |
| `GOLDER_SANDBOX` | 执行隔离：`off`（不启用）/ `auto`（默认，获准的 bash 默认收容；可完整枚举的信封直接跑）/ `enforce`（全部收容，且强制要求 runner） |
| `GOLDER_STREAM_MAX_RETRIES` | 默认 `4` 次重试（共 `5` 次尝试），连接失败、idle/EOF、文本续写和未执行的本地工具调用共用预算；`0` 禁用重试 |
| `GOLDER_STREAM_RESUME` | `0`/`false`/`off`：关闭已输出文本的续写；默认开。未完成的本地工具调用可安全重发，只在完整响应后执行一次；已由 provider 执行的服务端工具不自动重放 |
| `GOLDER_STREAM_FIRST_CONTENT_TIMEOUT` | 首个内容帧的等待预算（Go duration，默认 `2m`）；超时即重试，不再随尝试次数放宽 |
| `GOLDER_STREAM_IDLE_TIMEOUT` | 连续无任何字节的空闲窗口（Go duration，默认 `90s`）；SSE keep-alive 不计入内容进度 |
| `GOLDER_SANDBOX_READABLE` | 追加沙箱读白名单（冒号/逗号分隔的绝对路径），如 `/Volumes/KIOXIA:$HOME/miniconda3` |
| `GOLDER_READABLE_ROOTS` | 追加读取类工具（read/grep/find/ls/view_image）的免询问目录（冒号/逗号分隔），等同 `/boundary readable add`；同时放宽沙箱读白名单 |
| `GOLDER_MIRROR` | 自更新 / 安装脚本的下载镜像：空格分隔的 URL 前缀，或 `off` 禁用（默认直连 GitHub 失败后回退公益镜像） |
| `GOLDER_HOST_REPORT` | 宿主上报开关：`off` 关闭；`tuios` / `herdr` 强制指定（默认按 pane 环境自动判断，不在宿主里则不上报） |
| `GOLDER_DEBUG` | `1`/`true`/`on`：打开诊断 UI；当前用于显示默认隐藏的 allow 审批行 |
| `GOLDER_TUI_TRACE` / `GOLDER_STARTUP_TRACE` | 诊断 trace 文件路径（opt-in）。覆盖进程启动与 TUI、单一时钟、0600；只记录阶段/计数/耗时，不记录输入、路径或凭据。见 [TUI 延迟诊断](docs/tui-latency.md#startup-trace) |
| `GOLDER_NO_ANIMATIONS` | `1`/`true`/`yes`/`on`：关闭普通 TUI 装饰动画；检测到 `SSH_TTY` 或 `SSH_CONNECTION` 时也默认关闭 |
| `GOLDER_NO_TERMINAL_QUERIES` | `1`/`true`/`yes`/`on`：关闭普通 agent TUI 的自动颜色／键盘能力查询；SSH 默认关闭。显式剪贴板请求仍保留，外层 workspace client 不由此开关控制 |
| `OPENAI_BASE_URL` 等 `<PROVIDER>_BASE_URL` | 覆盖该 provider 的聊天和模型列表 API 地址 |
| `TAVILY_API_KEY` / `EXA_API_KEY` | 联网搜索后端凭证（Tavily / Exa） |
| `~/.golder/model-catalog.json`、`reasoning-catalog.json` | 模型列表 / 推理档位的 24h 缓存 |
| `OPENCODE_API_KEY`、`OPENCODE_ZEN_API_KEY` 等 `<PROVIDER>_API_KEY` | 各 Provider 的 API Key（`opencode-go` 用 `OPENCODE_API_KEY`，`opencode-zen` 只用 `OPENCODE_ZEN_API_KEY`） |

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=getan/golder&type=Date)](https://star-history.com/#getan/golder&Date)

## 许可证

[MIT](LICENSE)
