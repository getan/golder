# Golder GUI 技术选型与增量接入方案

> 状态：设计建议，尚未实施  
> 调研日期：2026-11-02  
> 目标：增加一个可选桌面 GUI，不重写现有 CLI/TUI/workspace，不引入 Electron 或随应用分发的 Chromium/Node 运行时。

## 结论

建议采用：

1. **Wails v3** 作为桌面窗口、系统 WebView、Go 服务绑定、事件流和原生菜单/对话框层；
2. GUI 作为独立的 **`golder-gui` 构建产物**，现有 `golder` 二进制及其默认构建、测试和 CGO-free 交叉编译保持不变；
3. 前端第一阶段使用内嵌的 **HTML + CSS + 少量原生 ES Module**，不使用 React/Vue/Svelte，不要求 Node/npm 运行时，也不打包 Electron；
4. Go 侧新增一个很薄的 `internal/appsession` 控制器，复用现有 `run.SetupEnv`、`runtime.StartRun`、`run.Gates`、`session.Store` 和工具/Provider 实现；
5. 审批继续走现有 `run.Gates` 的 `Confirm` 回调，不创建第二套权限系统；
6. 第一版只做聊天、工具卡片、审批、会话恢复、Provider/模型选择和取消，不嵌入 Shell、终端 workspace 或浏览器页面。

Wails **不是 Electron**。桌面端复用操作系统 WebView：

- macOS：系统 WebKit / WKWebView；
- Windows：系统 WebView2；
- Linux：WebKitGTK。

应用不会随包携带完整 Chromium，也没有常驻 Node 进程。Linux 仍需要系统 WebKitGTK；Windows 需要 WebView2 Runtime；macOS 的 WebKit 随系统提供。

如果项目要求“界面代码也必须 100% Go，不能有任何 HTML/JS”，备选是 **Fyne v2**。它更纯粹，但对 Golder 这种长文本、Markdown、代码高亮、diff、可折叠工具卡片和未来终端视图，开发成本明显高于 Wails。

---

## 约束和非目标

### 硬约束

- 不引入 Electron、打包 Chromium 或 Node 运行时；
- 不重写现有 Bubble Tea TUI；
- 不让 GUI 解析 ANSI 屏幕、PTY 输出或 CLI 文本作为业务协议；
- 不破坏现有 `golder` 单二进制、默认测试矩阵和 `CGO_ENABLED=0` Linux 交叉构建；
- 不降低现有 trust、boundary、permissions、sandbox、redaction 和 hook 边界；
- GUI 关闭或失联时，待处理审批必须 fail closed；
- CLI/TUI 与 GUI 使用同一套 session 格式和 Provider/工具语义；
- GUI 依赖只能进入显式 GUI 构建，不得成为现有 CLI 的运行时依赖。

### 第一阶段非目标

- 不把现有 split-pane workspace 搬进 GUI；
- 不把受控 Chromium 页面嵌入 GUI 的 WebView；
- 不实现完整终端模拟器；
- 不追求移动端；
- 不把 CLI/TUI 改造成 GUI 的子进程；
- 不同时让 CLI 和 GUI 写同一个 session；
- 不在第一阶段迁移现有 TUI 到新的控制器。

这些能力可以以后增加，但不能成为 GUI MVP 的前置条件。

---

## Golder 已有的可复用边界

### 1. `agent` 包已经是稳定的 Go 嵌入 API

`agent.Session` 已提供：

- Provider/模型解析；
- `Prompt` / `Stream` / `StreamEvents`；
- message/thinking delta；
- tool start/update/end；
- usage；
- tool allowlist/denylist；
- 可选 skills、memory 和 extensions。

`agent.Session.StreamEvents` 与 GUI 的单向事件流非常接近，可以直接用于低风险原型。

但它有两个不能忽略的限制：

1. 默认 built-in tools 会自动执行，语义等同于 CLI 的 `--approve`；
2. `agent.Session` 只有进程内上下文，不提供现有 CLI 的完整 session tree、resume、审批 UI 和 hook 生命周期。

因此：

- **原型**可以用 `agent.Session`，但必须 `WithoutTools()` 或只开放只读工具；
- **产品版**不能直接用默认 `agent.New()` 承载完整 GUI 权限模型。

