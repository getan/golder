# 现场调试：独立 Golder 在 SSH 中按 Tab 退出 / thinking 回传 400

这份文档可以直接交给出问题机器上的 agent。目标是**先留下证据，再修改代码**。
调查对象是独立 `golder`，不要把问题默认归因于 tuios-mini，也不要先清空配置或历史。

## 1. 现场 agent 的约束

- 首次复现保留原来的用户身份、目录、配置、会话、终端和启动参数；不要先加 `--approve`、
  禁用扩展、改 TERM、切换协议或关闭 thinking，这些会掩盖现场差异。
- 不删除/修复旧会话，不自动重跑已经执行过的工具。新会话只作为第二次对照。
- 不打印 `env` / `set` / `export -p`，不上传配置文件、会话正文或凭据。
- **不要启用 `TEA_TRACE`、HTTP 请求体日志或终端全量录屏。** 它们可能保存输入正文、
  token、工具参数及原始 thinking/signature。此处只使用安全的结构化诊断。
- 不修改 sshd、堡垒机、sudo 或 ptrace 的安全策略；权限不足则记录限制。

## 2. 确认二进制包含本次日志改动

仅设置环境变量不能给旧版二进制增加新日志。需要含本次诊断补丁的构建；不要未经确认就
更新或覆盖现场正在使用的程序。若现场有同步后的源码，可另行构建：

```bash
GOWORK=off go build -o ./golder-debug ./cmd/golder
./golder-debug --version
sha256sum ./golder-debug
```

下面示例按平时的 `golder` 启动。若使用 `./golder-debug`，请替换函数内版本/启动两处调用，
并让 `binary=` 指向实际调试二进制，避免记录旧 PATH 程序的校验和。保留原参数。
说明是否有 alias/function/包装器即可，**不要贴可能含密钥的定义**。

## 3. 最小采集：保持 stdin/stdout 连接终端

在出问题的 SSH 会话中用 **Bash** 执行下列函数。它在子 shell 内工作，不改父 shell 设置。
只重定向 stderr，**不要把 Golder 的 stdout 管道到 tee**，否则会改变 TTY 判断。

若平时是 `exec golder`、SSH forced command 或受管启动，这个函数不是等价的进程启动链。
先记录该事实，尽量在原入口只增加诊断环境变量和 stderr 重定向；不能保留时说明差异，
不要因为新增了外层 shell 后“不再登出”就宣称解决了问题。

```bash
golder_debug_run() (
  set +e
  umask 077
  d=$(mktemp -d "$HOME/golder-debug.XXXXXXXX") || exit 1
  printf '\nDebug evidence: %s\n' "$d"
  {
    date -u '+utc=%Y-%m-%dT%H:%M:%SZ'
    golder --version
    uname -srm
    printf 'uid=%s\n' "$(id -u)"
    printf 'TERM=%s\nCOLORTERM=%s\nTERM_PROGRAM=%s\n' \
      "${TERM-}" "${COLORTERM-}" "${TERM_PROGRAM-}"
    if [ -n "${SSH_CONNECTION-}" ]; then echo 'ssh_connection_set=true'; else echo 'ssh_connection_set=false'; fi
    if [ -n "${SSH_TTY-}" ]; then echo 'ssh_tty_set=true'; else echo 'ssh_tty_set=false'; fi
    if [ -t 0 ]; then echo 'stdin_tty=true'; else echo 'stdin_tty=false'; fi
    if [ -t 1 ]; then echo 'stdout_tty=true'; else echo 'stdout_tty=false'; fi
    if alias golder >/dev/null 2>&1; then echo 'shell_alias=true'; else echo 'shell_alias=false'; fi
    if declare -F golder >/dev/null; then echo 'shell_function=true'; else echo 'shell_function=false'; fi
    stty size
    stty -g
    binary=$(type -P golder)
    if [ -n "$binary" ]; then sha256sum "$binary"; fi
  } >"$d/environment.txt" 2>"$d/environment-errors.log"
  unset TEA_TRACE
  if GOLDER_TUI_TRACE="$d/agent.trace.log" GOTRACEBACK=all \
      golder "$@" 2>"$d/stderr.log"; then
    rc=0
  else
    rc=$?
  fi
  printf 'exit_code=%s\n' "$rc" >"$d/exit.txt"
  date -u '+utc=%Y-%m-%dT%H:%M:%SZ' >>"$d/exit.txt"
  printf '\nGolder exited: code=%s; evidence=%s\n' "$rc" "$d"
)

# 使用平时相同的启动参数；不要额外写入密钥。
golder_debug_run
```

