# Windows GUI 浏览器支持技术方案

> 状态：已实现（分支 `windows-browser-transport`，依赖伪版本已锁定）
> 最后更新：2026-11-28
> 范围：Windows GUI 的 `browser` 工具与 shared browser hub
> 原则：macOS/Linux 行为不变；不引入 Windows 专用浏览器生命周期；不使用 TCP 控制通道

## 1. 结论

采用 **B-minimal**：在 kitty-browse 增加一个很小的 endpoint/transport seam，只包含 `Listen`、`Dial`、`Lock`、`Validate`、`IsNotRunning` 五个能力。Unix 实现保持现有 Unix socket、`0600`、`flock`、UID 校验逻辑不变；Windows 使用 named pipe + 当前用户 DACL + named mutex。shared hub、HTTP API、action 层和 GUI 业务逻辑都不改，Windows 仍然走 `browserpool.Open` → `browserhost.OpenShared`，不增加 standalone 分支。

不采用另外两个方向：

- **A：Windows 走 standalone `browserhost.Start`**。短期改动少，但会形成两套浏览器生命周期，并让多个会话争抢同一个 Chromium profile，长期不可靠。
- **C：loopback TCP + 随机 token**。需要给每个 per-view 请求增加 token 校验，还要处理端口、防火墙、发现和 token 文件 ACL；安全模型从 owner-only 本地端点变成本地端口，反而更复杂。

预计工作量：核心 GUI + shared hub 约 **1.5–2 周**；workspace/CLI 的完整 parity 另算。

## 2. 现状

kitty-browse 的浏览器本体、Chromium/CDP、profile、headless 渲染都能在 Windows 编译运行。README 明确写了：两个包都能为 Linux、macOS 和 Windows 构建，但 owner-only control socket（也就是自动化 API）需要 Unix-domain socket；Windows 上库能编译、pane 能运行，`-control-socket` 会报告平台不支持。

具体代码：

- `internal/control/control_unix.go` 使用 `net.ListenUnix`、`net.Dial("unix")`、`os.Chmod(0600)`；
- `internal/control/control_unsupported.go` 在非 Unix 平台返回 `control sockets are unsupported on this platform`；
- `browserhost/shared_other.go` 在非 darwin/linux 平台返回 `shared browser sockets require macOS or Linux`；
- `browserhost/shared_unix.go` 依赖 `syscall.Flock`、`syscall.Stat_t.Uid`。

Golder GUI 正常对话的链路是：

```text
browser tool
  → nativeBrowserHosts.Start
  → browserpool.Open
  → browserhost.OpenShared（shared hub）
  → hub 创建 view
  → browserhost.Start（每个 view 的 control API）
```

相关位置：

- `gui/cmd/golder-gui/main.go` 的 `main` 只在 `darwin || linux` 注册 native launcher；
- `gui/cmd/golder-gui/browserhost.go` 的 `nativeBrowserHosts.Start` 在 `ConversationID != ""` 时走 `browserpool.Open`；
- `internal/cli/run/run.go` 的 `BuiltinTools` 只有在 `BrowserAvailable`、`BrowserWorkspaceAvailable`、`NativeBrowserLauncherAvailable` 或 `TuiosAvailable && KittyBrowseAvailable` 成立时才注册 `browser` 工具；
- `internal/browserctl/browserctl.go` 的 `Dial` 和 `NewWithTransport` 使用 `net.Dial("unix", socket)`，`checkSocket` 要求 socket 文件是 owner-only；
- `internal/agenttool` 的 browser discovery 会扫描 Unix socket，并读取 `.browser.json`。

所以 Windows 上上述条件都不成立，`browser` 工具不会出现。`docs/releases/v1.5.53.md` 也写明了：没有 Unix control-socket transport 的平台不提供该工具。

只删掉 GUI 的 `GOOS` 判断不够，因为 `control.Start` 仍然返回 unsupported，shared hub 的 listen/dial/lock/ownership 全是 Unix 实现，`browserctl` 仍然连接 Unix socket，discovery 仍然按 socket 文件扫描，Chromium profile lock 在 Windows 上也没有等价实现。

## 3. 目标与非目标

### 3.1 目标

