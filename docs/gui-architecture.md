# Golder GUI 应采用 MyGo Native UI 的语义外壳，而不是把 TUI 包进终端

> Author: Golder maintainers
>
> Last updated: 2026-11-02
>
> Status: Draft / 设计建议，尚未实施
>
> Discussion: 本文档及后续实现 PR
>
> 调研基线：Golder 当前工作树、MyGo `e25ccb49`、GoRex `b206e67`

> 当前实现说明：GUI 已支持多个独立的 session controller 并行运行；切换会话不会停止原会话，后台审批仍绑定原会话，侧栏显示运行中、等待用户、完成或失败。会话各自持有上下文、工具和 writer 锁，但同一项目的并行会话默认共享工作目录，并不自动创建 worktree。关闭 GUI 会结束其后台任务；隐藏 Computer Use 面板仍会暂停桌面控制。以下有关 MVP 只允许一个活动 turn、切换时取消审批的描述是早期设计，不再适用。

## 摘要

我们建议新增独立的 `golder-gui` 桌面应用，使用 **MyGo Native UI** 绘制项目、会话、对话、工具调用、审批、模型选择和变更审阅；Agent 仍在进程内复用 Golder 现有的 `run`、`runtime`、`session`、permissions、sandbox 和 tool 实现。现有 `golder` CLI/TUI/workspace 二进制不链接 MyGo，不改变命令、存储格式或默认构建。

GUI 可以借鉴 GoRex 的窗口、卡片、命令面板和终端体验，但不复制它的 PTY、session server、split tree 或 VT。Golder 已经通过 tuios-mini 拥有这些能力，再引入 GoRex 后端会形成第二套 workspace。MyGo terminal plugin 只用于明确的终端场景；日常的模型选择、权限切换、会话恢复等操作调用 typed Go action，不能靠向隐藏 TUI 注入 `/model` 之类的字符。

一句话概括：**外观上是一个对终端用户友好的 Agent 工作台，架构上是 Golder core 的新前端，不是 TUI 的终端套壳。**

---

## 背景：终端套壳能很快出现，但会把 GUI 体验锁死在字符协议里

最直接的原型是：MyGo 打开一个 terminal view，在 PTY 中运行 `golder`，按钮点击后向终端发送 `/model`、`/permissions` 或方向键。这条路一天内就能看到结果，而且 MyGo terminal plugin 已经支持 Ghostty 的 VT、键盘、选择和 Kitty 图形协议。

它不适合作为产品架构。

例如“选择模型”不是简单发送一行文本：

```text
点击 Model 下拉框
  → terminal.Send("/model ...\r")
  → 希望焦点恰好在 composer
  → 希望没有审批框、picker、运行中输入或粘贴状态截获它
  → 再解析屏幕，猜测切换是否成功
```

这个链路把 UI 状态、业务状态和终端焦点混为一谈。窗口 resize、输入法、并发审批、TUI 文案调整、slash command 参数变化都会影响它。GUI 也无法可靠知道当前 model、permission、context usage 和 session writer，只能从屏幕反推。

Golder 实际上已经有比字符屏幕更稳定的边界：

- `internal/cli/run.SetupEnv` 组装 provider、tools、skills、memory、permissions 和 prompt；
- `runtime.StartRun` / `DrainStream` 产生文本、thinking、tool、usage、compaction 等事件；
- `run.Gates.Confirm` 已有结构化审批回调；
- `internal/session.Store` 已有版本化 JSONL、message tree、resume 和 redaction；
- `runtime.SlashRegistry` 已把 command 分成 prompt、action 和 hybrid，并携带名称、分类、来源和参数提示；
- `cli.LiveConfig` 已是 provider/model/thinking 等 live state 的共同来源。

所以新增 GUI 不需要把 TUI 变成协议。它只需要在这些边界外增加一个 UI-neutral controller，再由 MyGo 渲染。

---

## 目标和非目标

### 目标

1. 给不习惯 slash commands 的用户提供可发现的鼠标操作。
2. 保留键盘和 slash command 作为高效路径，而不是为了 GUI 删除它们。
3. 复用 Golder 的 Agent、安全、session 和 workspace 语义，不创建平行实现。
4. 使用纯 Go 的 MyGo Native UI，不使用 HTML、JavaScript、WebView 或 Electron。
5. 提供一套克制的 Agent GUI：项目、线程、聊天、工具、审批、模型和 changes 足够；不先造 IDE。
6. GUI 的失败、依赖和发布节奏不能阻塞 CLI/TUI。

