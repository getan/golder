# Golder GUI 体验、浏览器与瘦身优化路线图

> Status: Implemented in Golder; upstream raw browser-input capability remains an optional parity improvement
> Baseline: Golder v1.5.48、MyGo v0.7.4、kitty-browse v0.2.8  
> Scope: GUI 体验、共享配置、Session 管理、可操作浏览器、终端边界与发布体积

## 结论

Golder GUI 的最终定位是：

> **原生 Agent 对话界面 + 原生可操作浏览器 + 可选终端能力。**

它不是 TUI 的终端套壳，也不是 tuios workspace 的桌面容器。主对话继续使用当前已经验证的 MyGo Native UI Markdown、工具卡片、审批和 DiffView；设置、Session 和 Browser 逐步补齐成熟 Agent GUI 的可发现性。

最终边界：

```text
Golder GUI
├── Native Agent UI
│   ├── Session sidebar
│   ├── Markdown transcript
│   ├── Tool cards / approvals
│   ├── Changes / Diff
│   └── Composer
├── Native Settings
├── Native interactive browser surface
└── Optional MyGo terminal
    └── shell / TTY / full-screen programs / Kitty graphics

Golder CLI / TUI
└── tuios-mini workspace
    ├── Agent / shell / browser panes
    ├── detach / attach
    └── terminal workspace lifecycle
```

必须执行的架构调整：

1. GUI 和 CLI 共用同一个 launch/config/credential resolver；
2. 增加统一 Settings 页面，不再把 Provider 配置散落在 Composer 与启动参数中；
3. Session sidebar 显示工作目录并提供管理能力；
4. 收敛顶栏与 Composer 按钮层级；
5. Send 变成输入框内右下角的圆形按钮，运行时同位置切换成 Stop；
6. 从 kitty-browse 抽 terminal-independent Browser Surface，Bitmap 负责显示，CDP 负责输入；
7. 删除 GUI 的 Workspace 入口与 App 内第二份完整 `golder` CLI；
8. 保留 MyGo terminal，但只作为显式终端能力；
9. macOS 分 arm64 / amd64 发布，不再默认 universal；
10. GUI 版本号跟随 release tag，不再长期固定为 `0.1.0`。

---

## 当前问题与代码事实

### OpenAI credential 在 GUI 中误报未配置

GUI 当前只读取：

```go
key := firstNonEmpty(*apiKey, cfg.APIKey)
```

终端入口还支持：

```toml
credential = "openai-default"
```

并从 `$GOLDER_HOME/.credentials.yaml` 解析 named credential。GUI 漏掉这一层，因此终端可用而 GUI 报 missing key。

第二个差异来自 macOS 的进程环境：

```text
Terminal / zsh 启动 → 可能继承 .zshrc 中的 OPENAI_API_KEY
Finder 双击 .app   → launchd 启动，不读取 .zshrc
```

GUI 不应通过执行 `source ~/.zshrc` 解决这个问题；那会在桌面应用启动时执行任意 shell 代码。正确方案是共享 named credential store。

### Session 已有 Cwd，但界面没有显示

`appsession.SessionSummary` 已携带 `Cwd`，sidebar 当前只渲染标题、日期和 provider/model，因此不同项目的 session 无法区分。

当前 `session.Store` 也没有面向 GUI 的 rename/archive/delete API。GUI 不应直接 `os.Remove` JSONL 文件；这些能力必须落到 Store 层并继续遵守 writer lock。

### 按钮没有明确层级

当前有三组重复入口：

```text
顶栏：Session / Changes / Workspace / Browser
Composer：Model / Think / Mode
Empty state：Model / Permissions
```

Workspace 与 Browser 永久占据顶栏，Session 又同时出现在 sidebar，Model/Permissions 在空白页与 Composer 重复。用户无法判断哪些是页面级操作、哪些是当前 Session 状态、哪些是全局设置。

### Browser preview 目前只读

GUI 当前仅调用 `/v1/screenshot`：

```text
PNG → ui.Bitmap
```

Bitmap 只有像素，不含 DOM、焦点、键盘、滚动和命中目标。kitty-browse 的终端 pane 之所以可操作，是因为它在进程内同时持有 Chromium `browser.Session`、`render.Stream` 和 terminal input pump，并直接调用 MouseMove/MouseDown/MouseUp/KeyPress/SendText/SetViewport。