### 2. 审批回调已经存在

`internal/cli/run.GatesOpts` 已有：

```go
Confirm func(ctx context.Context, req judge.ApprovalRequest) judge.ApprovalAnswer
```

TUI 当前就是通过该回调把审批请求送入 Bubble Tea channel，等待用户回答。GUI 可以使用同一个回调：

```text
run goroutine
    │ ApprovalRequest
    ▼
GUI controller pending map
    │ ordered event
    ▼
Wails stream → approval dialog
    │ answer
    ▼
RespondApproval(id, answer)
    │
    └─ reply channel → run.Gates
```

这意味着 GUI 不需要发明新的审批模型，也不需要把审批降级成字符串解析。

### 3. session 已有独立的 JSONL 存储层

`internal/session` 已经提供：

- 版本化 JSONL；
- `SessionHeader`；
- message tree；
- branch/leaf；
- list/load/append；
- resume/export/import；
- 大文件和单行大小上限；
- redaction 后持久化。

GUI 应直接复用该存储层，不创建 SQLite 或另一套聊天记录格式。

### 4. run/event bridge 已证明可行

TUI 的 `bridge.go` 已经证明下面的并发模型是稳定的：

```text
runtime.StartRun / DrainStream
        │
        ▼
bounded channel（阻塞，不丢事件）
        │
        ▼
单线程 UI 状态归并
```

GUI 应保持同一原则：Agent session 只由一个 goroutine 驱动，WebView 不直接读写 run state。

### 5. workspace daemon 不是 GUI 业务 API

现有 workspace daemon 的职责是：

- PTY 生命周期；
- pane/session；
- detach/reattach；
- 终端输入和绘制；
- browser pane。

它不是 Agent 语义 API。GUI 如果通过 workspace 屏幕抓取或 PTY 文本驱动 Agent，会重新引入：

- ANSI/终端协议解析；
- 宽度和重排差异；
- 审批输入竞争；
- 不稳定的文本契约。

所以 GUI 第一版应直接在进程内调用 Agent/run/session 层，workspace 保持原样。

---

## 候选方案

### Wails v3

截至调研日：

- 最新稳定版：`v3.1.0`（2026-11-01）；
- 许可证：MIT；
- 主体语言：Go；
- GitHub 活跃，约 36.9k stars；
- 提供 Go service binding、typed events、ordered stream、file dialogs、menu、tray、single-instance 和系统窗口 API；
- 静态资源可以直接来自 `fs.FS` / `embed.FS`；
- v3.1.0 提到实验性的 macOS/Linux CGO-free backend，但当前方案不应依赖实验能力。

优点：

- 不使用 Electron，不携带 Chromium；
- Go 后端与现有架构衔接自然；
- HTML/CSS 很适合 transcript、Markdown、diff、工具卡片和响应式布局；
- Wails v3 的 `HandleStream` 与 Golder 有序事件流高度匹配；
- 原生文件选择、菜单、快捷键、系统主题和窗口生命周期现成；
- 前端可以是零框架、零 npm 的静态资源；
- 以后若需要 xterm.js、Monaco 或复杂图形，可以增量引入，而不是一开始就决定。

代价：

- GUI 仍有少量 HTML/CSS/JavaScript；
- Linux 需要 WebKitGTK；
- Windows 依赖 WebView2 Runtime；
- 平台 WebView 行为不是完全一致；
- Wails v3 发布节奏快，应固定精确版本，不使用 `@latest`；
- GUI 原生构建、签名和打包不能继续完全依赖 `CGO_ENABLED=0` 的单一交叉构建。

适合 Golder 的原因：

- UI 的复杂点主要是文本和卡片，而不是自定义 GPU 控件；
- Golder 已有 Go 事件流，Wails 只做表示层；
- 可用 Go 端 Chroma/Goldmark/Bluemonday 生成代码高亮和安全 HTML，前端不必引入大型 Markdown/语法高亮依赖；
- 系统 WebView 让 GUI 体积和内存远低于 Electron。

### Fyne v2

截至调研日：

- 最新稳定版：`v2.8.2`（2026-10-12）；
- 许可证：BSD-3-Clause；
- Go toolkit，约 28.8k stars；
- 支持 Windows、macOS、Linux、Android、iOS 等；
- 自带 widget、canvas、layout、data binding、主题和打包工具。