### MVP 非目标

- 不重写 Bubble Tea TUI；
- 不把 `golder` TUI 当 GUI 后端协议；
- 不复制 tuios-mini 的 tabs、split panes、daemon 或 VT；
- 不内置完整文件树、代码编辑器、Git 客户端或多 Agent worktree 管理；
- 不在首版嵌入 Chromium 页面；
- 不在首版内嵌完整 tuios workspace；
- 不让 GUI 和 CLI 同时写同一个 chat session；
- 不追求所有 slash command 都有一个专用按钮。

---

## 我们从 GoRex、MyGo 和成熟 Agent GUI 中保留什么

### GoRex 适合参考交互和工程手法，不适合成为 Golder 的第二个 workspace

GoRex 给出了很好的 MyGo Native UI 样板：

- hidden title bar、跨平台窗口控制和原生菜单；
- tabs、pane cards、hover actions、拖动 divider；
- command palette 和完整快捷键；
- UI state 只在 MyGo main thread 更新，后台变化通过 `Window.Update` 汇入；
- headless view tests；
- terminal plugin 接一个 `io.ReadWriteCloser`，GUI 与 session server 解耦；
- app 关闭只 detach，server 保留 PTY 和 headless screen。

但 GoRex 的 `internal/rex` 同时负责 PTY、持久 session server、headless VT、attach stream、layout 和 foreground process。它与 tuios-mini 的职责高度重合。Golder 不应复制或导入 GoRex 的 internal code。

我们只复用它的设计语言和模式：title bar、command palette、卡片、活动状态、主线程更新、测试方式，以及 MyGo terminal 的连接方式。

### MyGo 已经覆盖 Golder GUI 最难的原生控件

当前 MyGo Native UI 已提供：

- flex/grid、sidebar、toolbar、tabs、dialog、popover、toast；
- text input、textarea、IME、selection 和 accessibility；
- virtualized list；
- `ui/markdown.Stream` 和完成态 Markdown；
- `ui/editor.DiffView` 与 code editor；
- headless view tests；
- system menu、dialog、notification、clipboard 和 window APIs；
- terminal plugin：libghostty-vt、PTY、selection、mouse、Kitty keyboard 和 Kitty graphics。

这改变了之前的技术选型。旧方案选择 Wails 是因为长 transcript、Markdown、diff 和终端在传统 Go GUI 中成本很高；MyGo 现在直接提供了这些能力，因此继续引入 WebView/HTML/JS 已没有明显收益。

### 成熟 Agent GUI 的共识比视觉样式更值得借鉴

Codex、Zed Agent Panel 和 VS Code Agent 的共同模式是：

- project 下组织 thread/session；
- transcript 是主视图，composer 是主操作入口；
- model 和 mode selector 靠近 composer；
- tool call 默认显示摘要，细节按需展开；
- approval 就地显示输入和风险，允许一次、会话级或拒绝；
- changes 用一个数量/stat pill 汇总，点击进入 diff review；
- terminal 是可展开的工具细节或独立 thread，不是所有业务状态的载体；
- command palette 收纳低频操作；
- context usage、compaction 和 session 状态可见但不过度抢占空间。

Golder MVP 采用这些交集，不采用第一版就多 Agent 并行、worktrees、automations、云同步和完整 IDE 等高复杂度能力。

---

## 推荐界面：一个主窗口、三个层级、一个可选抽屉

```text
┌──────────────────────────────────────────────────────────────────────┐
│ Project ▾   Session title                         Changes 3   ⋯      │
├────────────────┬─────────────────────────────────────────────────────┤
│ + New session  │                                                     │
│                │  User message                                      │
│ Today          │                                                     │
│  Fix login     │  Assistant markdown                                │
│  Review API    │  ┌ Tool: go test ./...             finished  ▸ ┐   │
│                │  └───────────────────────────────────────────────┘   │
│ Earlier        │                                                     │
│  …             │  ┌ Approval required                           ┐   │
│                │  │ git push …  [Deny] [Once] [This session]    │   │
│                │  └─────────────────────────────────────────────┘   │
│                │                                                     │
│                │  ┌ Message…                               Send ┐   │
│                │  └─────────────────────────────────────────────┘   │
│                │  Model ▾  Thinking ▾  Ask ▾  Context 41%           │
└────────────────┴─────────────────────────────────────────────────────┘
```

### 左侧只放项目和会话

首版 sidebar 只做：