### App 体积主要由第二份完整 CLI 造成

v1.5.48 universal app 的实测构成：

| 组件 | Universal 大小 | 单架构 arm64 |
|---|---:|---:|
| `Contents/Resources/bin/golder` | 127.1 MB | 61.2 MB |
| `Contents/MacOS/Golder GUI` | 65.0 MB | 31.3 MB |
| `libghostty-vt.dylib` | 4.1 MB | 2.0 MB |

安装后约 187 MiB 的主要原因是：

1. 为 Workspace 按钮额外打包了一份完整 `golder` CLI；
2. universal 同时包含 arm64 与 x86_64；
3. GUI 主程序本身已进程内包含 Agent runtime、providers、tools、session 和 permissions，第二份 CLI 大量重复这些 Go 代码。

MyGo terminal / Ghostty VT 只占约 2 MB/架构，不是 200 MB 的原因。

---

## 目标架构

```text
┌─────────────────────────────────────────────────────────────┐
│ Golder GUI                                                  │
│                                                             │
│  MyGo Native UI                                             │
│  settings · sessions · transcript · tools · browser · tty   │
│                    │ typed commands / ordered events         │
│  gui/appsession.Controller                                  │
│                    │                                        │
│  shared CLI bootstrap/config/credential resolver            │
│                    │                                        │
│  run.SetupEnv · runtime.StartRun · run.Gates · session.Store│
└─────────────────────────────────────────────────────────────┘

$GOLDER_HOME/config.toml           non-secret shared settings
$GOLDER_HOME/.credentials.yaml     named secrets, mode 0600
```

UI 与业务层约束：

- `gui/mygoui` 只持有 view state；
- `gui/appsession` 不导入 MyGo；
- GUI 不解析 TUI screen 作为业务协议；
- GUI 不拼接 slash command 模拟按钮；
- Settings、CLI flags 与 config.toml 通过同一个 resolver 汇合；
- Session、Provider、permissions、browser 都通过 typed API；
- secrets 不进入 event、snapshot、log、session 或 MyGo preference。

---

## MyGo terminal 是什么，为什么保留

MyGo terminal 是 **MyGo 仓库和 Go module 内的可选插件包**：

```go
import "github.com/egoist/mygo/plugins/terminal"
```

它不是 Go 标准库，也不是 Golder 自己实现的 VT，更不是另一个独立仓库。插件包含 Go 侧的 PTY、input、selection 和 renderer，并通过 purego 在运行时加载 Ghostty 的原生库：

```text
libghostty-vt.dylib   macOS
libghostty-vt.so      Linux
ghostty-vt.dll        Windows
```

`mygo build` 读取插件自带的 `mygo-plugin.json`，下载、校验并把对应平台库放进 App；不使用 cgo。若 GUI 不 import `plugins/terminal`，MyGo 不会为它打包这份库。

保留它的场景：

- User Terminal；
- Agent 的 live TTY takeover；
- vim、htop、lazygit、yazi 等 full-screen 程序；
- ANSI、OSC、mouse reporting、IME、selection、scrollback；
- 命令输出的 Kitty graphics。

不使用它的场景：

- 主对话；
- Session 管理；
- Provider/Settings；
- 普通 tool card；
- Changes/Diff；
- 原生 Browser Surface；
- tuios workspace。

最终定位是“显式终端能力”，不是 GUI 基础设施。普通 shell tool 仍显示 native tool card；只有 TTY/full-screen/Kitty 场景出现 `Open Terminal`。

---

## 共享设置与 Credential 设计

### 一套数据，两种前端

CLI/TUI/GUI 共用：

```text
$GOLDER_HOME/config.toml
$GOLDER_HOME/.credentials.yaml
```

`config.toml` 只保存非敏感数据与 credential 引用：

```toml
provider = "openai"
model = "gpt-..."
base_url = "https://api.openai.com/v1"
protocol = "responses"
credential = "openai-default"
thinking_level = "high"
```

`.credentials.yaml` 保存真实值：

```yaml
openai-default: sk-...
anthropic-default: sk-ant-...
```

规则：

- mode 始终为 0600；
- key 只写不回显；
- 修改时原子写入；
- 删除 credential 前检查 config 引用；
- UI 仅显示 available/source/name；
- 不复制到 session、clipboard、log、diagnostics；
- Finder 启动时未继承的 env 必须显示为 unavailable，而不是静默猜测。