优点：

- 应用 UI 代码可以全部使用 Go；
- 不依赖浏览器 DOM、JavaScript 或 WebView；
- API 比 Gio 高层，表单、菜单、列表、文件对话框等基础控件完整；
- Fyne packaging 能生成 `.app`、Windows executable 和 Linux 包结构；
- 测试 widget 比测试真实 WebView 简单。

代价：

- 桌面构建依赖 CGO/GCC 和原生图形头文件；
- 跨平台构建需要目标 C 编译器，官方推荐 `fyne-cross`/Docker；
- 长 transcript、虚拟滚动、Markdown、可选中文本、代码高亮和大 diff 需要大量自定义 widget；
- 自定义视觉体验通常不如 HTML/CSS 快；
- 嵌入终端或浏览器不是自然扩展路径；
- 流式文本频繁更新必须严格批处理到 UI goroutine（`fyne.Do`）。

适用条件：

- “不能有任何 HTML/JS”是比 UI 丰富度更高的优先级；
- GUI 只做聊天、设置和简单审批；
- 接受单独的 native/CGO 构建流水线。

### Gio

截至调研日，pkg.go.dev 显示 `v0.10.3`。Gio 是 Go 的 immediate-mode GUI：

- 支持 Linux、macOS、Windows、Android、iOS、BSD 和 WebAssembly；
- 使用平台窗口/input/GPU 库；
- renderer 基于 OpenGL ES / Direct3D，并持续演进 GPU renderer；
- 由应用每帧完整描述 UI。

优点：

- Go-first；
- 高性能、可控、适合大规模自定义绘制；
- 对高频更新和复杂可视化有优势；
- 依赖较少。

代价：

- immediate-mode 层级低；
- 文本编辑、IME、可访问性、焦点、选择、虚拟列表、菜单和复杂组件需要更多工程；
- Golder 已在终端侧投入大量 CJK/IME/选择处理，不宜在 GUI 第一版再从底层重做一次；
- 团队需要维护自己的组件系统和设计语言。

结论：适合定制渲染器或性能工具，不是“简单优雅接一个 Golder GUI”的首选。

### `webview_go`

它是底层 `webview` C/C++ 库的 Go binding：

- MIT；
- CGO；
- API 很小：窗口、HTML/URL、`Bind`、`Eval`、`Dispatch`；
- 使用系统 GtkWindow/NSWindow/HWND；
- 不使用 Electron。

优点：

- 概念和依赖面小；
- 做一次性 PoC 很快；
- 可以完全控制桥接协议。

代价：

- Go binding 仓库只有很少提交，最近一次 push 是 2024-08；
- 菜单、对话框、事件、打包、更新、单实例、类型生成、流协议、错误边界都要自己实现；
- `Eval` 很容易变成字符串拼接和注入风险；
- 长期看会把 Wails 已解决的问题重新实现一遍。

结论：只适合验证 UI 原型，不建议作为正式 GUI 基础。

### 明确排除

| 方案 | 排除原因 |
|---|---|
| Electron / Astilectron | 打包 Chromium/Node，体积和内存与本项目目标冲突 |
| Lorca | 依赖外部 Chrome/CDP，生命周期和可用性不适合作为桌面外壳 |
| Qt/GTK 大型绑定 | 原生 SDK/ABI/打包成本高，跨平台 CI 复杂 |
| Tauri | 前端轻，但核心宿主是 Rust，不符合 Go-first 目标 |
| Flutter | 需要 Dart/Flutter toolchain，与现有 Go 运行层割裂 |
| Ebitengine | 游戏/画布框架，不提供桌面应用需要的高层 widget 和可访问性 |

---

## 选型矩阵