- 当前 project；
- new session；
- recent sessions；
- running / waiting approval / failed 状态；
- 搜索和 archive 可以后置。

不要在这里同时塞文件树、Git、skills、MCP 和 workspace pane。低频配置进入 Settings 或 command palette。

### 中间 transcript 直接表达 Agent 语义

消息列表使用 MyGo `ui.List`；模型输出流式阶段使用 `markdown.Stream`，完成后使用 `markdown.View`。工具调用显示为可折叠 card：

- header：状态、动作摘要、sandbox/host 标记、耗时；
- collapsed body：最多几行 preview；
- expanded body：参数、完整有界输出、review note、错误；
- bash/TTY card 可出现 “Open Terminal” 或 “Show live output”；
- file edit card 可出现 “Review change”。

Thinking 默认折叠。并行 tool calls 保持事件顺序，但不把每个 token 都变成一次 layout。

### Composer 周围放最高频状态

model、thinking、permission mode 和 context gauge 放在 composer footer，因为它们会直接影响下一条消息。用户点击后调用 typed action；状态变化立即从 controller snapshot 回写，而不是等待下一轮 transcript 文案。

### Changes 是一个 drawer，不是完整 IDE

标题栏或 composer 上方显示 `Changes N · +A −D`。点击打开右侧 drawer：

- 文件列表；
- MyGo `editor.DiffView` 的 unified / split 模式；
- open in external editor；
- 首版只 review，不承诺逐 hunk “accept” 能撤销任意 shell 产生的改动。

Golder 的 rewind/checkpoint 与 Git diff 是不同概念，UI 必须用不同文案，不能让用户误以为撤销文件会逆转网络、部署或命令副作用。

---

## slash command 不消失，但鼠标不应伪装成 slash command 输入

### 高频命令得到明确控件

| 现有命令/概念 | GUI 入口 |
|---|---|
| `/model` | composer footer 的 Model picker |
| `/provider` | Model picker 内的 Provider 分组 / Settings |
| `/think` | Thinking picker |
| `/permissions` | shield/mode picker，危险模式显著标色 |
| `/boundary` | Settings ▸ Paths，使用系统目录选择器 |
| `/resume` | session sidebar |
| new session | sidebar 的 `+` |
| cancel / interrupt | composer 的 Stop button |
| `/compact` | context gauge 菜单 |
| `/rewind` | message context menu，附影响范围确认 |
| `/extensions`, `/skills` | Settings / command palette |
| `/help` | command palette 和 Help menu |
| `/status` | session info sheet |
| user prompt / skill command | composer 的 `/` autocomplete 与 command palette |

### 低频和动态命令进入 Action Catalog

`runtime.SlashCommand` 已提供 Name、Description、Category、ArgumentHint、Source 以及 prompt/action/hybrid 分类。GUI 应在它上面提供一个 UI-neutral `ActionCatalog` adapter：

```go
type Action struct {
    ID, Label, Category, Description string
    ArgumentHint, Source             string
    Availability                     Availability
}

type ActionRunner interface {
    ListActions(context.Context) []Action
    RunAction(context.Context, ActionRequest) (ActionResult, error)
}
```

这不是新命令系统。它让 command palette、menu 和 slash composer 使用同一 registry。对于 model、permission、resume 等高频操作，controller 暴露 typed method，并让 slash handler 与 GUI method 共享同一个 use-case function；不要让 GUI 拼接字符串再调用 parser，也不要让 TUI 被迫迁移到 GUI controller。

用户仍可直接输入 `/skill-name`、自定义 prompt template 或 plugin command。GUI 只是把“必须记住命令名”变成“可以搜索动作”。

---

## 推荐架构：GUI adapter 很薄，application controller 不知道 MyGo

```text
┌─────────────────────────────────────────────────────────────────┐
│ golder-gui                                                      │
│                                                                 │
│  MyGo Native UI                                                 │
│  sidebar · transcript · composer · picker · approval · diff     │
│                 │ commands               ▲ snapshots/events      │
│  ┌──────────────▼────────────────────────┴───────────────────┐  │
│  │ internal/guimygo                                          │  │
│  │ main-thread dispatch · view state · dialogs · menus        │  │
│  └──────────────┬────────────────────────▲───────────────────┘  │
│                 │ typed calls             │ ordered events       │
│  ┌──────────────▼────────────────────────┴───────────────────┐  │
│  │ internal/appsession.Controller                           │  │
│  │ one session owner · live config · approvals · persistence│  │
│  │ action catalog · cancellation · bounded event coalescing  │  │
│  └──────┬─────────────┬──────────────┬──────────────┬────────┘  │
│         │             │              │              │           │
│   cli/run         runtime         session       slash/actions    │
│   gates/tools     event loop      JSONL/tree    live config      │
└─────────┼─────────────┼──────────────┼──────────────┼────────────┘
          └──── existing providers / sandbox / trust / hooks ─────
```

