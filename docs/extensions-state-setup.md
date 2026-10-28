# 为现有扩展增加私有持久状态与用户授权入口

Status: Implemented locally; Linux runtime and provider acceptance pending
Last updated: 2026-10-28
Discussion: 用户确认接受 state_dir / setup；要求插件遵守现有规范、默认用户身份、优先内置浏览器扫码授权。
Related: [extensions-mcp.md](extensions-mcp.md), [lark-cli-plugin.md](lark-cli-plugin.md)

## 实现记录

现有 SourceConfig 已支持 opt-in state_dir、setup、setup_variants、setup_origins、executables、
skills、confirm_tools、aggregate_tools；CLI/TUI 提供显式 setup/reset。持久 HOME 有跨进程锁，
沙箱通过来源专属 typed capability 暴露它，不能用普通目录 grant 重开整个 protected root。
真实子进程 fixture 覆盖私有 HOME 可写、宿主/兄弟状态不可读、重启留存及 reset 边界。

浏览器实现收敛为宿主生成的**离线二维码显示页**：没有控制 socket、远端页面、cookies 或桌面
OAuth 跳转，手机访问已校验的官方 verification URL。Mac 仍需浏览器运行时例外，Linux 仍受沙箱。
它替代下文 §5 的通用敏感 OAuth 浏览器实例；真实服务验收仍需用户参与，Linux 容器测试仍暂停。

## Abstract / 摘要

我们在现有 CLI / MCP stdio / legacy RPC 执行方式上增加可选的 `state_dir` 和 `setup`。
前者保存该源自己的登录态；后者只在用户明确发起配置/授权时运行。浏览器由宿主管理，
插件只能提供授权 URL，不能获得浏览器控制 socket、用户全局 HOME 或宿主执行权限。

这不是第四种 runtime，也不是 MCP 自动 OAuth：业务执行仍走原有 adapter 和 gate。
本文是新增设计，不代表这些字段、命令或 UI 已经实现。

## Background / 背景与动机

`internal/extensions/process.go` 目前在 `prepareCommand` 中建立临时 HOME，cleanup 删除它。
CLI 每次调用失去本地状态；stdio server 在存活期间保留临时目录，但关闭/重连/新会话后也会丢失。
这保护了用户 HOME，却让 OAuth 登录和 refresh token 无法跨会话保存。

另外，`list/inspect/doctor` 必须无执行，不能为了判断登录态自动启动程序；浏览器授权
也不能伪装成 MCP 握手副作用。我们需要一个显式、通用、与业务插件无关的管理入口。

## Design / 设计

### 1. 字段是 opt-in，默认行为不变

以下是**待实现配置**，路径由用户安装位置决定：

```toml
[mcp.servers.team]
enabled = false
transport = "stdio"
command = "/opt/tools/team-mcp"
state_dir = true
setup = ["/opt/tools/team-mcp", "setup", "--events"]
executables = ["/opt/tools/team-cli"]
network = true
```

- `state_dir` 默认 false；true 时使用 `$GOLDER_HOME/extensions/mcp-team/home`，目录 0700、
  凭据文件 0600。HOME / macOS `CFFIXED_USER_HOME` 指向它；XDG config/data/cache/state
  指向其下各自子目录。TMPDIR 和单次 payload 仍使用临时目录，不把业务内容永久缓存。
- `setup` 是固定 argv，可执行文件必须固定、参数不做 shell 展开，不允许从模型参数拼 executable。
  需要多阶段授权时由这个程序编排，不把业务特例写进宿主。
- `executables` 是对依赖执行边界的补充：显式列出 helper 会调用的本地二进制。
  现有指纹只 hash 顶层 command，不会自动覆盖子进程。宿主在 Review 前解析并 hash 这些依赖，
  批准后只授予必要只读路径；**不接受运行中 server 自报路径后自动放行**。
  该字段是本次对 setup 方案的进一步澄清，需与实现一起评审；不是新 runtime。
  顶层 command 缺失仍不能启动；已声明的可选依赖缺失时记录 missing 状态、不授予执行资格，
  允许 helper 仅诊断/提供其他服务。依赖安装完成由宿主重新解析、更新指纹并要求 Review，
  不因 PATH 中突然出现同名程序就自动启用。