| 维度 | Wails v3 | Fyne v2 | Gio | webview_go |
|---|---|---|---|---|
| Electron/打包 Chromium | 无 | 无 | 无 | 无 |
| 业务后端使用 Go | 是 | 是 | 是 | 是 |
| UI 是否纯 Go | 否，HTML/CSS/少量 JS | 是 | 是 | 否 |
| Node 运行时 | 无 | 无 | 无 | 无 |
| Node 构建工具 | 可选；本方案不用 | 无 | 无 | 可选；本方案不用 |
| 富文本/Markdown/diff | 最容易 | 需自定义 | 大量自研 | 前端容易、桥接自研 |
| 有序流事件 | v3 stream 原生适合 | Go channel + UI 调度 | Go event loop | 自建 RPC/事件协议 |
| 原生菜单/对话框/托盘 | 完整 | 完整 | 基础/自行组合 | 需自研 |
| 构建约束 | 系统 WebView，通常有 native/CGO 约束 | CGO/GCC | 平台 GPU/窗口依赖 | CGO |
| 跨平台视觉一致性 | Web 技术高 | 中高 | 取决于自研 | Web 技术高 |
| 长期维护成本 | 低到中 | 中到高 | 高 | 高 |
| Golder 适配度 | **最高** | 次选 | 不建议首版 | 仅 PoC |

---

## 推荐架构

```text
┌─────────────────────────────────────────────────────────────┐
│ golder-gui                                                  │
│                                                             │
│  Wails system WebView                                       │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ embedded HTML/CSS/ES modules                         │  │
│  │ transcript · composer · tool cards · approvals       │  │
│  └───────────────┬───────────────────────▲───────────────┘  │
│                  │ typed commands        │ ordered stream   │
│  ┌───────────────▼───────────────────────┴───────────────┐  │
│  │ GUI service (Wails adapter only)                     │  │
│  └───────────────┬───────────────────────────────────────┘  │
│                  │                                         │
│  ┌───────────────▼───────────────────────────────────────┐  │
│  │ internal/appsession.Controller                      │  │
│  │ one owner goroutine · bounded events · cancel       │  │
│  │ pending approvals · session cursor · persistence    │  │
│  └───────┬───────────────┬───────────────┬───────────────┘  │
│          │               │               │                  │
│      run.SetupEnv    runtime loop    session.Store          │
│      run.Gates       tools/hooks     JSONL/tree              │
└──────────┼───────────────┼───────────────┼──────────────────┘
           │               │               │
       existing providers/tools/redaction/sandbox/memory/extensions
```

### 为什么增加 `internal/appsession`

不要让 Wails service 直接保存几十个 runtime/internal 指针。增加一个 UI 无关控制器，提供稳定的 command/event 语义：

```go
type Controller interface {
    Start(context.Context) error
    Submit(context.Context, SubmitRequest) error
    Cancel(runID string)
    RespondApproval(approvalID string, ApprovalAnswer) error
    ListSessions(context.Context, SessionFilter) ([]SessionSummary, error)
    Resume(context.Context, string) error
    NewSession(context.Context, NewSessionRequest) error
    Snapshot() StateSnapshot
    Events() <-chan Event
    Close() error
}
```

它只做编排，不包含 Wails、HTML 或 widget 类型。这样：

- GUI adapter 可以替换；
- Controller 可在无图形 CI 中完整测试；
- 现有 CLI/TUI 不需要改成调用 Controller；
- 将来如果 Controller 成熟，TUI 可以选择性复用，而不是现在强制迁移。

### 为什么不直接复用 TUI `Model`

Bubble Tea `Model` 包含：

- 终端 geometry；
- ANSI/lipgloss 渲染；
- selection/scroll；
- keyboard/mouse；
- terminal queries；
- Bubble Tea commands/messages。

这些都不是 GUI 的业务状态。复用 Model 会让 GUI 变成“把终端画面搬进 WebView”，耦合更重。

### 为什么不把 `golder --output-format stream-json` 当正式后端

可以把它用于一天内的 UI spike，但不应作为最终架构：

- 每轮一个子进程；
- ask-mode 无法由 GUI 回答审批，只能 fail closed；
- session/provider/tool 的实时状态只能靠额外 CLI 调用；
- stdout JSON 成为新的跨进程兼容协议；
- 取消、hook、approval、extension lifecycle 难以保持一致；
- 子进程继承环境和凭据的边界更难审查。

正式 GUI 应 in-process 复用 run/session 层。

---

## Command 与 Event 协议

### 前端允许调用的命令

首版只暴露显式、窄接口：