### Controller 只编排，不重新实现 Agent

建议的能力边界：

```go
type Controller interface {
    OpenProject(context.Context, string) error
    NewSession(context.Context, NewSessionRequest) error
    ResumeSession(context.Context, string) error
    Submit(context.Context, SubmitRequest) error
    Cancel(runID string)

    ListModels(context.Context, ModelFilter) ([]ModelOption, error)
    SelectModel(context.Context, ModelSelection) error
    SetPermissionMode(context.Context, string) error
    RespondApproval(context.Context, ApprovalResponse) error

    ListActions(context.Context) []Action
    RunAction(context.Context, ActionRequest) (ActionResult, error)
    Snapshot() Snapshot
    Events() <-chan Event
    Close() error
}
```

它必须满足：

- 一个 session 的 Agent context 只由一个 owner goroutine 驱动；
- event channel 有界，满时背压，不丢 final、approval 和 tool end；
- text/thinking delta 可按约 16–50ms 合并；
- 所有事件带 session ID、run ID 和递增 seq；
- MyGo view state 只在 main thread 通过 `Window.Update` 修改；
- window 关闭、session 切换或 run cancel 时，待审批请求 fail closed；
- controller 不导入 `mygo`、`ui`、Bubble Tea 或 terminal 类型。

### TUI 无需迁移

现有 TUI 继续使用自己的 `runSession`、Bubble Tea bridge 和 renderer。为了让 GUI 复用 use case，允许把少量真正 UI-neutral 的逻辑从 TUI 文件移到 `internal/cli` 或 `internal/appsession`，但必须保持原调用路径和行为测试。

这比“先统一所有前端”更安全。等 controller 稳定后，是否让 TUI 复用它是另一个设计决策，不是 GUI 的前置条件。

---

## 终端、VT 和 Kitty 图形协议应分层处理

### 当前能力有三处重叠，但它们服务不同边界

| 能力 | tuios-mini | MyGo terminal | GoRex | Golder GUI 决策 |
|---|---|---|---|---|
| PTY 生命周期 | 有，workspace daemon 所有 | 有，可直接运行程序 | `rex` server 所有 | workspace 继续归 tuios；GUI-owned terminal 才用 MyGo |
| VT emulator | `tuios.dev/vt` | Ghostty `libghostty-vt` | server/client 都用 Ghostty | 不在 GUI 再实现 VT |
| tabs/splits | 有 | 仅 view，无 workspace policy | 有 | MVP 不复制；沿用 tuios workspace |
| detach/reattach | 有 | Conn seam 可支持 | 有 | 沿用 tuios；不创建第二个 server |
| pane control/security | 有，且 Golder 已有 proof/grants | 无 | 本地自有协议 | 沿用 tuios |
| Kitty graphics | 现有 terminal/browser 路径依赖它 | 完整解析和原生绘制 | 通过 MyGo | 独立 MyGo terminal 可直接使用；workspace attach 要单独验收 |
| Agent 语义 | 无 | 无 | 无 | 始终归 Golder controller |

### MyGo terminal 已满足独立终端的 Kitty 要求

MyGo terminal plugin 的 `Options.ImageLimit` 控制 Kitty image 存储；renderer 支持普通 placement、z-order，以及 yazi 等程序使用的 Unicode placeholder virtual placement。因此 GUI 自己创建的 terminal view 不需要再引入 xterm.js，也不需要编写 Kitty parser。

这项结论只适用于 **MyGo terminal 自己消费 PTY/raw stream** 的场景。

### 嵌入现有 tuios workspace 不能直接宣称“已经复用”

tuios daemon 已经用自己的 VT 保存 screen。若 GUI 再把 raw bytes 喂给 MyGo/Ghostty，形成 server VT + client VT 的双 emulator；GoRex 也采用双 VT，但两端同属 Ghostty，并有专门 snapshot/attach 协议。tuios 与 MyGo 使用不同 emulator，cursor、reflow、alternate screen、OSC 和 Kitty image 生命周期可能漂移。

因此 MVP 采用以下边界：