### 提取共享 resolver

将 `cmd/golder/main.go` 中现有的 config overlay、credential reference、provider/model canonicalization 抽到公共的内部包，例如：

```text
internal/cli/bootstrap
```

候选 API：

```go
type LaunchConfig struct {
    Provider, Model, BaseURL, Protocol string
    Thinking agentcore.ThinkingLevel
    Credential CredentialStatus
}

func ResolveLaunchConfig(Flags, config.FileConfig) (LaunchConfig, error)
func LoadCredentialStatus(provider string) CredentialStatus
func SaveNamedCredential(name, value string) error
func RemoveNamedCredential(name string) error
```

`cmd/golder` 与 `gui/cmd/golder-gui` 必须同时改用它，禁止再复制启动逻辑。

### Settings 信息架构

固定在顶栏最右侧的齿轮按钮打开完整 Settings 页面。

#### Providers

```text
Provider                         [OpenAI ▾]

Status
● Credential configured
  Source: openai-default

API Key                         [Replace…] [Remove]
Base URL                        [https://api.openai.com/v1]
Protocol                        [Responses ▾]
Default Model                   [gpt-… ▾]
Default Reasoning               [High ▾]

[Test Connection]               [Save as Default]
                                [Apply to Current Session]
```

`Save as Default` 修改未来 Session；`Apply to Current Session` 只在 idle 时可用。Test Connection 是显式网络操作，不在输入时自动执行。

#### Permissions & Sandbox

- 默认 permission mode；
- readable roots；
- writable roots；
- network policy；
- sandbox availability；
- 当前 Session 的临时模式仍由 Composer chip 控制。

#### Tools & Extensions

- Skills；
- MCP sources；
- setup/auth status；
- browser runtime；
- Lark 等 first-party package 状态。

Implemented follow-up: Settings → Extensions now manages the checksummed
first-party Lark helper, named App Secret reference, reviewed native CLI paths,
explicit fingerprint approval, browser authorization, connection testing,
enable/disable, reset and uninstall. See [`gui-extensions.md`](gui-extensions.md).

#### Terminal

- shell；
- font/size/theme；
- scrollback；
- copy-on-select；
- User Terminal 与 Agent Tool Terminal 的安全说明。

#### General / About

- appearance、language、updates；
- app/core versions；
- config root；
- Open Config Folder；
- Open Raw Config；
- diagnostics（不含路径、prompt、secret）。

---

## Session 侧栏与项目管理

### 目标布局

```text
golder
~/work/golder

  Fix login flow                         2h
  ~/work/golder · openai/gpt-…

  Review GUI                            Nov 2
  ~/work/golder · anthropic/claude-…

mygo
~/work/mygo

  Terminal rendering                    Nov 1
  ~/work/mygo · openai/gpt-…
```

要求：

- 默认按 project/Cwd 分组；
- sidebar 顶部是 Project selector 与 New Session；
- 默认只显示当前 project，可切 All Projects；
- path 使用 `~` 与尾部缩短，完整路径放 tooltip；
- row 明确显示 model/provider、时间、running/waiting/error；
- 当前 Session 有清晰选中态；
- 启动时异步加载，不阻塞首帧。

### 管理菜单

```text
Open
Rename
Reveal Project in Finder
Copy Session ID
Export Markdown
Export JSONL
Archive
Delete…
```

Store 层需要增加 typed API：

```go
Rename(id, title string) error
Archive(id string, archived bool) error
Delete(id string) error
```

限制：

- 当前运行 Session 不可删除；
- writer lock 存在时拒绝破坏性操作；
- delete 需要确认且报告实际文件；
- rename/archive 不改变 message tree；
- CLI 的 `session list/resume/export` 继续兼容。

可在 `SessionHeader` 增加 optional `DisplayName` / `ArchivedAt`，或使用独立 metadata index；不能把 UI 标题写成对话消息。

---

## 顶栏、导航与按钮层级

### 顶栏

从：

```text
Session | Changes | Workspace | Browser
```

收敛为：

```text
[Project ▾]  Session title                    [Changes 3] [⚙]
```

决策：

