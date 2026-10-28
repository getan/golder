# SSH workspace 渲染与背压调查

## 本次实测和改动

在 tuios-mini 的独立源码仓库实现 SSH 客户端精简策略：不依赖 Nerd Font 的 ASCII
装饰、小型纯文字欢迎界面、无渐变/大背景/悬停装饰/动画，关闭时钟、系统表、自定义 dock/rail
刷新和屏保。保留窗口、会话、工作区、输入、滚动、选择、通知与必要操作状态。

后台 pane 输出合并到最多 10Hz，输入仍走即时响应窗口。鼠标默认请求 click/wheel/drag，
不请求每次无按键移动；pane 内应用明确要求全移动上报时保留其功能。没有增加输入正则过滤，
也不丢弃程序输出、中文或程序自己的 ANSI 颜色。

ASCII 文件列表沿用已有单列 fallback：`>` 目录、`^` 上级、`.` 文件，保留文件名/扩展名。
不同文件类型的 Nerd Font 图标及独立配色在 SSH 下关闭；单个图标本身不是一分钟卡顿的证明。

配置只作用于客户端模型，不写回用户配置，不污染同进程浏览器客户端。OpenSSH 上启动的
本地进程也会识别 SSH 环境；并非只覆盖 tuios 的内建 SSH server。
诊断对照可用 `TUIOS_SSH_FULL_UI=1` 恢复 OpenSSH 本地客户端的完整 UI。

## 真实 SSH 限速对照

普通测试用户、Linux arm64、120×40、隔离 HOME/workspace、没有真实模型调用。
通过本机 TCP 代理把 SSH 服务端到客户端的流限速为 4096 B/s；输入方向不额外限速。
相同流程执行 80 次计数刷屏、期间输入回显、切空工作区、空闲、注入鼠标移动、打开帮助、退出。

| 项目 | 原版 | 精简版 |
|---|---:|---:|
| 完整测试流程收到的总输出 | 22,770 B | 9,292 B |
| 同一刷屏区间的输出 | 3,466 B | 1,015 B |
| 刷屏中输入回显延迟 | 259 ms | 63 ms |
| 空工作区空闲 1 秒输出 | 0 B | 0 B |
| 495 次无按键鼠标移动引发输出 | 30 B | 0 B |
| 帮助标题到达延迟 | 1.195 s | 0.911 s |

总输出约减少 **59%**，刷屏区间约减少 **71%**。不限速的本机 SSH 两版均可毫秒级响应。
这些不是生产堡垒机的测量，标题/首帧标记到达也不等于整个界面已完整显示。
原版在这个空闲样本中没有持续输出，因此**不能宣称已证明或解决现场启动后的一分钟卡顿**。

证据在 `.build/ssh-workspace-evidence/`，分别为 `baseline-4k-repeat` 和 `reduced-final-4k`。
`.scratch/ssh_workspace_probe.py` 仅用于隔离测试；不要对真实会话自动录制原始终端流。

## 现场如何定位剩余一分钟

先确认实际二进制版本，以及是否连接的是升级前已启动的 workspace daemon；升级客户端
不会自动替换已有 daemon。保留原会话，不盲目 stop、删除状态或改权限。

使用 [调试文档](debug-ssh-tab-exit.md) 的私有采集函数，传入平时相同的 workspace 参数：

```bash
golder_debug_run workspace
```

按 PID 区分客户端、daemon 和 pane agent，优先看：

- `workspace owner.probe` / `owner.spawn`：运行前的探测、启动是否慢。
- `workspace client.run`：进入了嵌入客户端准备/渲染路径。
- `workspace.input`：是否读到、解码并投递按键；不能把正常 idle poll 当成阻塞证据。
- `workspace.stacks`：5 秒的函数名采样可区分终端写、配置读取、附着/恢复和 daemon RPC。
  现有采样在 client.run 前启动，不覆盖更早的命令行/状态路径读取。
- 若栈在终端写路径，结合输出字节量和实际链路限速判断背压；若在 `GetTerminalState` /
  `sendAndWaitMatching` / `recv`，需查 daemon 响应与已有窗口恢复。依赖中确有 30 秒 RPC/
  读超时，多个串行等待有可能接近一分钟，但**没有现场栈就不能把“可能”说成根因**。

不要直接打开 tuios 的原始键盘事件日志：它可能记录输入正文。该诊断只用安全阶段日志。

## 独立的终端恢复缺陷

网络/开源研究发现 Bubble Tea v2 的已知早期错误路径可能在进入 raw mode 后返回，而未恢复
termios。通过仅降低一次性测试子进程的文件描述符软上限，真实 PTY 回归复现了这个问题。
Golder 现增加借用终端的状态快照/恢复兜底，应用于独立 TUI 和可见 workspace；不另读 stdin，
不关闭借用文件，不额外拦截信号，也不向未取得协议所有权的父终端盲发 reset。

这个兜底只恢复内核 TTY 属性；**不能保证鼠标/增强键盘 reset 穿过已经断开的 SSH**，也不能
捕获 SIGKILL。Esc 分段误触发退出的问题仍独立保留证据，没有借此次渲染优化偷改退出键语义。

参考：
- [Kitty 键盘协议：Esc 歧义、增强与状态恢复](https://sw.kovidgoyal.net/kitty/keyboard-protocol/)
- [tmux 官方 FAQ：escape-time 与慢网络](https://github.com/tmux/tmux/wiki/FAQ)
- [Ghostty SSH 断开后鼠标模式残留讨论](https://github.com/ghostty-org/ghostty/discussions/12359)
- [Bubble Tea #1850：启动错误后未恢复 raw mode](https://github.com/charmbracelet/bubbletea/issues/1850)
- [Bubble Tea #1849：关闭期间第二个信号跳过恢复](https://github.com/charmbracelet/bubbletea/issues/1849)

## 当前集成/验证边界

测量阶段通过私有开发 workspace 构建 `.build/golder-linux-ssh-lite`，执行上面的真实 SSH
对照。实现现已发布为 tuios-mini **v0.10.17**，Golder 的公开 go.mod 已改为该 tag；kitty-browse
没有新改动，继续使用其最新 tag **v0.2.3**。不提交任何本地 replace 或 go.work 路径。

依赖的完整 root-module race 套件已在 941 秒内通过；此前 4/6 分钟预算不足以完成穷举
设置面板与 fuzz 测试。最终文件/侧栏/重命名/SSH/workspace/重新 attach 定向 race 也通过，
额外确认 ASCII 装饰不会删除中文文件名或用户在命名框输入的文字。构建、vet、tidy、格式和
Linux arm64 交叉构建通过。完整 Linux 容器预检仍暂停；已授权的定向 Linux 原生 SHM/PTY/
SSH 测试不是全套 Linux 验证。

Golder 也已对公开依赖 tag 使用 `GOWORK=off` 完成完整 race（含输入依赖 fork，51 秒）、
build/vet/tidy/格式，以及 Linux arm64、Windows amd64 交叉构建。Python getpass 兼容探针
使用系统 Python 的干净 PATH；没有为本机 pyenv 的私有 dylib 放宽被测沙箱。

同时下载并复现了 2026-10-28 的两条失败 CI：三个 fuzz 样本、SSH 图形洪泛测试的共享内存
名称冲突/分段标记、workspace 初始化就绪判断，以及本地全套暴露的旧 push 被误当 attach
回复等问题。修复与负对照见依赖的
[CI 取证报告](https://github.com/getan/tuios-mini/blob/v0.10.17/docs/CI_2026_10_28.md)。
原失败 run 保留供对照，修复提交已重新触发 Fuzz 与包含 race 的 Go unit tests。
