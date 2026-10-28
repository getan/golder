# 独立 Agent：SSH Tab 退出与 thinking 回传调查

## 范围与限制

- 调查独立 `golder` agent 的 TUI/REPL 和 Responses provider，不归因于工作区守护进程，
  不修改 tuios-mini，也没有连接远端堡垒机或使用真实 API 凭据。
- 用户随后明确要求使用本机 Linux 容器，已恢复本次范围内的定向 SSH 验证。
  这不是恢复全量 Linux CI，也不等于覆盖了用户现场的堡垒机链路。

## thinking：旧修复仍在，但有采集与来源判断缺口

历史修复 `b5e476d` 修正了重放形状：reasoning head 在工具调用前，多个调用保持连续。
它没有被撤回；原有测试主要从预先构造好 `ReasoningItems` 的消息开始。

新增 SDK 级两轮模拟请求覆盖「接收 SSE → 会话 JSON 往返 → 提交工具结果」：

1. 原始 reasoning 已出现在 `response.output_item.done`，但最终 `response.completed.output`
   不重复它；原实现忽略该 done 事件的 reasoning，导致状态丢失。
2. 干净结束但没有 completed 事件的流，只保留了工具调用，遗漏已 finalized 的 reasoning。
3. 网关返回的模型标签与请求选择的模型别名不同；原实现用 `ResponseModel` 严格比较，
   即使下一轮仍选择同一模型也会丢弃已保存的原始状态。
4. `content` 使用 `thinking/thinking` 而非 `reasoning_text/text`；原有判断误认为没有正文，
   额外插入 missing-state placeholder。空终态重复壳同样需要保留 richer 的 done 记录。

修复保留 finalized 原始 JSON、按 ID 去重/补齐终态遗漏，并以请求模型记录来源。
跨 provider/跨模型仍不回传 opaque 状态；不拿 UI 摘要重建私有 thinking，不改签名，不重跑工具。
上述模拟用例在修复前复现状态丢失/错误占位符，在修复后通过。它们证明代码缺口，
不是用户那次真实请求的 wire dump；仍需版本、模型名或安全诊断把具体触发分支对应起来。

已丢失在旧历史中的私有协议状态不能凭空恢复，不能承诺只升级就一定修复每个已有会话。

## SSH `/pro` + Tab：尚未复现，不能宣称已修复

- 增加 55 组窗口尺寸的 SSH 环境 TUI 补全测试：Tab 只填入候选，不执行命令或退出。
- 增加独立 TUI 的实际 PTY 输入/渲染测试：直接发送 `/pro` 与 ASCII HT，完成后继续输入，
  不依赖任何工作区/多路复用器；当前本地通过。
- REPL 原始按键循环也覆盖 `/pro` + Tab + 继续输入 + Enter，不应提前提交或返回 EOF。

这些结果不能否定远端现象，也不能定位远端进程退出/SSH 断开原因。
下一步需用户机器的 `golder --version`、终端/启动方式，以及退出前 stderr/崩溃堆栈。

## 本机 Linux 容器的真实 OpenSSH 验证

复用运行中的 `golderlinux`（Linux arm64、OpenSSH server），从宿主真实 SSH 到
`127.0.0.1:2222`，登录用户为 `golder`（UID 1000）。没有通过工作区或多路复用器启动 agent。
使用隔离的临时 HOME/GOLDER_HOME、fixture API key 和不可用的本机 API 地址；不读取既有
账户配置，也不发起真实模型请求。

测试动作是 `/pro` → Tab → 继续输入 `-TAB-ALIVE` → 最后显式 Ctrl+D。
共 9 组：

- 当前 Linux 二进制，TERM 为 xterm-256color / xterm / vt100 / linux，80×24。
- 当前二进制，xterm-256color，120×40。
- 现存 10 月 26 日 Linux 二进制对照。
- Kitty `CSI 9;1u` 和 modifyOtherKeys `CSI 27;1;9~` 两种 Tab 编码。
- 开启内置工具、选择 opencode-go / deepseek-v4.1-flash 的对照。

9 组在 Tab 后 SSH/agent 均保持运行并能继续输入；最终退出发生在显式 Ctrl+D 之后，
agent/SSH 退出码均为 0。未见 panic 或 fatal error。原始终端流和安全阶段日志保存在
`.build/ssh-agent-tab-evidence/`，探针为 `.scratch/ssh_tab_probe.py`（仅调查工具，不随产品发布）。

另外，将 TUI 和 provider 测试交叉编译成 Linux 原生测试二进制，通过 SSH 在该容器中实际执行：
独立 PTY/55 组尺寸测试、thinking 两轮模拟请求、模型来源隔离、顺序去重及既有 reasoning
回归均通过。这些是 Linux 运行结果，不再仅是 Mac 模拟或交叉编译。

现有 SSH 服务禁止转发，未改其安全配置来模拟跳板；未覆盖用户现场 root 身份、具体堡垒机
终端实现、真实启动脚本和已有会话内容。当前结果仍是「真实 SSH 条件下尚未复现」，不是修复声明。