- Session 生命周期全部进入 sidebar；
- 删除 Workspace；
- Browser 仅在有活动 browser 时以 tab/drawer 或 tool-card action 出现；
- Command Palette 使用 `Cmd/Ctrl+K`，不永久占一个按钮；
- Settings 固定在最右侧；
- Changes 是唯一常驻的工作结果入口。

### Composer footer

```text
[Model] [Thinking] [Mode]                     Context 18%
```

这些表示当前 Session 的 live state。Provider 与 Model 合并到一个 picker；全局默认值只在 Settings 中编辑。

### Empty state

不再重复显示 Model/Permissions 按钮，只保留简短引导、最近项目或推荐任务。固定 Composer 始终可见。

---

## Composer 与内嵌圆形 Send

目标：

```text
┌──────────────────────────────────────────────────────────┐
│ Message Golder…                                          │
│                                                          │
│ [+]                                            [ ↑ ]      │
└──────────────────────────────────────────────────────────┘

[Model] [Thinking] [Mode]                     Context 18%
```

### 视觉与状态

- 输入容器：18 DIP 左右圆角、单一 border/background；
- TextArea：1–8 行自动增长，右下预留 48 DIP；
- Send：32–36 DIP 圆形、白色箭头；
- empty：灰色 disabled；
- ready：accent/深色 face；
- generating：同一位置切成 Stop，小方块图标；
- 不再在输入框外放一个 Send 或 Stop 文本按钮；
- accessibility label：`Send message` / `Stop generation`；
- tooltip：`Send (Enter)` / `Stop`。

### 键盘行为

```text
Enter            Send
Shift+Enter      Newline
Cmd/Ctrl+Enter   Optional send alias
Escape           Cancel run（IME composing 时不拦截）
```

MyGo 侧用无外观的 `ButtonBase` 组合圆形按钮，避免默认按钮 padding 破坏比例。TextArea 的 paste/image interception 后续可在同一容器左下增加 `+`，不扩展成第二条 toolbar。

---

## Native Interactive Browser Surface

### 目标

不能把可操作的 Kitty browser pane 降级成只读 screenshot。正确链路是：

```text
Chromium/CDP
  → screencast frame / PNG fallback
    → ui.Bitmap
      → MyGo native view

MyGo pointer / keyboard / IME
  → viewport coordinate mapping
    → browser surface
      → CDP input events
```

Bitmap 只负责显示，browser session/CDP 才负责控制。

### kitty-browse 当前能力

`browserhost` 公开 headless browser 与控制 socket（Info/Action/Screenshot/Quit），但不公开 frame/input API。`browsepane.Run` 拥有完整 terminal experience，内部已实现：

- `render.Stream.Frames()`；
- PNG still fallback；
- viewport fitting / resize recovery；
- MouseMove/MouseDown/MouseUp/Click；
- text/Backspace/Enter/Arrow；
- input conflict；
- tab/address/navigation chrome。

这些能力位于 `browsepane` 与 kitty-browse 的 `internal` 包，Golder 不能直接复用。因此改动应先发生在 kitty-browse，而不是在 Golder 复制一套 CDP renderer。

### 新公共包

建议增加：

```go
package browsersurface

type Surface interface {
    Frames() <-chan Frame
    Resize(width, height int, scale float32) error
    Pointer(PointerEvent) error
    Key(KeyEvent) error
    Text(string) error
    Back() error
    Forward() error
    Reload() error
    Navigate(string) error
    Close() error
}
```

两个 adapter 共用它：

```text
Browser Surface
├── Terminal adapter: frame → Kitty graphics; terminal event → input
└── MyGo adapter: frame → ui.Bitmap; native event → input
```

这样 terminal pane 行为不变，GUI 与 terminal 共用 Chromium、profiles、CDP、render health 和 input conflict。

### Native Browser UI

```text
┌─────────────────────────────────────────────┐
│ ←  →  ↻  https://example.com        Tabs  ⋯ │
├─────────────────────────────────────────────┤
│                                             │
│                 ui.Bitmap                   │
│                                             │
└─────────────────────────────────────────────┘
```

要求：

- pointer DIP → content rect → scale factor → viewport pixel；
- letterbox/zoom/scroll offset 必须参与映射；
- resize 后丢弃旧 viewport 的迟到 frame；
- text input 使用 CDP insertText；
- CJK IME 使用 composition/commit，不把拼音阶段逐字符发给页面；
- tabs、address、back/forward/reload 使用 native controls；
- active frame 使用 screencast，idle 使用现有 still probe；
- browser crash、profile lock、socket disconnect 有明确恢复状态。