```text
OpenProject(path)
NewSession(model, provider, cwd)
ResumeSession(id)
Submit(text, attachments)
Cancel(runID)
RespondApproval(id, answer)
ListSessions(scope)
GetModels(provider)
SetModel(provider, model, thinking)
GetSettings()
UpdateAllowedSetting(key, value)
ExportSession(id, format, path)
```

禁止绑定：

- 通用 `Exec(command)`；
- 通用 `ReadFile(path)`；
- 通用 `WriteFile(path, bytes)`；
- 任意反射调用；
- 任意 URL 导航；
- 把 tool registry 直接暴露给 JavaScript。

Agent 的文件和命令能力只能经现有 tool + gates 执行。

### Go → GUI 有序事件流

建议使用 Wails v3 `HandleStream`，而不是每个 token 一个无背压广播事件：

```text
session.opened
run.started
message.user
message.delta
thinking.delta
tool.started
tool.updated
approval.requested
approval.resolved
tool.finished
usage.updated
context.updated
compaction.started
compaction.finished
session.persisted
run.finished
run.failed
```

规则：

- 每个 event 带 `sessionID`、`runID` 和单调递增 `seq`；
- bounded channel，满时阻塞生产者，不丢审批、工具结束和最终消息；
- `message.delta` 可以按 30–50ms 合并，完成事件立即发送；
- frontend reload 后先请求 `Snapshot()`，再从新 stream 的序号继续；
- thinking 是否展示由用户设置决定，默认折叠；
- tool result 继续使用现有有界输出，不向 WebView 发送无限日志。

### 审批生命周期

`run.Gates.Confirm` 回调：

1. 生成随机 `approvalID`；
2. 放入 `pending[id] = replyChannel`；
3. 发送 `approval.requested`；
4. 阻塞当前 tool goroutine，等待 GUI 回答或 context 取消；
5. GUI 调用 `RespondApproval`；
6. 原样转换成 `judge.ApprovalAnswer`；
7. GUI 断开、窗口关闭或 run cancel：返回 `Answered:false`，fail closed。

不要让前端自行重建 capability 逻辑。它只渲染 Go 给出的请求和可选答案。

---

## 前端实现：无框架、无 Node 运行时

建议目录：

```text
cmd/golder-gui/
    main.go                 // //go:build gui
internal/appsession/
    controller.go
    events.go
    approvals.go
    sessions.go
internal/guiwails/
    app.go                  // //go:build gui
    service.go              // //go:build gui
    assets.go               // //go:build gui
    frontend/               // 必须位于 assets.go 所在包目录之下，供 go:embed 使用
        index.html
        app.css
        app.js
        components/
```

资源用 `go:embed` 编进 GUI：

```go
//go:embed all:frontend
var assets embed.FS
```

前端使用：

- 原生 Custom Elements 或小型函数组件；
- CSS Grid/Flexbox；
- `AbortController` 管理视图订阅；
- Wails 生成/提供的 runtime bridge；
- Go 服务端渲染完成态 Markdown。

不使用：

- npm install；
- React/Vue/Svelte；
- webpack/vite；
- CDN；
- 远程字体、脚本、图片；
- `eval` 或动态脚本拼接。

### Markdown 和代码高亮尽量留在 Go

Golder 已经依赖 Goldmark、Chroma、Bluemonday 等相关组件。GUI 可以：

1. streaming 阶段显示 escaped plain text；
2. `message.end` 后由 Go 端把 Markdown 渲染为 HTML；
3. Chroma 生成 code span；
4. Bluemonday 使用显式 allowlist 清理 HTML；
5. 前端只插入已标记的 sanitized result。

这样既保持 Go-first，也避免引入前端 Markdown/highlight 供应链。

### WebView 安全设置

- release 禁用 devtools；
- 只加载内嵌 asset origin；
- CSP 至少为 `default-src 'self'`，禁止远程 script/style；
- 外部链接交给系统浏览器并做 `https` allowlist；
- 阻止 WebView 导航到任意远程页面；
- 不把 Provider key、Cookie、SSH 环境或完整进程环境发给前端；
- 不在 localStorage 保存凭据；
- Go binding 只接收 typed DTO，并在 Go 侧重新验证；
- 拖入路径先进入 Go 的路径授权流程，不直接读；
- GUI WebView profile 与受控 browser profile 完全分离。

---

## 构建与发布隔离