- Windows GUI 会话中注册并使用 `browser` 工具；
- 支持 `open`、`snapshot`、`find`、`click`、`fill`、`type`、`screenshot`、tab 操作、`close/quit` 等现有 action；
- 多个会话通过 shared hub 共享同一个浏览器 profile 和一个 Chromium owner；
- 每个会话仍有独立的 view/control endpoint；
- macOS/Linux 的代码路径、socket 权限、flock、错误处理保持不变；
- 不新增 TCP 监听端口；
- 不改变 `browser` 工具的审批、权限和目标绑定语义。

### 3.2 非目标

- Windows 上 `kitty-browse` 可见终端 pane 的完整 parity；
- workspace/CLI 的 browser pane 全量对齐（可作为后续阶段）；
- Android/移动端；
- Edge 专属能力或非 Chromium 浏览器；
- 通用 IPC 框架、插件化 transport 或运行时 transport 选择；
- 改变现有 browser action schema 或审批模型。

## 4. 设计

### 4.1 设计原则

1. **Endpoint 是不透明字符串**：Unix 是文件路径，Windows 是 `\\.\pipe\...`；业务代码只传递，不解析平台细节。
2. **平台差异只在 transport 层**：用 build tag 分成 `_unix.go` 和 `_windows.go`，不在 shared hub 或 GUI 里写 `if GOOS`。
3. **Unix 实现原样搬移**：`net.ListenUnix`、`0600`、`flock`、UID 校验不重写，降低回归风险。
4. **Windows 使用 named pipe**：HTTP 直接跑在 pipe 上，per-view 不新增 bearer token；pipe DACL 就是授权边界。
5. **只抽需要的五个能力**：不做通用 RPC、不做 transport 注册表、不做运行时开关。

### 4.2 Transport seam

在 kitty-browse 增加内部包 `internal/transport`：

```go
package transport

type Endpoint string

func Listen(ep Endpoint) (net.Listener, error)
func Dial(ctx context.Context, ep Endpoint) (net.Conn, error)
func Lock(dir string) (release func(), error)
func Validate(ep Endpoint) error
func IsNotRunning(err error) bool
```

Unix 实现（`transport_unix.go`）：

- `Listen`：现有 `net.ListenUnix`、`SetUnlinkOnClose(false)`、`Chmod(0600)`、`SameFile` 校验；
- `Dial`：现有 `net.Dialer.DialContext(ctx, "unix", path)`；
- `Lock`：现有 `syscall.Flock(LOCK_EX|LOCK_NB)`；
- `Validate`：现有 socket 文件类型、权限、目录可写性检查；
- `IsNotRunning`：`ECONNREFUSED`、`ENOENT` 视为“没有正在运行的实例”。

Windows 实现（`transport_windows.go`）：