1. GUI 可以通过现有 `tuiosctl` 列出 workspace/panes；
2. 可以显示 `capture` 文本或 `screenshot` 只读 preview；
3. “Open Workspace” 启动或聚焦现有 `golder workspace` 终端客户端；
4. 不在首版把 tuios raw stream 接入 MyGo terminal。

若真实用户需求证明必须嵌入，先在 tuios-mini 设计 public mirror attach protocol：

- replayable full snapshot + 后续 raw bytes；
- boot ID、sequence 和 gap resync；
- resize ownership；
- scrollback 策略；
- Kitty image data/placement 的 snapshot 语义；
- disconnect/reconnect 和 bounded frame；
- 与 `terminal.Options{Conn: ..., ResizeByConn: true}` 的契约测试。

只有 cursor、wide text、IME、alternate screen、resize reflow、mouse、OSC 8/52 和 Kitty image fixture 全部通过后，才把 embedded workspace 列为支持能力。否则继续外部打开；这不是失败，而是避免两套 terminal 看似相同、实际不一致。

### 浏览器 preview 不必绕 Kitty

GUI 是原生 bitmap surface。未来若展示 Golder browser，可以从现有 browser control API 取得 PNG 和 accessibility snapshot，直接绘制 `ui.Bitmap`；不需要先编码成 Kitty escape 再由 terminal 解码。手动网页交互和 profile 权限仍由 browserhost/control socket 负责，不能把 MyGo 的普通 WebView 当受控浏览器替代品。

---

## 审批和安全必须保持 Golder 语义

审批 card 显示结构化的 tool、summary、scope、network、host-control 和 reviewer rationale。常用按钮为：

- Deny；
- Allow once；
- Allow for this session（仅现有 gate 支持时）；
- remembered grant（仅现有持久策略支持时）。

GUI 不自行推导“这个命令安全”，也不把 tool registry 暴露成通用 exec API。所有 Agent 文件、命令、browser 和 workspace 操作仍经过现有 tools + gates。

硬约束：

- GUI 关闭、失联或切换 session 时，pending approval 返回 unanswered；
- permission mode 与 `permissions.State` 是同一个 live object；
- path picker 选择路径不等于授予权限，grant 仍走 boundary action；
- API key、Cookie、完整环境和 browser profile 不进入 view state；
- remote Markdown、tool output 和 filenames 只是数据，不能生成可执行 action；
- external links 使用系统打开并明确显示目标；
- GUI-owned user terminal 与 Agent tool terminal 在视觉上明确区分，前者不能伪装成经过 sandbox 的调用。

---

## Session、项目和并发写入保持兼容

GUI 直接使用 `internal/session` 的 JSONL 和 tree：

- GUI 创建的 session 可由 CLI `--resume`；
- CLI 创建的 session 可由 GUI resume；
- stored model/provider 仍是历史元数据，resume 使用当前 live config；
- system prompt 按现有 `ResumeSystemPrompt` 规则重建；
- redaction、hooks 和 compaction records 不变；
- 不新增 SQLite 或 GUI-only conversation 格式。

同一 session 同时打开时只能有一个 writer。首版最简单的规则是进程级/file lock：第二个前端以只读方式打开或要求用户显式关闭 owner，绝不做 last-writer-wins。不同 session 可以并行，但 MVP UI 只运行一个 active Agent turn，以控制复杂度。

---

## 目录、构建和发布应把 GUI 隔离成独立产物

建议目录：

```text
cmd/golder-gui/
    main.go                    // //go:build gui
internal/appsession/
    controller.go             // 无 MyGo/Bubble Tea import
    events.go
    actions.go
    approvals.go
    sessions.go
internal/guimygo/
    app.go                    // //go:build gui
    view.go
    transcript.go
    composer.go
    changes.go
    terminal.go              // 后续阶段
```

发布物：

```text
golder                 现有 CLI/TUI/workspace，行为和体积预算不变
Golder GUI.app          macOS
golder-gui.exe          Windows
golder-gui              Linux desktop package
```

MyGo 当前和 Golder 一样要求 Go 1.27.3，并使用 purego 而非 cgo。terminal plugin 的 `libghostty-vt` 是运行时加载的 native library，必须由 MyGo build/plugin manifest 正确打进应用或按其受校验的缓存机制提供；“no cgo”不等于“没有平台 native artifact”。

GUI 文件和 MyGo imports 放在 `gui` build tag 下。默认 gate 必须继续通过：

```text
go test -race ./...
go build ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...
```