### Agent 与用户并发控制

```text
Agent automation active
  → GUI 显示 “Agent controlling browser”
  → manual input blocked/queued

User Take Over
  → acquire manual-control lease
  → Agent automation pauses
```

复用 kitty-browse 现有 `InputBlocked` / conflict semantics。禁止 Agent click 与用户 drag 同时进入 CDP。

### 兼容性验收

在默认切换到 native browser 前必须覆盖：

- first frame、active stream、idle still、resize；
- click/double/right/hover/wheel/drag；
- input/textarea/contenteditable；
- Backspace/Enter/Arrow/shortcuts；
- CJK IME；
- tabs/address/navigation；
- iframe/canvas/fixed/sticky/device scale；
- profile/login/download/upload；
- agent/manual lease；
- crash/reconnect/quit。

迁移期间 terminal browser pane 是 fallback；当前 screenshot preview 必须标为 read-only，不能冒充完整浏览器。

---

## 删除 GUI Workspace 与第二份 CLI

GUI 不需要 tuios workspace。GUI Session 是对话历史，workspace session 是 terminal multiplexer 状态，两者概念不同。

删除：

- Workspace 顶栏按钮与 modal；
- `golder workspace attach` GUI 路径；
- App Resources 中的完整 `golder` CLI；
- CI 中编译 `resources/<os>-<arch>/bin/golder` 的步骤；
- GUI-only workspace discovery/list/capture UI。

保留：

- CLI/TUI 的 `golder workspace`；
- tuios-mini；
- agent 在真实 tuios pane 内注册的 workspace/tuios tools；
- GUI 的 conversation、tools、browser、terminal。

这不会影响 GUI 主对话、Provider、tool calls、approvals、sessions、changes、memory、MCP 或 browser surface。

---

## 体积与发布优化

### macOS 分架构

替换 universal：

```text
golder-gui-<version>-darwin-arm64.dmg
golder-gui-<version>-darwin-amd64.dmg
```

Apple Silicon 与 Intel 各下载自己的架构。Windows/Linux 继续按现有架构拆分。

### 体积目标

删除内置 CLI、保留 MyGo terminal、增加 browser surface 后：

```text
GUI 主程序                  ~31 MB（当前 arm64 基线）
libghostty-vt               ~2 MB
browser surface             数 MB
icons/config                可忽略
installed target            < 45 MB
DMG target                  < 25 MB
```

CI 应增加门禁，防止重新把完整 CLI 或 Chromium 打进 App：

- per-arch app < 50 MiB；
- DMG < 30 MiB；
- bundle 不含 `Resources/bin/golder`；
- bundle 不含 Chromium；
- terminal build 含且仅含对应架构 libghostty-vt；
- `go list -deps ./gui/cmd/golder-gui` 不包含 tuios-mini workspace renderer。

### Release version

`gui/mygo.json` 当前 version 固定为 `0.1.0`。tag build 必须把 `v1.5.x` 注入 MyGo config，产物名、Info.plist 与 About 页面都使用同一版本。

### macOS 签名

另一台 Mac 普通双击需要 Developer ID + notarization + staple。没有 Apple Developer Program 时只能通过 Privacy & Security 的 Open Anyway 或清除 quarantine；图标、DMG 和 zip 不能绕过 Gatekeeper。

---

## 主对话渲染保持不变

当前优秀的对话效果来自：

```go
markdown.Stream(c, text, options)
markdown.View(c, text, options)
editor.DiffView(c, state)
```

以及结构化 `AgentEvent → ItemTool/ItemNote/ItemAssistant`。它从未依赖 terminal screen。删除 Workspace、内置 CLI 或 browser Kitty renderer 不会改变主对话。

普通工具继续渲染 native card；只有以下场景进入 MyGo terminal：

- `tty=true` interactive process；
- full-screen TUI；
- 用户明确打开 shell；
- Kitty image output；
- manual takeover。

图片附件、browser frame 和 tool image result 直接渲染 `ui.Bitmap`，不先编码为 Kitty graphics。

---

## 安全与隐私不变量