- CLI / legacy 本地来源适用同样语义；远端 HTTP MCP 没有本地进程，不提供 state_dir/setup。
- 持久目录内容会因 token 刷新而变，不进入配置指纹；源身份、目录能力、setup argv、
  executables、依赖哈希、网络包络进入指纹。状态内容不能重新授予信任。

### 2. state 是源专属能力，不是一般目录授权

宿主创建目录时拒绝符号链接和非本用户所有的目录，固定 source → state 映射，不接受任意路径。
OS 沙箱只给当前来源自己的目录读写；不向 bash、其他来源、项目目录或子 Agent 发目录 grant。
0700 只能隔离其他 OS 用户，不能隔离同 UID 的裸进程；同 UID 外部进程不在该保证内。

当前 `ProtectedGolderPaths()` 会封锁整个 Golder root。实现必须显式支持“只暴露该源 state”：

- macOS 编译保护规则时精确划出该子目录，其他 Golder 路径仍 deny。
- Linux 先遮蔽 protected root，再把该源 state 挂载到专用可写位置；必要的空父目录由宿主创建，
  不挂载包含 credentials/grants/transcripts 的父目录。
- 这只能由扩展管理器产生的 typed capability 启用；普通 `ReadDirs/WriteDirs` 不能重新打开 protected root。
- 两个平台都要做黑盒测试，不能只凭规则顺序推断生效，也不能取消整个 protected-root deny。
- setup 与业务调用共用同一 HOME、环境和沙箱构造器；重启 CLI 的 USER/hostname 等派生条件保持稳定。

同一源 setup/reset 与运行互斥；使用跨进程锁防止多个 golder 同时刷新/清空同一状态。
disable 中断正在运行的 setup，不承诺撤销服务端已经发出的令牌。

### 3. 管理入口复用现有命名

```text
golder mcp setup <id>             # 用户明确启动一次配置/登录流程
golder mcp reset <id>             # 明确确认后删除该源状态
golder plugin setup|reset <id>    # 本地 CLI/legacy 同义
/mcp setup <id>                   # TUI/REPL 入口
```

不另造 `golder lark` 或不存在的 `golder extension` 命令树。
`/extensions` 详情提供「配置/授权」「取消授权流程」「清除本地状态」。

- 先 Review 固定命令、依赖、网络、state 能力，再授权运行；enabled/trust/setup/authentication 独立。
- setup 不因 list/test/connect 自动触发，不能以“安装/修复”为名偷偷下载。
- 模型业务工具只能返回 `AUTH_REQUIRED` 和如何点击授权，不能默认启动 setup。
  用户从当前对话明确要求登录时，可以引导到同一管理入口；不是 server 的授权能力。
- reset 要先停止调用，单纯 disable 保留状态；remove 默认保留，显式 purge 才删。
  reset 是本地删除，不声称服务端授权已经撤销；完整登出/撤销按服务商流程另行处理。

### 4. setup 用少量结构化事件，而不是新增宿主万能 API

带 `--events` 的 setup 进程向 stdout 输出版本化 NDJSON；stderr 是有界脱敏诊断：

```json
{"v":1,"type":"notice","message":"正在准备授权"}
{"v":1,"type":"authorization","service":"team","verification_url":"https://auth.example.com/device?...","expires_at":"2026-10-28T10:10:00Z"}
{"v":1,"type":"complete","service":"team","authenticated":true}
```

setup 进程内部持有 device_code、轮询间隔、refresh token；宿主不需要它们，也不把它们交给模型。
服务商 CLI 原始 JSON 由插件适配，不能直接透传（可能含 token）。宿主只消费声明的展示字段，
未知字段忽略；事件/行/总输出限长，不把 notice 当命令执行。