操作：等 composer 出现 → 手动输入 `/pro` → **按一次 Tab，不按 Enter** → 观察能否继续输入。
记下时间，区分“Golder 退回 shell”和“整个 SSH 断开”。若未退出，继续输入几个无敏感内容
的字符，再用 Ctrl+D **主动结束对照测试**，在报告里注明这次退出是有意的。

若 SSH 断开，重新登录后按刚才打印的目录找文件。`exit.txt` 缺失可能是整个 shell 也被终止，
**不等于已证明 Golder 收到了某个信号**。不要为了一次没有退出码就重复执行真实模型工具。

### 采集文件的安全级别

- `agent.trace.log`：按设计只记录类别、数量、布尔值、耗时和编译后的函数名/行号；0600。
  不记录普通字符、命令名、提示词、thinking、签名、URL、会话 ID 或请求体。
- `environment.txt`：白名单终端元数据、版本及校验和；可能包含本机路径，分享前检查。
  若 PATH 二进制被 alias/function 包装，校验和只表示 PATH 上的程序，不代表整个启动链。
- **`stderr.log` 不保证脱敏**：运行时 panic 和上游错误可能含正文、URL、文件路径甚至凭据。
  现场 agent 先本地检查，只提供最小脱敏片段，不能把整个目录直接公开上传。
- 日志每次单独建目录，避免把不同 PID/版本/复现过程混在一起。开启后事件较多，只记录
  最小复现，不要长期常开。日志初始化失败会在 stderr 提示，需检查目录权限/磁盘空间。

## 4. `/pro` + Tab 的判读顺序

按同一个 `pid=` 的时间线查看，不要只凭最后一行断言根因：

| 事件 | 含义 / 下一步 |
|---|---|
| `tui control key=tab` | 解码器交给 Model 的是 Tab；只记固定控制键类别，不记按键正文。若现场按 Tab 却出现 `ctrl_d`，重点检查终端/堡垒机键映射。 |
| `tui completion begin` → `apply` → `end` | 进入补全、即将填入候选、补全返回。包含窗口尺寸、输入字节数、候选数、选中索引、运行/退出状态，不含候选名或输入。 |
| `completion apply` 后没有 `end`，并有 `panic phase=tui.update` | 查看紧随的 `panic frame=… function=… line=…`；定位 SetValue / refresh 等实际失败位置，不要先修改 SSH。 |
| `tui exit intent source=ctrl_d accepted=true` | 命中了明确的 Ctrl+D 退出分支，不等于内核发了 EOF。`accepted=false` 表示运行中拒绝退出。 |
| `source=double_interrupt` / `source=slash_exit` | 命中了双重中断或 `/exit` 分支。不是 Tab 自身的退出逻辑。 |
| `input read … error_class=eof` | 底层输入结束，和已解码的 Ctrl+D 是两件事；检查 fd/TTY/SSH 生命周期。 |
| `output end … error_class=broken_pipe` / `io_error` / `bad_fd` / `closed_file` | 输出或文件描述符异常；结合前后时间线判断，不直接归罪于某个组件。 |
| `tui program end … error_class=panic` | Bubble Tea 报告 panic；安全栈或私有 stderr 可能有进一步证据。 |
| `… error_class=interrupted` / `program_killed` | 框架报告中断/取消；**不代表已识别具体 OS 信号**。底层 I/O/context 原因可显示为对应类别。 |
| `main exit code=…` + `diag trace end` | 程序走到了显式退出/关闭日志的路径；与是否还有外层 shell 是两个问题。 |

