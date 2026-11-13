# 统一前端权限运行时，并以显式工具链能力执行构建

Author: Golder contributors
Last updated: 2026-11-13
Status: Implemented; local acceptance completed
Discussion: 本次 GUI/TUI Go 缓存访问不一致的排查与用户确认。

## Abstract / 摘要

我们将权限启动契约收敛到共享运行时，增加显式的 Go 执行配置，并让同一份结构化决策进入工具结果和前端卡片。GUI 与 TUI 在相同环境、授权和命令下必须生成相同的沙箱权限。工具链配置是权限请求，不是自动白名单；它不授予网络、不关闭沙箱，也不允许读取整个 HOME。

## Background / 背景与动机

CLI 在 `cmd/golder/main.go` 调用 `grants.Open` 并导入 `GOLDER_READABLE_ROOTS`；GUI 使用相同的工具和 gate，却遗漏了这段启动步骤。`grants.Process()==nil` 因此被当作空授权表：持久授权没有加载，新授权也无法登记。这是生命周期缺失，不是有意设计的 GUI 安全等级。

默认系统根不包含 `$HOME/go/pkg/mod` 和用户构建缓存。当前机器的 GOROOT 本身也位于下载工具链的模块缓存内。仅批准“运行测试”和批准“读取工具链、写缓存”不是同一个决定。现有 `require_escalated` 又把这些狭窄需求升级成了整个进程逃逸。

目前工具结果只有 `envelope` 和泛化的 `escape not granted`。虽然 review note 含来源和理由，模型常常无法知道拒绝来自用户、reviewer、reviewer 不可用还是 enforce。我们不能从 EPERM 或一段自然语言反推授权事实。

## Design / 设计

### 1. 进程只初始化一次，项目切换不重置授权

增加共享 `permissionruntime.Initialize(getenv)`，负责打开/迁移授权表、导入显式环境读根并返回状态和警告。CLI 和 GUI 的正常启动均在构造工具前调用它。浏览器 helper、文件 worker、终端 bootstrap 的早期入口仍在初始化之前返回，不能在隔离子进程中加载宿主授权文件。

重复初始化不替换现有表、不丢失会话授权。初始化失败与空表是不同的状态：启动入口明确报告错误；不能声称已记住授权。现有 remembered 格式和会话内拒绝规则继续适用。此改动不把进程会话授权变成跨进程授权，只有 remembered 授权跨启动加载。

### 2. Bash 显式请求工具链配置，不从命令字符串猜测

新增可选参数 `profile: "go" | "go-download"`：

```json
{"command":"go test ./...","profile":"go"}
```

`go` 请求只读 GOROOT、只读模块缓存、可写构建缓存及存在时的精确 GOENV 配置文件。`go-download` 另请求模块缓存写入权限；下载仍须独立的 `network: "on"` 决策。配置覆盖整条 shell 命令，不声称只能由编译器访问这些路径，reviewer 必须审查完整命令。

探测器只查询已安装、系统运行时目录中的真实 Go 二进制，不执行工作区提供的同名程序。探测使用非项目目录、`GOTOOLCHAIN=local`、`GOWORK=off` 和关闭代理/校验网络的环境，只查询路径字段，不输出 GOENV 内容或代理凭据。输出有大小和时间限制。自定义非系统安装在不能证明可信时拒绝自动探测，用户仍可使用现有显式路径授权。

profile 先检查 PATH 中可识别的系统安装，再检查 `/opt/homebrew/bin`、`/usr/local/go/bin` 等标准位置，不执行 HOME 中的 shim。实际执行时将选定 SDK 的 bin 放到 PATH 首位，使用与它匹配的 GOROOT；这项选择进入 manifest 和授权说明。它不重定向 GOCACHE/GOMODCACHE/GOPATH。测试发现 `go test` 宿主会导出自己的 GOROOT，因此不能只更换 PATH 而保留宿主 GOROOT，否则会出现 Go 编译器版本不一致。

所有路径规范化，拒绝文件系统根、HOME、受保护 Golder 状态和已知凭据目录。环境变量只提供候选路径，不等于授权。探测器是可扩展注册点；首版只实现 Go，不增加 Python/Node 的隐式行为。

### 3. 一份 manifest 贯穿审核与执行

gate 在分类前解析 manifest，向 reviewer 和人展示目录、读写能力及 profile 名称。新增工具链 capability 使用现有统一 consent 流程；只有其请求实际被批准才产生执行上下文中的 `ToolchainGrant`。

一次授权只进入该次 `CallPolicy`，不能偷偷变成进程目录白名单。会话/记住授权复用现有 grants 表，并受更具体的拒绝规则约束。配置已经被路径授权完全覆盖时不重复询问，但仍绑定 manifest。

manifest 对二进制身份、配置名称、规范路径和相关配置作指纹。Bash 在启动前重新解析并比较；路径、SDK 或配置改变则拒绝启动，不把旧授权用于新目录。模型不能通过参数构造授权上下文。文件隔离始终保留；profile 不产生 Direct/Bare、网络或凭据能力。

显式 profile 的运行中拒绝不触发旧的路径重新登记/自动重跑机制；一次命令可能已经有部分效果，新增需求须作为新调用重新审核。模块缓存与构建缓存不能重叠，构建缓存不能覆盖 SDK；可写缓存也不能落在系统安装树或已知凭据存储中。