这是 operator-only setup 辅助协议，不是 MCP wire 扩展，不注册为模型工具。
执行网络 setup 的批准只覆盖这个有限时长的授权会话，不转成永久业务网络授权。

### 5. 浏览器只负责显示，CLI 负责证明授权完成

用户点击「授权」表示同意打开对应的登录页；宿主校验 HTTPS URL、无 userinfo、精确可信 origin，
拒绝任意外部 URL、file/javascript/data 和任意 loopback 地址。允许的认证 origin 属于用户审阅的
setup 目的地配置，不能由 server instructions 或远端结果自行扩大；新租户/新 origin 需确认。

然后宿主通过现有 BrowserTool 的启动/授权 broker 打开**独立的授权浏览器实例**，
保留 Chromium 自身沙箱、scrubbed env 和现有 macOS browser-specific 授权要求。
这不是让插件继承浏览器例外。插件拿不到 CDP/控制 socket，也不执行 `open/xdg-open`。

- 浏览器优先显示官方扫码/登录页面；扫码、密码、MFA、确认权限由用户完成。
- 授权实例标记 sensitive，不向模型的浏览器发现、snapshot、network 或截图暴露此实例；
  共享启动基础设施，不共享授权页面的模型控制权。该隔离还需要宿主 UI 接线，不是当前已存在保证。
- 页面允许的 OAuth 重定向按配置 origin 约束；遇到未知重定向停下显示原因，不通过读取 cookies/token 解围。
- setup 按服务商 `interval` 轮询，遵守 slow_down、expires、取消；UI 收到事件马上展示，
  不等整轮模型输出，所以不会出现“用户看不到 URL，程序却在等扫码”的死锁。
- 授权成功以 CLI 的 token 落盘 + 服务端身份校验为准，不用页面文字/URL/截图判断成功。
- browser 缺失、授权被拒、只读模式、SSH 无可展示窗口时，提供同一 URL + 本地二维码备用；
  用户可用本机浏览器/手机继续。不开沙箱后门，不自动安装浏览器。
- 超时或取消即停止轮询，关闭本次 owned 页面/实例，清除待授权临时信息；绝不关闭用户原有浏览器。

## Rationale / 权衡

我们选择插件私有登录态，而不是挂载用户全局凭据：多扫一次码，但来源隔离、跨会话刷新都可解释。
文件加密的密钥与密文同在 source state 时，安全基线仍是文件权限和 OS 沙箱，不宣称能抵抗同 UID 入侵。
浏览器页面与 token 获取分离，因此不需要浏览器 cookie 提取或跨沙箱 localhost callback。

我们不把标准 MCP OAuth、sampling/elicitation 或 Host.Exec 加进来；setup 只运行用户批准的一个程序。
凭据过期后的 refresh 是已授权登录的维护；新的 scope、账号或租户仍要重新提示用户。

## Compatibility / 兼容性

旧配置默认临时 HOME，不改变运行行为。新增能力需显式 opt-in；不能把 `runtime=lark` 当兼容捷径。
旧版宿主不支持这些字段时必须给清晰的“版本不支持”提示，不能静默忽略后运行在错误 HOME 中。
现有工具策略、`--no-tools`、deny 优先、禁用即时撤销仍适用；setup 不调用业务工具。

## Implementation / 验收门槛

1. 配置/指纹/依赖审查、state 生命周期和双平台沙箱测试先落地。
2. setup executor + NDJSON、取消/过期/并发锁，再接 UI。
3. 复用浏览器 launcher 与 gate，补 sensitive auth 实例隔离；不可用有二维码回退。
4. 验证 refresh 能写回、重启仍有效、其他来源无法读、reset 后不再能用旧对象。
5. 假授权 server 覆盖 pending/slow_down/denied/expired/恶意 URL/伪造 success；
   真扫码需用户参与，在 macOS/Linux 分别验收，不能用源码阅读代替实测。