### 保持现有 CLI 不变

GUI 相关入口和 Wails import 放在 `gui` build tag 下：

```go
//go:build gui
```

要求 CI 固定验证：

```bash
# 原有矩阵，不能因为 GUI 改变
go test -race ./...
go build ./...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...

# GUI 独立矩阵
go test ./internal/appsession/...
go build -tags gui ./cmd/golder-gui
```

GUI 不应加入默认 `golder` binary。发布物分开：

```text
golder                 existing CLI/TUI/workspace binary
golder-gui.app         macOS
golder-gui.exe         Windows
golder-gui package     Linux
```

### 不建议第一版拆成独立 Go module

独立 module 虽能隔离 Wails 依赖，但会引入：

- GUI 与 core 的版本同步；
- 本地 `replace ../` 或 go.work；
- internal package 导入边界；
- 两套 release/tag；
- 在提交中误用旧版 `agent` SDK。

第一版在同一 module、独立 build tag 下更简单。等 GUI API 稳定且只依赖公开 `agent` 包后，再评估拆 module。

### 平台构建

不要承诺“从一台机器交叉编译所有 GUI”：

- macOS job：构建、codesign、notarize、DMG；
- Windows job：构建、WebView2 策略、Authenticode、installer/zip；
- Linux job：构建、确认 WebKitGTK ABI、tar/deb/AppImage（按实际支持选择）。

Wails v3 的 CGO-free backend 目前是实验能力；现有 CLI 的 CGO-free 承诺不能依赖它。

---

## 分阶段计划

### Phase 0：一周内的可弃用 spike

目标：验证 Wails、静态无框架前端和事件流，不追求完整能力。

- `golder-gui` 打开窗口；
- 项目目录选择；
- Provider/model 输入；
- `agent.Session.StreamEvents`；
- `WithoutTools()` 或仅 `read/grep/find`；
- 显示 text/thinking/tool/usage；
- Cancel；
- 不做持久会话和审批。

验收：

- 不安装 Node/npm；
- macOS 包不包含 Chromium；
- streaming 不卡 UI；
- 关闭窗口能取消 run；
- PoC 可以删除而不影响现有代码。

### Phase 1：可发布 MVP

- 新增 `internal/appsession.Controller`；
- 复用 `run.SetupEnv`；
- 复用 `run.Gates.Confirm`，GUI 审批 fail closed；
- 复用 `session.Store`，支持 list/new/resume；
- transcript、tool card、diff、usage、context；
- Provider/model/thinking 切换；
- trust/permissions/boundary 基本设置；
- hooks、redaction、sandbox 与 CLI 同语义；
- macOS 首发，Windows/Linux smoke build。

MVP 明确不含：

- embedded terminal；
- embedded browser；
- workspace panes；
- full `/goal`、`/btw`、session tree 编辑器；
- GUI 自动更新。

### Phase 2：功能对齐

- session tree/fork/rewind/export/import；
- memory、dream、extensions；
- model/provider picker 与默认值；
- 文件/图片附件；
- Markdown 完成态渲染；
- 大 transcript 虚拟化；
- search/filter；
- tray/notification；
- remote-control 状态。

### Phase 3：可选 workspace 体验

优先顺序：

1. GUI 中列出和管理现有 workspace/session；
2. “Open Terminal Workspace” 启动/聚焦现有 `golder workspace`；
3. 显示 shell/browser pane 的只读 snapshot；
4. 真有需求再评估 xterm.js + PTY bridge。

不要在 Phase 1 直接把 terminal emulator 和浏览器嵌进 WebView。

---

## 关键验收标准

### 兼容性

- GUI 创建的 session 可由 CLI `--resume` 继续；
- CLI 创建的 session 可由 GUI 继续；
- 同一 session 同时打开时拒绝第二个 writer；
- Provider/model/thinking/session header 与 CLI 一致；
- prompt 恢复继续按当前 binary 重建系统提示。

### 安全

- ask 模式能从 GUI 回答一次/session/remembered；
- GUI 关闭时审批拒绝，不允许；
- read-only/full-access/auto 与 CLI 一致；
- path grant 和 sandbox denial 走同一 gate；
- WebView 不持有 API key；
- 远程内容不能注入 application binding；
- release WebView 不开放 devtools；
- GUI 不读取个人 Chrome profile；
- redaction 和 session 文件行为不变。