### 4. 拒绝原因是数据，不是猜测

统一决策记录包含 mode、source、reason code、rationale、是否请求/批准逃逸。至少区分：`user_declined`、`approval_unanswered`、`reviewer_denied`、`reviewer_contained`、`reviewer_unavailable`、`sandbox_enforced`、`profile_unavailable`、`profile_changed`。执行结果另记录实际 envelope。blocked 与已执行失败必须分开。

GUI、TUI、模型结果共享这些字段。旧自然语言消息保留兼容，但模型不再只能看到 `not granted`。GUI 的会话批准缓存必须把能力需求纳入 key；相同命令但缓存路径或写权限改变，不能复用旧答案。

## Rationale / 理由与取舍

- 不在 GUI 复制 `grants.Open`：复制启动逻辑还会在下次迁移时分叉。
- 不在 `SetupEnv` 每次 Open：GUI 创建项目环境会多次调用它，会覆盖正在使用的进程授权表。
- 不自动允许 GOPATH/HOME：模块缓存可能包含私有代码，可写缓存也有跨项目污染风险；用户必须知道具体路径和能力。
- 不自动识别任意 `go ...` shell 字符串：环境覆盖、函数、脚本和命令链不能可靠地推导全部影响。显式 profile 更可审查。
- 不为每次构建请求完整逃逸：目录能力能在现有 OS 沙箱内满足需求，应优先保留边界。
- 不通过改写 GOCACHE/GOMODCACHE 到仓库“解决”拒绝；默认工具链布局不变。

探测和启动前复核增加一次受限查询的成本。这比缓存一个可能过期的权限 manifest 更诚实。首版不承诺支持任意 HOME 下的工具链包装脚本；支持这些安装需要显式可信安装来源，而不是降低探测边界。

## Compatibility / 兼容性

不带 profile 的 bash 行为不变，旧 grants 文件无需改版。GUI 会开始加载之前被遗漏的明确 remembered 授权和拒绝，这是恢复一致性而不是扩大默认系统白名单。用户会话权限仍不因另一个进程启动而自动继承。

`auto/ask/read-only/full-access/enforce` 的现有语义保留。read-only 不允许构建写缓存；enforce 下可以批准沙箱内 profile，但不能批准逃逸。无可用 OS runner 时不以 profile 名义承诺隔离。

## Implementation / 实现与过渡

1. 共享启动服务与 CLI/GUI 接线；测试重复启动不重置授权、持久拒绝/授权加载、失败可观测。
2. Go manifest 探测与纯校验、指纹；先测试 PATH 劫持、宽路径、凭据目录和配置变化。
3. consent capability、执行上下文与 `CallPolicy` 接线；测试未批准/篡改参数不可获得目录能力，一次授权不污染下一次调用。
4. 结构化拒绝原因和 GUI 审批缓存身份；保留旧卡片兼容性。
5. 端到端验证：同一 gate/Bash 执行路径能在沙箱内读模块缓存并写构建缓存，但不能读未授权 HOME 文件，拒绝后不重放命令、不降级为裸执行。

验收包括共享启动与前端等价测试、manifest 与授权绑定测试、默认/下载配置的能力差异、完整 race/vet/build 和 Linux arm64 交叉编译。本机不构建 GUI 安装包，不启动暂停中的 Linux 容器。

### 实施记录

- 已实现 `internal/permissionruntime`，CLI/GUI 正常启动使用相同初始化入口；AST 启动契约测试防止再次遗漏，运行时测试覆盖持久授权加载、环境读根导入、重复初始化保留会话拒绝，以及损坏授权表明确失败。
- 已实现 `internal/toolchain` 的 Go manifest、`CapToolchain`、host-only `ToolchainGrant` 上下文、执行前复核和沙箱目录配置。测试覆盖一次授权不污染进程表、会话授权复用、拒绝优先、SDK/模块写入隔离、包装脚本不执行、配置变化拒绝启动及真实 executor 传递。
- 已实现 `AuthorizationDecision` 和实际 execution envelope：拒绝原因进入 gate note、工具 Details 和模型可见结果；GUI note 保留 `DecisionCode`，审批缓存身份包含能力列表及 manifest 指纹，不再只比较命令摘要。
- macOS 原生验收实际通过：`GOLDER_GO_PROFILE_TEST=1 go test ./internal/toolchain ./internal/agenttool -run 'TestGoInstalledProbeLive|TestBashGoProfileNative' -v -count=1`。系统安装的 Go 在 Seatbelt 内使用默认缓存完成编译/测试，读取模块目录；同一测试中的未授权 checkout 私有夹具读取返回权限拒绝。测试夹具位于本工作区并已清理，没有读取用户私有文件、关闭沙箱或重定向缓存。
- 本机完整验证通过：`go test -race -timeout 6m ./...`、`go vet ./...`、`go build ./...`、`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...`；`gofmt -l .` 无输出，`git diff --check` 通过。不把交叉编译等同于 Linux 桌面/内核上的沙箱验收。Linux 容器预检保持 suspended，没有生成 GUI 安装包，也没有自动修改用户的授权配置或部署二进制。