GUI 使用独立矩阵和 MyGo packaging。MyGo 版本固定到精确 tag/commit，升级单独评审。首版保留同一个 Go module，避免 internal import、双 tag、`replace ../` 和 core/GUI 版本漂移；只有当 application API 稳定为公开包时再考虑拆 module。

---

## 实现分四阶段，每一阶段都可以独立停止

### Phase 0：验证 MyGo，而不是验证“能打开一个终端”

做一个可删除 spike：

- Native UI window、sidebar、virtual list、composer；
- `markdown.Stream` 显示模拟或只读 Agent stream；
- model/permission picker 的静态交互；
- headless view test；
- terminal plugin 单独运行 Kitty graphics fixture；
- macOS package，Windows/Linux build smoke；
- 测量启动时间、空闲内存、长 transcript、CJK/IME 和 accessibility。

可以额外做一个“在 MyGo terminal 里运行 golder TUI”的 throwaway demo，用来验证 Bubble Tea、鼠标和 Kitty 图像，但代码不得进入产品路径。这个 demo 的成功只证明 terminal plugin 可用，不证明按钮注入 slash command 的架构可维护。

继续到 Phase 1 的门槛：Markdown stream、万级 message block 虚拟化、IME、clipboard、窗口恢复、打包和 terminal native library 都没有阻断问题。

### Phase 1：可发布的语义 GUI MVP

- `appsession.Controller`；
- project picker 和 recent sessions；
- new/resume、submit、stop；
- transcript、thinking、tool cards、review notes；
- queued approval cards 和 fail-closed lifecycle；
- provider/model/thinking picker；
- permission mode 和 context usage；
- changes summary + diff drawer；
- command palette 和 slash autocomplete；
- system menu、shortcuts、notification；
- macOS 首发，Windows/Linux smoke 后再声明支持。

MVP 不含 embedded terminal/workspace/browser、多 Agent、worktree、内置 editor 和自动更新。

### Phase 2：增加终端逃生口和 workspace 管理，不复制 tuios

- tool card 打开 live TTY view（必须通过现有 exec session/sandbox ownership）；
- 可选的独立 user terminal，清楚标记不属于 Agent tool；
- workspace list/status/start/stop；
- pane capture/screenshot preview；
- “Open in Terminal Workspace”；
- browser PNG/accessibility preview spike；
- session search、archive、rewind 和更完整的 changes review。

MyGo terminal plugin 在本阶段作为 renderer 使用，不引入 GoRex server。

### Phase 3：需求证明后再评估 embedded workspace

只有用户频繁在 GUI 与 workspace 间切换，且外部窗口造成明确体验损失时，才实现 tuios mirror attach protocol。它是 tuios-mini + MyGo 的跨仓库能力，不应藏在一个 Golder adapter 中。

---

## 理由与取舍

### 我们选择语义 GUI，而不是最快的 PTY wrapper

PTY wrapper 的优点是改动少、功能看起来完整、TUI 新功能自动出现。它的代价是所有 GUI action 都依赖焦点和字符协议，无法做可靠状态、原生审批、diff review 和 accessibility。我们保留它作为验证 terminal compatibility 的 spike，不作为产品基础。

### 我们选择 MyGo Native UI，而不是继续旧文档的 Wails 方案

Wails 对 HTML transcript 很成熟，但会引入 WebView、HTML/CSS/JS 和另一套前端安全边界。MyGo 已有 native Markdown stream、virtual list、diff/editor、terminal、accessibility 和 desktop packaging，且 Golder 与 MyGo 的 Go toolchain 已对齐。现在采用 MyGo 能满足纯 Go 目标，并减少桥接层。

代价也必须承认：MyGo 年轻、API 变化快，复杂文本和三平台行为需要真实验证，terminal plugin 还有 native library 发布面。因此 Phase 0 是 release gate，不是假定它一定可用。

### 我们不复用 GoRex backend，因为 Golder 已经有 tuios-mini

复制 `rex` 能很快得到漂亮 split terminal，但会同时存在两套 daemon、PTY、layout、attach、VT 和 session state。用户会遇到“这个 terminal 在哪套 workspace”“关闭 GUI 是否结束 pane”“agent tool 能否控制它”等新概念。我们只借 GoRex 的 UI 模式和 MyGo 用法。

### 我们不立即统一 TUI 和 GUI controller

强迫 TUI 迁移会扩大回归面，而且 Bubble Tea model 已包含 terminal geometry、selection、input、renderer 和异步消息。GUI 首先复用 core seam；以后若 controller 被证明稳定，再单独评估 TUI 迁移。

