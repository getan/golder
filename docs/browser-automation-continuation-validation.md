# 浏览器自动化恢复：本地验收记录

日期：2026-11-11。Golder 基线 `27fe62f`；kitty-browse 基线 `v0.2.11`。
本记录区分实现、本地 fixture、真实 Chromium 和未做的现场验证。

## 已实现

- GUI 抽屉／隐藏抽屉时的 composer、主 TUI 卡片、浏览器 pane 均有恢复入口；
  TUI/pane 的按钮不通过注入 Ctrl+G 实现。鼠标按下／释放、Tab/Enter/Space、
  确切本地命令及单一已展示请求下的完整确认短语接入同一条件恢复。
- 随机 RequestID、五分钟期限、最多八个待决 view；epoch/generation 变化使旧请求
  无效。未知传输结果只读查询回执，不重发。过期／明确未消费请求可由人刷新。
- host 临界区 CAS、五秒稳定点、每 view 最多 64 条十分钟回执、关闭撤销、
  私有 operator 来源隔离、challenge 绑定的 view/runtime 证明。
- 浏览器暂停不改变归属。profile 字符串不足以恢复权限；已证明的复用不重复
  索取启动能力。命名 profile、外部浏览器、read-only、deny/contain/enforce 不放宽。
- 浏览器恢复与原任务接续分开；仅同一仍在等待的 run 可调度一个新 turn，要求
  重新观察，不重放旧提交。已取消／替换／完成的任务不被复活。

## 执行过的检查

开发阶段使用显式、仅本地的 Go workspace 同时加载两个模块。配套变更已通过
[kitty-browse PR #5](https://github.com/getan/kitty-browse/pull/5) 合入，公开版本
`v0.2.12` 对应 `0064264b9c740fd9871fd9c9b556c0ce3754c3a8`，上游 CI 通过。
Golder 已锁定该版本及校验和；最终检查使用 `GOWORK=off` 和 `-mod=readonly`，
`go list -m` 确认无 kitty-browse 本地 replace。临时开发 workspace 已移除。

| 检查 | 结果与边界 |
|---|---|
| kitty-browse `go test -race ./... -timeout 3m` | 通过；没有排除用例 |
| kitty-browse `go build ./...`、`go vet ./...` | 通过 |
| kitty-browse Linux/arm64、CGO 关闭的 `go build ./...` | 通过；不是 Linux 运行时测试 |
| Golder `go build -mod=readonly ./...`、`go vet -mod=readonly ./...` | 通过；`GOWORK=off`、公开依赖 |
| Golder Linux/arm64、CGO 关闭的 `go build -mod=readonly ./...` | 通过；`GOWORK=off`、公开依赖 |
| Golder 全仓 race | `GOWORK=off go test -mod=readonly -race -timeout 6m ./... -skip '^TestControlledPaneActualOSIsolation$'` 通过；明确排除下述已复现基线失败 |
| 新增 coordinator／GUI renderer／TUI／adapter／judge 定向 race | 通过 |
| 格式与 diff 空白检查 | `gofmt -l` 无输出，`git diff --check` 无输出 |
| Linux container preflight | **suspended**；未启动容器 |

本地安装的 Chromium 已运行 localhost 协议 fixture，包括可见挑战阻止输入、
私有恢复后仍存在的挑战重新锁存、主文档 429 与 Retry-After 保留。测试使用临时
profile 并清理运行时；未下载 Chromium，未操作真实账号。

## 独立复现的基线问题

`TestControlledPaneActualOSIsolation` 在本机返回 `outside fixture leaked:
private-fixture`。同一测试在未修改的 `27fe62f` 临时 worktree、相同测试环境中也
失败。本次未改动其隔离策略或断言。不能因此宣称完整、未排除的 Golder suite
全绿，也不能把该环境的外层沙箱保护描述为已现场验证。

## 未声称完成的现场验证

- 没有指定账号／任务／权限范围的真实飞书登录、授权与业务任务。
- 原生 GUI 窗口的人工点击回归；GUI 点击覆盖来自 renderer 测试驱动。
- 实际 PTY／终端复用器的鼠标和快捷键冲突回归；TUI/pane 控件覆盖来自事件模型测试。
- Linux 容器中的运行时 race／沙箱测试。

这些限制不是新的自动化授权。不得为验证它们导入个人 profile、读取 Cookie、
自动通过验证码、扩大应用授权范围或轮换身份／代理。