### 性能

- streaming delta 合并，不对每 token 重排整个 transcript；
- transcript 至少在数千 block 下可滚动；
- tool partial output 有界；
- event channel 有背压、不丢 final/approval；
- GUI 空闲时无持续动画或高 CPU；
- GUI 进程内存单独统计，不改变 CLI 的 50–100 MB 目标。

### 发布

- 默认 `go build ./...` 和 Linux CGO-free cross-build 不依赖 GUI 系统库；
- GUI 每个平台在原生 runner 构建；
- 安装包明确标注系统 WebView 要求；
- CLI 继续独立下载，不强制安装 GUI；
- GUI 版本可独立暂缓，不阻塞 CLI release。

---

## 风险与缓解

| 风险 | 缓解 |
|---|---|
| Wails v3 变化快 | 固定精确版本；升级单独 PR；契约测试 Go service/event |
| WebView 平台差异 | 原生三平台 smoke；前端只用基础 Web API；不依赖浏览器实验特性 |
| Linux WebKitGTK 部署复杂 | 明确支持 distro/ABI；容器只做构建，不声称等同真实桌面 |
| GUI 与 CLI session 并发写 | session writer lock；打开时显示 owner；只读查看作为降级 |
| public `agent` SDK 默认自动工具 | PoC 关 tools；产品 Controller 安装现有 Gates |
| 事件爆量 | bounded stream、delta coalescing、tool output cap、snapshot+seq 重连 |
| WebView binding 过宽 | typed service allowlist；无通用 exec/read/write/eval |
| HTML 注入 | Go 端 Markdown sanitize；CSP；无远程 assets；禁止任意导航 |
| GUI 反向污染 CLI | 独立 command/build tag/artifact；原 CI 和 CGO-free gate 不变 |
| 过早嵌入终端/浏览器 | 明确推迟到 Phase 3，先复用现有外部窗口和 snapshot |

---

## 最终建议

### 推荐路径

```text
Wails v3.1.x（固定版本）
+ system WebView
+ Go application service/controller
+ embedded static HTML/CSS/ES modules
+ Go 端 Markdown/代码高亮/sanitize
+ 独立 golder-gui artifact
```

它满足：

- 不依赖 Electron；
- 不打包 Chromium；
- 不需要 Node 运行时；
- 业务和安全逻辑继续是 Go；
- UI 可以快速达到现代聊天/代码工具需要的表现力；
- 对现有 CLI/TUI/workspace 的侵入最小。

### 何时改选 Fyne

只有在下面条件成为硬要求时改选 Fyne：

```text
任何 HTML/CSS/JavaScript 都不能进入仓库
```

接受的交换是：Markdown、diff、长 transcript、选择复制、虚拟列表和未来终端能力需要更多 Go widget 工程。

### 不建议

- 不建议 Electron/Astilectron；
- 不建议直接使用 `webview_go` 做正式版；
- 不建议 GUI 驱动 CLI/PTY；
- 不建议第一版嵌入 terminal/browser；
- 不建议为了 GUI 重写现有 Bubble Tea Model。

---

## 参考资料

调研事实以 2026-11-02 的官方仓库、release 和文档为准：

- Wails repository: <https://github.com/wailsapp/wails>
- Wails v3.1.0 release: <https://github.com/wailsapp/wails/releases/tag/v3.1.0>
- Wails v3 application API: <https://pkg.go.dev/github.com/wailsapp/wails/v3/pkg/application>
- Fyne repository: <https://github.com/fyne-io/fyne>
- Fyne v2.8.2 release: <https://github.com/fyne-io/fyne/releases/tag/v2.8.2>
- Fyne getting started: <https://docs.fyne.io/started/>
- Fyne desktop packaging: <https://docs.fyne.io/started/packaging/>
- Fyne cross-compiling and CGO: <https://docs.fyne.io/started/cross-compiling/>
- Gio: <https://gioui.org/>
- Gio app package (v0.10.3 at research time): <https://pkg.go.dev/gioui.org/app>
- webview_go: <https://github.com/webview/webview_go>
- webview_go API: <https://pkg.go.dev/github.com/webview/webview_go>