### 我们保留 slash commands

专用控件覆盖高频命令，但 skills、prompt templates 和 plugins 是动态的。删除 slash commands 会牺牲可扩展性和高级用户效率。Action Catalog 让同一个 registry 同时服务点击和输入，比为每个扩展发明 GUI schema 更简单。

---

## 风险和缓解

| 风险 | 缓解 |
|---|---|
| MyGo API 快速变化 | 固定精确版本；所有 import 收口到 `internal/guimygo`；升级单独 PR |
| Native UI 在长文本/IME/a11y 上有缺口 | Phase 0 真实 CJK、screen reader、selection、长 transcript gate |
| terminal plugin native library 漏打包 | 三平台 package smoke；离线启动测试；校验 plugin manifest 和 hash |
| GUI 和 TUI 编排逻辑漂移 | 只把 UI-neutral use case 提取为共享函数；core event/gate/session 契约测试 |
| 两个 VT 对同一 workspace 解释不同 | MVP 不嵌入；后续 mirror protocol + differential fixtures |
| GUI action 与 slash command 行为不同 | Action Catalog 复用 registry；高频 action 与 slash handler 调同一 use case |
| session 双写损坏 JSONL | writer lock；第二个前端只读或拒绝；崩溃恢复测试 |
| approval 在窗口关闭时悬挂 | controller 拥有 pending map；context/window close 全部 unanswered |
| tool output 让 UI 卡顿或爆内存 | bounded event、delta coalescing、output cap、virtual list、lazy detail |
| GUI 依赖污染 CLI binary | 独立 command、build tag、artifact 和 CI；CLI size/import graph gate |
| GUI-owned terminal 绕过 Agent 安全语义 | 显式标成 User Terminal；Agent terminal 必须来自 gated exec session |
| 过早变成 IDE | MVP 非目标和单窗口布局固定；changes 只 review，外部 editor 负责编辑 |

---

## 验收标准

### 兼容性

- 不带 `gui` tag 的 `golder` 行为、imports、体积和测试矩阵不变；
- CLI 和 GUI 可以互相 resume session；
- session 同时写入会被拒绝，而不是静默竞争；
- model/provider/thinking、hooks、redaction 和 compaction 语义一致；
- slash 输入、command palette 和鼠标控件对同一 action 得到同一结果。

### 安全

- ask/read-only/auto/full-access 与 CLI 共用 `permissions.State`；
- GUI 能回答现有 approval scopes，关闭时 fail closed；
- tool call、path grant、network 和 host control 继续经过现有 gate；
- view state 不含 secret、Cookie 或完整环境；
- user terminal 与 sandboxed Agent command 的边界在 UI 和实现中都不可混淆。

### 体验

- 新用户不输入 slash command 也能完成：选项目、新会话、选模型、发送、审批、停止、resume、查看 changes；
- tool output 默认不淹没对话，展开后仍可完整检查有界结果；
- model、permission 和 context 状态在 composer 附近可见；
- keyboard 用户可用 command palette 完成所有鼠标高频 action；
- CJK 输入、文本选择、复制、screen reader label 和 focus order 可用。

### 性能和终端

- token delta 合并后不按 token 重排整个 transcript；
- 数千消息/tool block 可滚动，历史细节按需构建；
- 空闲窗口无持续 repaint 或高 CPU；
- 独立 MyGo terminal 通过 Kitty image placement、virtual placeholder、wide text、resize 和 alternate-screen fixtures；
- embedded tuios workspace 在 mirror 协议验收前不标记为支持。

---

## 最终建议

推荐路径是：

```text
MyGo Native UI
+ UI-neutral appsession controller
+ existing Golder run/runtime/session/gates
+ Action Catalog over the existing SlashRegistry
+ optional MyGo terminal renderer
+ existing tuios-mini workspace as the only workspace backend
+ separate golder-gui artifact
```

不要走的路径是：

```text
MyGo terminal
  └─ run golder TUI
       └─ mouse click injects "/model ..."
            └─ parse screen to recover state
```

后者适合 demo，前者才是可维护的 GUI。这样既能得到 GoRex 那种轻量、漂亮、支持 Kitty 的 native terminal 体验，又不会让 GoRex 与 tuios-mini 重复，也不会为了 GUI 重写 Golder 已经成熟的 CLI/TUI 核心。

---

## 参考资料