如果连 `control key=tab` 都没有，先看 `input read` / `input decode.*` / `input deliver.*` /
`update begin/end`，区分没读到、解码/投递阻塞还是 Update/View/终端写入卡住。
`output begin` 没有对应 `output end` 也可能是写阻塞，而非进程退出。

安全 panic 日志**不会记录 panic 值**，并会原样重新抛出，不能把它当成修复。
仅覆盖已布点的 TUI Init/Update/View/Run；其他 goroutine、SIGKILL、机器重启、文件系统故障
不保证留下栈或正常尾行。正常尾行缺失本身不能区分这些原因。

### 新增证据：单独到达的 ESC 可将协议片段变成退出动作

收到的 `32;21;55M`、`0;23;55m` 等片段与 SGR 鼠标上报相符：完整形式例如
`ESC [ < 32 ; 21 ; 55 M`，末尾小写 `m` 表示释放。`…;5u` 与增强键盘的 CSI-u 报告相符。
它们不是随机乱码，也不能仅凭显示片段确定原始输入的完整字节。

在 v1.5.16 相同代码的 Linux 原生构建上，通过真实 OpenSSH 做了可重复对照：

- 同批送入完整的增强 Tab、鼠标按下/拖动/释放报告：agent 保持运行，之后显式 Ctrl+D 退出。
- 将每条报告的第一个 `ESC` 独立发送，等待约 250ms，再发送其余内容：当前 50ms 的裸 Esc
  歧义超时会先生成 Escape 键，后续尾部变成普通输入。打开补全菜单时，第一个 Escape
  关闭菜单；后续两个 Escape 在 3 秒退出确认窗口内命中 `double_interrupt`，agent 正常
  退出码为 0，外层 SSH 命令随之结束。此过程没有执行 `/provider`。

日志出现 `control key=escape` → `exit armed source=interrupt` →
`exit intent source=double_interrupt accepted=true` → `main exit code=0`。
测试证据在 `.build/ssh-agent-tab-evidence/reports-delayed-single-escape.*`，完整报告对照为
`reports-coalesced-control.*`。这是构造分段/延迟条件的确定性复现，**不等于已证明现场堡垒机
采用同样的分段方式**。

这也说明只有“任意分割边界”测试不够：起始 ESC 与后续字节之间实际经过多少时间同样重要。
已有完整 CSI 前缀的保留策略不能消除裸 Esc 与一条尚未到达的协议报文之间的固有歧义。
不能用正则删除普通的 `32;21;55M` 文本来补救，否则会破坏正常输入和粘贴。

在这个本机样本中，正常退出输出**包含**鼠标/键盘模式的关闭序列。现场失联后鼠标仍上报，
还可能涉及关闭序列未能送达、程序/读取循环停止或客户端模式残留，须单独核对，不能把
这个退出路径说成解释了全部现象。退出键策略修复仍待确认。

若现场已经无法输入，先停止移动鼠标，使用终端应用的 **Reset/重置终端**（不是仅清屏），
或新开终端标签再连接。不要在不知道当前输入交给谁的状态下盲贴恢复命令或真实凭据。

## 5. 需要区分 OS 信号时：可选的第二次对照

程序没有额外注册信号处理器，避免为了诊断改变原来的 HUP/TERM/INT 行为。
若现场已安装且允许使用 `strace`，可另建私有目录做一次信号级对照：

```bash
umask 077
d=$(mktemp -d "$HOME/golder-signal-debug.XXXXXXXX")
printf 'Signal evidence: %s\n' "$d"
strace -tt -e trace=none -e signal=HUP,INT,QUIT,TERM,PIPE \
  -o "$d/signals.log" \
  env GOLDER_TUI_TRACE="$d/agent.trace.log" GOTRACEBACK=all \
  golder 2>"$d/stderr.log"
```

