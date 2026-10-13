# hostcompat

golder 让**运行它的那个终端复用器**认出自己，并在对方的界面里像
claude-code / codex 一样出现：状态点、Inbox 条目、通知、会话恢复。

支持两个宿主，**都不需要改它们的代码**：

| 宿主 | 怎么上报 | 需要装东西吗 | 状态词汇 | 恢复会话 |
| --- | --- | --- | --- | --- |
| [tuios](https://github.com/Gaurav-Gosain/tuios) | `tuios set-agent-state` 等 verb（走 `TUIOS_SOCKET`） | **需要一份 manifest**（`assets/tuios-golder.toml`） | `none` / `working` / `needs_input` / `idle` / `done` / `errored` / `unknown` | manifest 的 `[resume]` |
| [herdr](https://github.com/herdrdev/herdr) | `$HERDR_BIN_PATH pane report-agent` 等 | 不需要，自报即识别 | `working` / `blocked` / `idle` / `unknown` | 上报里的 resume argv |

## 快速开始

```sh
golder host-compat status      # 现在处在哪个宿主里、缺什么
golder host-compat install     # 写 tuios 的 manifest（herdr 不需要）
tuios kill-server              # tuios 只在 daemon 启动时读 manifest（会关掉所有 pane）
```

在宿主 pane 之外运行 `status` 也能用，它会直接说"这里没有宿主"。任何上报失败都不
会中断 golder：宿主是环境，不是依赖。

## 代码地图

| 文件 | 作用 |
| --- | --- |
| `hostcompat.go` | 宿主探测、中立状态词汇（`State`/`Kind`）、异步单槽上报器 `Reporter` |
| `session.go` | 生命周期记账 `Session`：回合开始/结束、**并发的**审批弹窗计数、退出释放 |
| `tuios.go` | tuios 方言（verb 名、参数、`[resume]` 用的 `ResumeArgv`） |
| `herdr.go` | herdr 方言（`--seq` 单调序号、`done`/`errored` → `idle` 的折叠、resume argv） |
| `install.go` | manifest 的安装/卸载/状态（幂等、不覆盖别人的文件、atomic 写） |
| `assets/tuios-golder.toml` | **要改的就是这份**：tuios 的 harness manifest |

驱动层的接线（只管"什么时候"，不管"报什么"）：

- `internal/cli/tui/sessionhost.go`
- `internal/cli/repl/replhost.go`
- `internal/cli/headless/headless.go`
- 审批漏斗：`judge.GateOpts.OnPrompt` / `trust.GateOpts.OnPrompt` /
  `judge.ReadScopeOpts.OnPrompt` → `run.GatesOpts.OnPrompt`（golder 里所有"等人回答"
  的问询都从这三个叶子包发出，所以一个钩子就覆盖了 TUI 弹窗、REPL 的 y/N、
  read-scope 与信任确认）

## 逃生舱

```sh
GOLDER_HOST_REPORT=off      # 完全不上报
GOLDER_HOST_REPORT=tuios    # 强制按某个宿主上报（一个 pane 看着像两个宿主时用）
GOLDER_HOST_REPORT=herdr
```

## 维护须知（重要）

**tuios 的 Inbox 里"按一下批准"依赖屏幕规则。** 状态由 golder 自报（`report`
来源，优先级高于屏幕规则），但 tuios 要替人按那个键，必须先**读懂弹窗**：它在
`assets/tuios-golder.toml` 里找到 `needs_input` 规则，再重新读屏、注入按键。

所以：

1. 改动 `internal/cli/tui/approval.go` 里的**按钮文案**（`Approve once` /
   `Approve for this session` / `Deny` / `Do you trust this folder?`）、或
   `input.go` 的 `> ` 提示、或 `spinner.go` 的 `(1m 54s · ↓ … tokens · …)` 形状时，
   **同一次提交里改 manifest 的对应规则**，并把 `ManifestVersion` 加一。
2. 改完要 `tuios kill-server` 才生效（daemon 启动时读 manifest）。
3. 忘了改的代价是**静默降级**：状态照常上报，Inbox 只是不能替你按键，回到"去 pane
   里自己答"。不会出错，但也不要让它悄悄发生——`golder host-compat status` 会把
   `outdated` 指出来。

herdr 侧没有这个问题：它只读上报，没有屏幕规则。

## 设计约束

- **叶子包**：`hostcompat` 只依赖标准库，不 import golder 任何其他包。驱动拿到的
  是一个 `*Session`，nil 即"没有宿主"，所有方法在 nil 上都安全（no-op），所以调用点
  不需要任何 `if`。
- **永不阻塞 agent**：上报在独立 goroutine 上合并发送（单槽：只保留最新状态），
  1.5s 超时，失败只写一行日志。子进程的 stdout/stderr 全部丢弃，免得污染转录。
- **状态要"诚实地折叠"**：herdr 没有 `done`/`errored`，折叠成 `idle` 并把原词写进
  message，不发明状态、也不静默丢弃。
- **并发审批**：审批弹窗可以重叠（并行工具调用），`Session` 用计数而不是布尔；回合
  结束时若有弹窗未答，保留 `blocked` 而不是报 `done`。这条错了会让 pane 卡在
  `needs_input`（`ask-agent` 会一直拒绝），所以有专门的单测。

## 测试

`go test ./internal/hostcompat/`：宿主探测、状态/术语映射、herdr 的 `--seq` 单调性、
argv 逐字断言（fake runner，不需要装宿主）、manifest 安装的幂等与"不覆盖别人的文件"、
并发审批的计数与释放。