- MyGo repository: <https://github.com/egoist/mygo>
- MyGo terminal plugin: <https://github.com/egoist/mygo/tree/main/plugins/terminal>
- GoRex repository: <https://github.com/egoist/gorex>
- Zed Agent Panel: <https://zed.dev/docs/ai/agent-panel>
- VS Code — Use tools with agents: <https://code.visualstudio.com/docs/agents/run/tools>
- VS Code — Review and revert agent changes: <https://code.visualstudio.com/docs/agents/run/review-code-edits>
- OpenAI — Introducing the Codex app: <https://openai.com/index/introducing-the-codex-app>
- Golder integrated workspace boundary: [`workspace-m4.md`](workspace-m4.md)
- Golder pane/tool boundary: [`panes.md`](panes.md)

---

## 实现状态（2026-11-02）

第一阶段已经落地，代码全部在仓库的 `gui/` 目录，作为独立的 `golder-gui` 命令；`cmd/golder` 不导入它。

```text
gui/
├── appsession/          UI-neutral controller（不导入任何 GUI 包）
│   ├── controller.go    Open/Close/Snapshot/Events
│   ├── run.go           Submit/Cancel/run 装配/gate 接入
│   ├── approvals.go     审批队列与 fail-closed
│   ├── sessions.go      persist/new/resume/list/writer lock
│   ├── actions.go       SlashRegistry → Action Catalog、/compact
│   ├── models.go        provider/model/thinking 切换
│   ├── changes.go       git status + unified diff
│   ├── workspace.go     tuios workspace 发现、pane 列表、capture
│   ├── browser.go       browser 控制 socket 的 PNG 预览
│   └── lock.go          session writer 锁（跨进程）
├── mygoui/              MyGo Native UI
│   ├── view.go          title bar、session sidebar、transcript、composer
│   ├── transcript.go    Markdown、thinking、tool card、gate note
│   ├── approvals.go     内联审批卡片
│   ├── modals.go        model / reasoning / permission picker
│   ├── palette.go       command palette
│   ├── changes.go       右侧 diff drawer（editor.DiffView）
│   └── terminal.go      工作区终端窗口 + browser PNG 预览
├── cmd/golder-gui/      main（flag、config.toml、run.SetupEnv）
├── mygo.json            MyGo 打包配置
└── README.md
```

已实现且与本文一致：

- 进程内复用 `run.SetupEnv`、`runtime.StartRun/DrainStream`、`run.Gates`、`session.Store`、`permissions.State`、`SlashRegistry` 和 hooks；
- 模型、reasoning、权限模式都是 typed action，不向终端注入 `/model`；
- 审批在窗口内回答，窗口关闭时全部按 unanswered 处理（fail closed）；
- session writer 锁拒绝第二个写者，新建会话在首次落盘前也出现在侧栏；
- changes 走 `git status` + `git diff`，由 MyGo `editor.DiffView` 展示；
- GUI 不再发现或 attach workspace，也不打包第二份完整 CLI；
- browser 通过 owner-only control socket 获取 Bitmap frame，并回传鼠标、键盘、滚轮、resize 和提交后的 IME 文本；
- MyGo terminal 只用于显式 user shell 和现有 Agent TTY 的 detachable takeover。

与本文的差异，按事实记录：

- **没有使用 `gui` build tag**。GUI 是独立命令、独立目录；`golder` 二进制不导入它，但 `go build ./...` 会编译 GUI 包（这也是它的测试入口）。MyGo 因此成为 module 依赖，而不是被 `golder` 链接的依赖。
- **GUI 明确不实现 workspace**。split pane、daemon、detach/attach 继续属于 CLI/TUI；这是产品边界，不再是待补功能。
- **Browser Surface 的 Golder 侧接口已独立于 terminal**。kitty-browse v1 control API 尚无 raw pointer action，当前用只接受数值坐标和封闭事件集的兼容 adapter；上游加入 raw input capability 后可无 UI 变化地替换。

验证命令：

```sh
go test ./gui/...
go build ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...
```

后续体验与边界已经基于真实 GUI 试用重新收敛，详见
[`gui-ux-optimization.md`](gui-ux-optimization.md)：它定义了共享
credential/config resolver、Settings、按项目分组的 Session 管理、输入框内
圆形 Send/Stop、kitty-browse 共享 Browser Surface、保留可选 MyGo terminal、
删除 GUI workspace/第二份完整 CLI，以及单架构 <45 MiB 的目标。该文档是
当前优化实施路线；本节保留 v1.5.46–v1.5.48 已实现状态作为事实基线。