保留原参数，必要时把 `golder` 换成现场实际二进制路径。这次 `env golder` 不会执行 shell
alias/function，若原启动依赖包装器，必须说明差异。strace 会影响时序，只是补充证据。
**不要改成记录 read/write/execve 或 `-e trace=all`**；本例只记录信号/退出，不记录数据缓冲区。
若未安装、ptrace 被禁止或不允许使用，跳过，不提权、不改系统策略。

## 6. thinking 400：同一开关提供结构化回传日志

为这个问题另建一次采集，保留原会话及原模型，复现一次即可。不要输出原始 SSE、请求体、
thinking 或签名。下列事件只对 **Responses 驱动**生效，其他 wire 没有这些事件是正常的；
不要为得到日志而强行换协议。

`responses request=N` 是本进程内的递增尝试编号，**不是上游 request/session ID**。
每次重试使用新编号；`replay step=K` 的 K 是完整消息列表的零起始下标。

| 事件/字段 | 判读 |
|---|---|
| `request.start … thinking_on=…` | 这次是否真正按 reasoning 模式构建请求；不记录模型名和 URL。 |
| `capture completed=… finalized=… terminal=… stored=…` | finalized 是 item.done 缓存数，terminal 是终态中原有数，stored 是合并保存数。例如 `1/0/1` 表示终态漏项已从 done 恢复。 |
| `head_nonempty`、`reasoning_text_parts`、`thinking_parts`、`other_parts` | 原始协议是否有可识别正文及 content 类型数量；不代表签名已被服务器验证。 |
| `model_alias=true` | 返回模型标签与请求选择不同；只记比较结果，不记标识。 |
| `replay … disposition=not_recorded` | 旧消息根本没有保存原始状态；不能用 UI 摘要重建。 |
| `disposition=provider_mismatch` / `model_mismatch` | 来源隔离导致不回传；核对是否确实切换，及 `model_source` 是否仍是旧的 `legacy_response`。不要直接取消隔离。 |
| `disposition=no_valid_items` | 有记录但没有合法 reasoning 项；调查保存/恢复或导入格式。 |
| `disposition=replayed … placeholder=false` | 已选择原始状态回传；若仍报 400，继续核对协议/网关契约，不自动重试已经执行过的工具。 |
| `placeholder=true` | 该步骤缺少可识别的原始正文，插入了现有 missing-state 安全网；它不是恢复出来的私有 thinking，也不保证每个网关接受。 |
| `request.end … error_class=thinking_replay_rejected http_status=400` | 上游消息符合缺少 thinking/reasoning 回传的已知文本；日志不保留原始错误正文。 |

首次保留旧会话取证，之后再用新会话作对照，区分旧历史状态已丢失和新代码仍采集丢失。
不要为了修复这个错误伪造签名、用显示用摘要替代协议状态或反复执行工具。

## 7. 交回报告（只含最小脱敏证据）

1. 二进制版本/校验和，是否包含本次补丁；普通用户/root；终端类型、尺寸及是否经过堡垒机。
2. 平时的启动方式（是否有包装器）、新/旧会话、触发动作及时间；不要附含密钥的完整命令。
3. 是仅 agent 退出、SSH 断开还是卡住；退出码和是否有正常尾行。注明主动 Ctrl+D 对照。
4. 相关 PID 的安全日志片段：控制键、completion、exit、program end、panic 或 responses 事件。
5. 如有必要，经过本地脱敏的 stderr 栈/错误片段及信号日志；不附会话、配置、token 或私有正文。
6. “已观察事实 / 推测 / 尚未覆盖”分开写，给出最小复现；不要因为本机容器没复现就否定现场。

本机已通过普通用户的 Linux OpenSSH 对照，但不覆盖现场 root、具体堡垒机实现和已有会话。
前期调查见 [standalone-agent-regressions.md](standalone-agent-regressions.md)。