- GUI 不执行 shell rc；
- key 不进入 config.toml 明文；
- key 不进入 session/event/log/clipboard；
- Settings 不回显 secret；
- Browser manual/agent control 需要 lease；
- browser profile 与普通 WebView profile 不混用；
- user terminal 不标记为 sandboxed Agent call；
- Agent terminal 只能接现有 gated exec session；
- external URL、base URL、protocol 与 credential source 都在 Go 侧验证；
- GUI 关闭时 pending approval fail closed；
- Session 破坏性操作遵守 writer lock；
- release bundle 不包含用户 config/profile/cookies/credentials。

---

## 分阶段实施

### Wave 1：正确性与信息架构

- 提取共享 launch/config/credential resolver；
- 修复 OpenAI named credential 与 Finder env 差异；
- 增加 Settings 页面；
- 重构顶栏、sidebar、Composer；
- Send 改为输入框内圆形按钮，运行时同位 Stop；
- 删除重复 Model/Permissions/Session 按钮。

验收：同一 `config.toml` + `.credentials.yaml` 下，CLI 与 GUI 对 provider/model/base URL/protocol/credential 的解析一致。

### Wave 2：Session 管理

- 显示 Cwd；
- project grouping / selector / All Projects；
- rename/archive/delete/export；
- running/waiting/error 状态；
- store API、writer lock 与兼容性测试。

验收：用户能仅凭 sidebar 判断每个 Session 的项目、状态和模型，并安全管理非活动 Session。

### Wave 3：瘦身与发布

- 删除 Workspace UI；
- 删除内置完整 CLI；
- 删除 CI resource CLI build；
- macOS arm64/amd64 分包；
- version 跟随 tag；
- 体积门禁；
- 保留 MyGo terminal。

验收：arm64 installed app < 45 MiB，DMG < 25 MiB，主对话/工具/Session/Browser 不依赖外部 CLI。

### Wave 4：kitty-browse Browser Surface

- 抽公共 surface；
- terminal adapter 改用 surface，保持现有 pane 行为；
- MyGo Bitmap adapter；
- pointer/key/text/IME/resize；
- native tabs/address bar；
- agent/manual control lease；
- parity and integration tests。

验收：Native Browser 完成兼容性矩阵后才取代 terminal browser fallback。

### Wave 5：Terminal 增强

- User Terminal；
- live TTY attach；
- tool card `Open Terminal`；
- Kitty image fixture；
- lifecycle、interrupt、close 与 accessibility；
- 清楚区分 User Terminal / Agent Tool Terminal。

---

## 明确不做

- 不通过隐藏终端注入 `/model`；
- 不为 GUI 新建第二套 config/credential store；
- 不执行 `.zshrc` 获取 key；
- 不用静态 screenshot 冒充可操作 Browser；
- 不在 GUI 嵌 tuios workspace；
- 不把第二份完整 CLI 塞进 App；
- 不打包 Chromium；
- 不用普通 MyGo WebView 代替受控 Chromium/profile；
- 不让 Workspace、Browser、Session、Settings 同时常驻顶栏；
- 不破坏当前主对话 Markdown/Diff/tool-card 路径。

---

## 最终验收摘要

| 领域 | 完成标准 |
|---|---|
| Provider | CLI/GUI 同源解析；named credential、URL、protocol、model 一致 |
| Settings | key 只写不回显；默认值与当前 Session 分层 |
| Session | 路径可见、按项目分组、可安全管理 |
| Composer | 输入框内圆形 Send/Stop，键盘与 IME 正确 |
| Buttons | 顶栏仅 Project/Title/Changes/Settings；无重复入口 |
| Browser | Bitmap frame + 双向 CDP input；与 terminal adapter 共用 surface |
| Terminal | 仅显式 PTY/TTY/Kitty 场景；不承载主对话/Browser/Workspace |
| Workspace | 只留在 CLI/TUI + tuios-mini |
| Packaging | 单架构、无内置完整 CLI、无 Chromium，installed < 45 MiB |
| Compatibility | CLI/TUI/session/tool/security 语义不回归 |

目标不是简单“把 GUI 做漂亮”，而是让每种能力只存在于一个清晰层级：主对话是语义 UI，Browser 是共享 CDP Surface，Terminal 是可选 PTY，Workspace 是 CLI/TUI 产品线，配置与 credentials 则由所有前端共享同一个事实源。