- `Listen`：`winio.ListenPipe`，返回 `net.Listener`，交给 `http.Server.Serve`；
- `Dial`：`winio.DialPipe`，返回 `net.Conn`，作为 `http.Transport.DialContext`；
- `Lock`：`Local\golder-browser-<hash>` named mutex，进程退出自动释放；
- `Validate`：校验 endpoint 是 `\\.\pipe\` 前缀、名称由固定前缀和 hash 组成；不调用 `os.Stat`；
- `IsNotRunning`：映射 `ERROR_FILE_NOT_FOUND`、`ERROR_PIPE_BUSY`、`ERROR_BROKEN_PIPE`，必要时对 `ERROR_PIPE_BUSY` 做短重试。

`winio` 使用 `github.com/Microsoft/go-winio`。Golder 的 `go.mod` 已经有该依赖（当前是间接依赖）；kitty-browse 需要把它加为直接依赖。

### 4.3 Endpoint 生成

`browserhost` 当前通过 `SharedSocket(dir)`、`ViewSocket(dir, id)` 生成 Unix socket 路径。改为平台实现：

- Unix：`SharedSocket(dir)` 仍返回 `filepath.Join(dir, "hub.sock")`；`ViewSocket(dir, id)` 仍返回 `filepath.Join(dir, id+".sock")`。
- Windows：`SharedSocket(dir)` 返回 `\\.\pipe\golder-hub-<hash(dir)>`；`ViewSocket(dir, id)` 返回 `\\.\pipe\golder-view-<hash(dir,id)>`。
- hash 使用绝对 `dir` 和 view id，避免不同项目或会话碰撞；名称长度控制在 Windows pipe 限制内，不拼接用户输入。

Unix 的返回值与今天完全一致，不需要迁移已有会话或 metadata。

### 4.4 Server 侧改造

`internal/control`：

- 把 `control_unix.go` 的 `Start` 主体改为调用 `transport.Listen`；
- 删除 `control_unsupported.go`，由平台实现替代；
- `newServer` 接收 `net.Listener` 和可选的 cleanup 函数，不再假设 listener 是 `*net.UnixListener`；
- `Close` 在 Unix 上继续 unlink socket；Windows 上只关闭 pipe，不删除文件；
- `Capture`、`Info`、`Action`、`Quit` 的 HTTP 请求逻辑不变，只把 dialer 换成 `transport.Dial`；
- 保持现有超时、响应体上限、错误 envelope 不变。

`browserhost/shared.go`：

- `net.Listen("unix", socket)` → `transport.Listen(socket)`；
- `net.DialTimeout("unix", socket, ...)` → `transport.Dial(ctx, socket)`；
- `sharedLock(...)` → `transport.Lock(...)`；
- `sharedOwned`、`os.Lstat`、`Mode().Perm()`、`os.Chmod`、`os.SameFile` 只在 Unix 的 `Validate` 中保留；
- `sharedCall` 的 dialer 改用 `transport.Dial`；
- `syscall.ECONNREFUSED` 的 stale socket 判断改用 `transport.IsNotRunning(err)`；
- `writeSharedToken`/`readSharedToken` 继续使用 `hub.auth`，Unix 权限检查不变；Windows 侧依赖用户 profile 的默认 ACL，并增加只读的 owner/ACL 校验。

shared hub 的 HTTP handler、token 认证、`/open`、`/reconnect`、`/input`、`/heartbeat`、view 生命周期和 2 分钟 lease 全部不动。

`internal/browser` 的 profile lock：

- Unix 继续使用 `profile_lock_unix.go`；
- Windows 在 `profile_lock_other.go` 中实现 named mutex（名称由 profile 绝对路径 hash 得到）；
- 该锁是 shared hub 之外的防御层；shared hub 自己的 `transport.Lock` 仍然保证一个 profile 只有一个 owner。

### 4.5 Client 侧改造（Golder）

`internal/browserctl`：

- 新增 `dial_unix.go` / `dial_windows.go`：Unix 保持 `net.Dialer.DialContext(ctx, "unix", socket)`；Windows 使用 `winio.DialPipe(socket, nil)`；
- `NewWithTransport` 默认使用平台 dialer；
- `checkSocket` 拆成平台实现：Unix 保留当前 socket 文件、`0600`、目录检查；Windows 只校验 `\\.\pipe\` 前缀和合法字符，不调用 `os.Stat`；
- `SocketFromEnv`、`KITTY_BROWSE_SOCKET` 继续接受字符串；Windows 下该变量可以放 pipe 名。

`internal/agenttool`：

- `browserSocket`、`socketPath` 仍然按字符串处理；
- `browserControlSocketPath` / `browserPaneSpec` 在 Windows 返回 pipe 名，Unix 返回现有短路径；
- `browserCandidatePaths` 在 Unix 继续扫描 socket 文件和 `.browser.json`；Windows 只读 `.browser.json`（以及显式环境变量），不扫描目录；
- `BrowserWorkspaceAvailable` 在 Windows 基于 metadata 判断；
- 不改变 endpoint 的校验边界：模型不能提供任意 pipe 路径，路径仍由 trusted launcher 生成。

GUI：

- 删除 `gui/cmd/golder-gui/main.go` 里的 `darwin || linux` 门；
- `gui/cmd/golder-gui/browserhost.go` 不再增加 standalone 分支，所有平台都走 `browserpool.Open`；
- `appsession`、browser preview、operator 注册、审批路径不改；
- 如果 transport 初始化失败，只是 `browser` 工具不注册，不能让 GUI 启动失败或影响其他工具。

### 4.6 Chrome / Edge

- `KITTY_BROWSE_CHROME` 仍然是最高优先级的显式路径；
- 为空时，由 kitty-browse 的 `internal/browser` 按平台探测：
  - Chrome：`%ProgramFiles%\Google\Chrome\Application\chrome.exe`、`%ProgramFiles(x86)%\...`、`%LocalAppData%\...`；
  - Edge：`%ProgramFiles(x86)%\Microsoft\Edge\Application\msedge.exe`、`%ProgramFiles%\...`；
  - 优先 Chrome；只有在没有 Chrome 时回退 Edge；
- Edge 只按 Chromium/CDP 兼容性验证，不承诺 Edge 专属行为；
- `RequireSandbox = true` 保持不变；不以 `--no-sandbox` 绕过 Windows 权限或企业策略；
- 如果 Chrome/Edge 都不存在，`browser open` 返回明确错误，工具仍保持注册（与现有“后端缺失时 schema 收缩”的策略一致）。

### 4.7 安全模型

Unix：

- 保持 socket `0600`、父目录不可被其他用户写、`flock`、UID 校验；
- per-view control API 继续只依赖 socket 权限，不新增 token；
- shared hub 继续使用 `hub.auth` bearer token。

Windows：

- named pipe 使用创建进程的默认 DACL；在标准用户下通常等价于当前用户 + SYSTEM/Administrators；
- 不授予 Everyone、Network 或匿名访问；实现时用 `winio.PipeConfig.SecurityDescriptor` 明确限制到当前用户 SID，并在 Windows 集成测试中验证第二用户无法连接；
- 不接受远程 pipe 客户端；如果 `go-winio` 不直接提供 `PIPE_REJECT_REMOTE_CLIENTS`，在 DACL 层拒绝 Network SID，并记录到安全测试；
- `hub.auth` 放在用户 profile 下的 Golder/kitty-browse 私有目录，继承用户 ACL；`readSharedToken` 在 Windows 增加 owner/ACL 校验，不依赖 `os.Chmod`；
- per-view control API 不新增 bearer token：pipe DACL 就是授权边界，避免协议分叉；
- 不监听 TCP；验收时用 `netstat` 确认 GUI/浏览器进程没有新增监听端口；
- 不改变 browser 工具的审批、目标绑定、operator input 和 verification pause 规则。

## 5. 分阶段实施

| 阶段 | 工作 | 时间 | 验收 |
|---|---|---|---|
| Phase 0：Spike | `transport` 的 Windows `Listen`/`Dial`；`control.Start` 可用；`browserctl` dialer；Chrome/Edge 跑通 Info/Snapshot/Screenshot | 1–2 天 | Windows 上能截图并返回 PNG；进程退出后 pipe 清理；Unix 无变化 |
| Phase 1：Transport + control | Unix/Windows transport、conformance tests、`control` 平台无关、错误映射 | 2–3 天 | Unix control 测试全过；Windows 单测覆盖 listen/dial/validate；三平台 build 通过 |
| Phase 2：Shared hub | `SharedSocket`/`ViewSocket` 分平台；hub listen/dial/lock/ownership 走 transport；`hub.auth` ACL；metadata 发现；多 view 测试 | 3–5 天 | 两个会话共享一个 profile 和 hub；关闭一个 view 不影响另一个；stale pipe 可恢复 |
| Phase 3：GUI 接入 | 删除 `GOOS` 门；所有平台走 `browserpool.Open`；Chrome/Edge 探测；tool/discovery/operator/preview 验证 | 2–3 天 | Windows GUI 中 `browser` 工具出现并完成 open/截图/点击/关闭；退出后无残留 |
| Phase 4：CI/发布 | `windows-latest` 测试；三平台 gofmt/vet/build/test；kitty-browse 发版和 go.mod 升级；文档 | 2–3 天 | CI 全绿；Windows 真实浏览器验收；Unix 行为不变 |

如果 Phase 0 失败，停止在该阶段，不进入 shared hub 改造。

## 6. 测试矩阵

| 范围 | Unix（macOS/Linux） | Windows |
|---|---|---|
| transport 单元测试 | listen/dial/lock/validate 原行为 | pipe listen/dial、mutex、错误映射 |
| control API | 现有测试全部保留 | Info/Action/Screenshot/Quit |
| shared hub | 现有测试全部保留 | 两个 view、lease、stale endpoint |
| browserctl | Unix socket 权限检查 | pipe 名称、DACL、错误映射 |
| browser tool | 现有 discovery/绑定测试 | metadata 发现、工具注册 |
| GUI | 现有 headless/UI 测试 | 真实窗口、右侧 drawer、输入、关闭 |
| 安全 | socket 0600/目录/flock | 第二用户不可连接、无 TCP 端口、token 文件 ACL |

Windows 集成测试至少覆盖：

1. `browserhost.Start` + `browserctl.Info`；
2. `snapshot` + `screenshot`；
3. 一次 `click` 或 `fill`；
4. shared hub 两个 view；
5. 关闭一个 view 后另一个仍可用；
6. 进程退出后 endpoint 清理。

Unix 回归要求：

- `go test ./...` 在 macOS/Linux 通过；
- 现有 socket 路径、权限、错误文案不变；
- 不新增运行时 `GOOS` 分支到 shared hub 或 GUI 业务层。

## 7. 风险与缓解

| 风险 | 缓解 |
|---|---|
| Windows pipe 默认 DACL 比预期宽 | 显式使用当前用户 SID 的 security descriptor；第二用户测试；拒绝 Network/Everyone |
| pipe 名称碰撞或超长 | 使用绝对 runtime dir + view id 的 hash；固定前缀；长度校验 |
| `ERROR_PIPE_BUSY` 被误判为失败 | `transport.IsNotRunning` 区分 busy/not found/broken；必要时短重试 |
| shared hub 的 token 文件权限不可靠 | 放在用户私有目录；读取时校验 owner/ACL；不依赖 `os.Chmod` |
| 多实例争抢 Chromium profile | shared hub 的 `transport.Lock` + Windows profile named mutex |
| Edge 与 Chrome 行为差异 | Chrome 为主；Edge 只按 Chromium 兼容性验证；显式 `KITTY_BROWSE_CHROME` 可覆盖 |
| 管理员/提权下 Chromium sandbox 失败 | fail closed；不自动加 `--no-sandbox` |
| Windows runner 无 Chrome | CI 明确安装或 fail；不能因缺浏览器让集成测试静默通过 |
| 发现逻辑仍按 socket 文件扫描 | Windows 只读 metadata；Unix 保持现有扫描 |

## 8. 回滚与兼容性

- 所有 Windows 代码都在 build tag 后面；macOS/Linux 二进制不包含 Windows 实现；
- 如果 Windows 实现未完成或不稳定，只需不注册 Windows launcher，`browser` 工具会像今天一样缺席，不影响其他工具；
- Unix endpoint 字符串完全不变，已有 `.browser.json`、session、profile 不需要迁移；
- 不改变 browser action schema、审批模式、目标绑定和 operator 凭据格式；
- Windows 的 browser 工具是增量能力，不需要数据迁移或配置开关。

## 9. 决策记录与参考

- **选择 B-minimal**：一个很小的 transport seam + Windows named pipe，shared hub 和 GUI 业务逻辑不变。
- **拒绝 A**：Windows standalone 会导致两套浏览器生命周期和 profile 争抢，短期简单、长期不可靠。
- **拒绝 C**：TCP + token 需要额外的认证、端口、发现和防火墙处理，安全模型也更弱。
- **成本判断**：核心 GUI + shared hub 约 1.5–2 周；完整 workspace/CLI parity 约 3 周以上。

参考：

- `gui/cmd/golder-gui/main.go`：native launcher 的 `GOOS` 门；
- `gui/cmd/golder-gui/browserhost.go`：`browserpool.Open` / `browserhost.Start`；
- `internal/cli/run/run.go`：`BuiltinTools` 的 browser 注册条件；
- `internal/agenttool/browser_launch.go`：`NativeBrowserLauncherAvailable`；
- `internal/browserctl/browserctl.go`：Unix socket dial 和 `checkSocket`；
- kitty-browse `internal/control/control_unix.go`、`control_unsupported.go`；
- kitty-browse `browserhost/shared.go`、`shared_unix.go`、`shared_other.go`；
- `docs/releases/v1.5.53.md`：v1.5.53 的 native browser drawer 说明。

## 10. 实现状态（2026-11-28）

- kitty-browse：新增 `internal/transport`（Unix / Windows named pipe）、平台无关
  `control`、shared hub 的 endpoint / lock / token、Windows profile lock、
  Chrome/Edge 探测和 Windows CI job。
- Golder：`browserctl` Windows dialer、`browserpool` Windows runtime / helper
  env、pipe metadata 发现、GUI 去 `GOOS` 门、Windows CI job。
- 依赖锁定：`github.com/getan/kitty-browse v0.2.18-0.20261128145844-5db14e6a6473`
  （伪版本，未创建 tag）。
- 已验证：Windows `go build ./...`、darwin/linux 交叉构建、Windows
  transport/control/shared hub 单测、GUI 与 browserpool 编译。
- 已知限制：kitty-browse 完整 Windows 测试仍有既有的 PTY、文件权限和终端
  graphics 失败，不属于本 transport 改动；workspace/CLI browser pane 的全量
  parity 未包含；真实 GUI 浏览器操作仍需 Windows 人工验收。
