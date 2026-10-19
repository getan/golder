# 沙箱与容器化

> 对标 pi 的 [containerization.md](https://github.com/earendil-works/pi-mono/blob/main/packages/coding-agent/docs/containerization.md)（issue #571）。中文文档，示例均可复制执行。

## 默认立场（与 pi 相同）

golder **内置 OS 沙箱并默认开启**：获准执行的 `bash` 默认在沙箱内跑（可完整枚举且不碰凭据的调用可能裸跑，见"信封"），原生文件工具走一次性沙箱 worker。沙箱是**白名单**：读限项目 + 系统运行时 + 显式授权的读根，写限项目 + `TMPDIR` + 显式授权的写根，网络默认关闭、按次授权（见下文"执行隔离"）。

沙箱之外还有一层**准入控制**（trust 三态、四档权限模式、`--approve`、`--allowed-tools`/`--disallowed-tools`）：它约束"模型是否被允许发起某个工具调用"，与操作系统隔离是两件事。只有 `GOLDER_SANDBOX=off`、或平台从未实现沙箱（Windows）时才裸跑；需要比 OS 沙箱更强的边界（整机隔离、容器）时，再把 golder 容器化。

## 三层防线的分工

| 防线 | 约束对象 | 防什么 | 不防什么 |
|------|---------|--------|---------|
| 项目信任（trust 三态）+ `--approve` | 模型发起的副作用工具调用 | 未信任目录下的静默副作用 | 已信任目录内的任何工具调用（`--approve` 放行全部） |
| `--allowed-tools` / `--disallowed-tools` | 模型可见/可调用的工具集合 | 误用与越权调用（黑名单优先、子 Agent 继承、`--approve` 不可绕过） | 工具内部的系统调用（`bash` 仍能做任何事） |
| 容器 / 沙箱边界 | 整个 golder 进程 | 文件逃逸、网络外联、资源滥用 | 模型层面的误判（边界内仍可为害） |

三层是**叠加**关系，不是替代：容器内仍应配合 `--disallowed-tools bash`（如果该会话不需要 shell），`--approve` 只应授予容器内进程。

## 进程内第四层：权限模式 + LLM 审查 + 平台沙箱（sandbox-exec / bubblewrap）

> 对标 Codex 的 `/permissions` 预设与 guardian/auto-review：审批档位是显式的四档，`auto` 档由**当前会话的模型**审查每次调用并给出理由——不引入额外的分类服务或密钥，是"用正在用的 LLM"而不是再配一个模型。

调用链是 `trust → permission gate → hooks`：trust 只认目录（信了不代表全放行），权限门按当前模式处理每次改动型调用，hooks 维持用户自定义。deny 赢，前面的 block 会短路后面的环节。

### 四档模式

| 模式 | 行为 | REPL（可交互） | TUI/headless（无 stdin） |
|------|------|----------------|--------------------------|
| `read-only` | 只读；改动型工具直接拒绝 | 拒绝 `bash`/`apply_patch`，只放行 read/图片/搜索/待办等 | 同左 |
| `ask` | 低风险放行，其余询问 | 弹 `[y/N]`；可隔离的 Contain 档在有 runner 时直接进沙箱 | 无输入可问 → 拒绝（带理由卡片） |
| `auto`（默认） | 模型审查后自动决定 | Confirm 放行、Contain 进沙箱、Deny 拒绝，各附一行理由 | 同左 |
| `full-access` | 不审查、不套沙箱 | 直放；仅静态硬拒名单生效 | 同左 |

切换方式：会话内 `/permissions`（TUI 是 ↑↓ + Enter 选择器，带每档说明），启动参数 `--permissions <mode>`，环境变量 `GOLDER_PERMISSIONS`（`GOLDER_JUDGE=off` 作为旧的逃生舱别名等价于 `full-access`）。模式是进程内实时状态：切换后**下一次工具调用**立刻生效，子 Agent 继承父会话的模式（进程隔离子 Agent 通过环境变量继承）。

### 批准之后的信封（envelope）

四档模式回答"准不准跑"，信封回答"在哪跑"；两者正交。一个已批准的 bash 调用，只有在**整条命令的效果都能从文本读出**时才不套文件沙箱（`Bare`），否则默认收容：

- `shellfacts` 解析失败、含 `for`/`if` 等构造、出现 `$VAR`/`$(...)`/`$((...))`/进程替换、赋值改变程序或加载（`PATH=/tmp/bin`、`LD_PRELOAD=...`、`HOME=...`）→ 收容；`GOCACHE=`/`TMPDIR=` 这类缓存/临时目录不改变执行，仍可枚举；
- 程序本身是运行文本或包装器（`bash`/`sh`/`python`/`node`/`make`/`docker`/`env`/`xargs`/`sudo` 等）→ 收容；
- 命令行里出现凭据路径（`~/.ssh` 等）→ 收容（凭据永远不可授权）；
- 其余（`git push`、`go test`、`ls`、`cargo build` 等）→ 不套沙箱，直接在宿主上跑。

`GOLDER_SANDBOX=enforce` 仍然把**每一条**都收进沙箱，优先于信封；`full-access` 不变；`ask`/`auto` 的信封相同。每次 bash 结果和 worker 执行的原生文件工具（`read`/`grep`/`find`/`ls`/`view_image`/`apply_patch`）的结果都带 `details.envelope`（`sandbox`/`bare`）：TUI 卡片标题带 `·sandbox`/`·unconfined`，REPL 结果行后缀同义。原生文件工具由 `toolworker` 的一次性沙箱执行；`full-access`、Direct 授权或没有 runner 时退回进程内，标 `bare`。`todo`/`websearch`/`webfetch`/`write_stdin` 等不走 OS 沙箱的工具没有这个标记。信封只改变**已批准**调用的执行位置，不产生批准；显式 `sandbox_permissions=require_escalated` 的 Direct 流程保持不变。

网络是另一个维度：Bare 的命令跑在宿主上，网络是它的自然能力，所以 Bare 的 Allow 和自动批准的 Confirm 不再单独问网络（记住的网络拒绝规则仍优先）。`/network follow` **默认开**，把 auto 模式下**评审通过的调用**（allow / confirm / contain，含 contained）的网络一起带上——那次调用本身已经过审查，不再问第二遍；`/network follow off` 恢复每次单独问。`/network add host|command|tool <key>` 可以预授权（`ask` 档或想不经评审时仍有用），`/network rm` 删除。信封不是网络白名单，也不是持久授权；`ask` 里需要你拍板的 Confirm 仍会先问命令，再问网络。

### `auto` 的审查器（就是会话模型）

分级链（`internal/judge`，叶包）：

1. **StaticFloor**：不可逆硬拒（`sudo`、`rm -rf /`、`mkfs`、写 `~/.ssh`/`~/.gnupg`/`trust.json`、整环境导出 piped 到网络、敏感变量 echo 进网络等），只拒不放，其余全交模型。单半边合法（`echo $PATH` 调试、`curl` 正常下载、`env FOO=bar ./cmd` 传参）放行给审查器。它也是**唯一不经过模型就拒绝**的一层——正因如此，凭据读这类结论既不能被重试，也不能被提示词说服。
2. **决策模型（jev）→ 会话模型（LLM）**：每个调用都审，没有豁免。链路是 `reviewpolicy.Classifier` 包住的：先问 TypeSafe 的 System One（`internal/jev`，约 0.78s/次，实测固定线路下 p90 808ms），confident 就直接判，否则升级给会话模型的 `LLMJudge`（严格 JSON：`{level: allow|confirm|contain|deny, risk, authorization, rationale}`，`rationale` 用对话语言书写并直接显示给用户）。jev 按两个规则路由：**能决定，但不能便宜地决定**（Allow 要高置信 + 大差距 + 所有危险信号干净；Deny/Contain 中等置信即接受，因为它们的失败模式只是多余的拦截或收容；Confirm 一律升级）。没有 TypeSafe 凭据时，这一层换成**本地规则表** `judge.Local`（同样每个调用都判，判不了的档上交会话模型），jev 整条路径不接。
3. **兜底**：审查超时（默认 30s，`GOLDER_REVIEW_TIMEOUT_MS` 可调）、网络错误、JSON 不可解析一律标记为失败，绝不故障放行——`auto` 下能隔离的（`bash`）进沙箱、不能隔离的拒绝；`ask` 下弹窗问人；无 prompt 的驱动直接拒绝。审查器缺失（理论上不会发生）同样按失败处理。

**曾经有两条豁免，都已删除**（记录于此，因为它们的设计理由仍然成立，只是不再换来那点延迟）：

- **只读快路径**：能*证明*只读的命令行直接放行。判定用的是封闭白名单 + 严格扫描器（重定向、命令替换、赋值、`env`/`nohup`/`time` 包装、变量、命令位置的路径调用一律拒绝；每个程序按名字排除能写能执行的开关：`rg --pre`、`find -exec/-delete`、`sed` 非 `-n Np`、`git --output/--textconv` 等）。删除它的理由不是它不安全，而是**它买的东西不值它的复杂度**：决策模型把一次审查压到约 0.78s，而白名单为了自证清白需要 kill switch、内部再跑一遍凭据底线、以及升级请求的例外——三样都是"豁免"这个身份带来的。现在整条链只剩一条规则：**每个调用都审**。
- **日常编辑快路径**：`apply_patch` 的目标全在工作区内时直接放行（实测 586 个真实补丁里 584 个走这条）。它的两个"例外"其实是**关于编辑的事实**而不是豁免的一部分：`.git/**` 是植入执行（hook 在下一次 git 命令时运行，而 git 命令正是只读快路径天天放行的那一类；`.git/config` 是同一扇门：`core.hooksPath`、`core.sshCommand`、credential helper——已实测：往临时仓库写一个 hook，下一次 `git commit` 就执行了它），`AGENTS.md` 是持久的提示注入通道（工作目录的这一份会被读进**之后每个会话**的系统提示词，改它等于改写 agent 的起始指令）。这两条事实没有随快路径消失：它们现在作为 `State.Outliving` 送给审查者（`judge.OutlivingPaths`），**报告而不拒绝**——请求写一个 pre-commit hook 是合理的需求，旧代码也同意（这些路径只是保留审查，从没被拒），硬拒会把合理请求一起挡掉。

**沙箱现在是默认执行环境，与命令内容无关**：获准执行的每条 `bash` 调用都进沙箱，不论它是 `ls` 还是 `go test`；原生文件工具（`read`/`grep`/`find`/`ls`/`view_image`/`apply_patch`）改由**一次性 worker** 在沙箱内执行。原先那套"手写 shell 解析器 + 白名单"判断命令是否只读、只读才收容的机制**已整体删除**（`internal/judge/readonly.go` 与它的交叉表），因为路由不再依赖命令内容：`cksum /outside/x` 这类漏判不再需要"只读识别"兜底，沙箱本来就是地板。

只读快路径**不再喂给 TUI**：`bash` 卡片一律保留自己的卡片并显示完整命令，不进 `• Explored` 单元格。折叠需要一行摘要，而摘要必须在复合命令里挑一段来命名——实测 20% 挑中的是 `echo "==="` 这类 banner 回声，真正干活的命令反而被藏了。卡片直接显示命令就不可能说错跑了什么。原生读工具（`read`/`ls`/`grep`/`find`/`view_image`）保持折叠：它们的参数就是整个调用，`Read a.go, b.go` 是忠实的概括。写在折叠单元格里的裁决（拒绝、收容）仍然可见，展开则逐条还原。

新增依赖为零：审查用的就是会话模型，`/model` 切换后审查器随之切换，不需要 `TYPESAFE_API_KEY`。

### 展示（codex 风格审批卡片）

审查结果以一行卡片显示，中文/英文随对话语言：`auto` 通过 `⚠ 自动审批通过（bash，风险：中，授权：高）：<理由>`，隔离运行 `⚠ …将在沙箱内运行…`，拒绝 `✗ 自动审批拒绝（…）：<理由>`，审查失败 `⚠ 审查不可用，已按保守策略处理…`；`read-only` 阻止显示 `✗ 只读模式：已阻止 bash`。TUI 里卡片插在该调用卡片正上方（与 Codex 的 review 行 + 调用行顺序一致），REPL 直接打印。`ask` 弹窗本身已展示理由，因此不再重复卡片。

### 执行隔离

**隔离是默认，不再由评级决定**：门在放行前会问 `run.SandboxGate()`——它覆盖 `bash` 与那几个原生文件工具，并与 `WireBashSandbox`/worker 的装配条件一致（`GOLDER_SANDBOX` 非 off、平台有可用 runner；`full-access` 照挂 runner，只在执行时绕过它）。`SandboxGateMatchesWiring` 测试把这条一致性钉死。只有平台**从未实现过**沙箱（Windows）才保持裸跑；Linux 上 bubblewrap 被禁则失败关闭，而不是悄悄降级。

执行隔离（`internal/seatbelt`）：CLI 与工具参数都不变，语句从 `bash -c` 换成平台沙箱；原生文件工具走一次性 worker，用同一套策略。两个平台同一条策略——**读白名单、写白名单、网络默认关闭**：

- **macOS**：`sandbox-exec -f <现场生成的 profile> bash -c …`。profile 按规范 vnode 路径匹配，而 `/var` 是 `/private/var` 的软链，因此项目与临时目录同时写入**符号链接形式与 `EvalSymlinks` 后的规范路径**（只写软链形式会导致该允许的写操作被静默拒绝）。
- **Linux**：`bwrap --die-with-parent --unshare-pid --unshare-uts --unshare-ipc --unshare-net --ro-bind <各白名单路径> --dev /dev --proc /proc --bind <项目> <项目> --bind-try <TMPDIR> <TMPDIR> … bash -c …`。`--dev` 会挂载新的 devtmpfs（含 devpts），所以 `tty=true` 的交互式会话照常工作。bwrap 是否可用在执行前**实探**一次（能否真的创建 userns/挂载；被 AppArmor 或内核开关挡掉时视为不可用，回退为询问/fail-closed，而不是命令跑到一半才报错）。

### 读白名单（home 默认不可读）

`ReadableRoots()` 给出沙箱内**可读**的路径，正是这些：系统运行时（`/usr`、`/bin`、`/sbin`、`/lib`、`/lib64`、`/etc`、`/opt`，macOS 另加 `/System`、`/Library`、`/Applications`、`/private/var/db`、`/private/etc`、`/private/tmp`、`/private/var/tmp`）加上项目目录、沙箱临时目录，以及**两个 git 配置文件**（`~/.gitconfig`、`~/.config/git/{ignore,attributes}`）。

**其余一切不可读**——`~/.zshrc`、`~/.ssh`、`~/.gnupg`、`~/.aws`、`~/.netrc`、`~/.git-credentials`、`~/.config/*`、`~/.golder/*`、命令历史、macOS `~/Library/Keychains` 全部在内。这是"宁可错杀"的取舍：新冒出来的凭据位置自动被挡，不需要维护黑名单。实测（macOS 真实 `sandbox-exec`）：项目内读写、`git status`、`git commit`（需要身份）、读 `/etc/hosts` 全部正常；`cat ~/.zshrc`、`ls ~/.config`、读写 home、联网全部被拒。

两个容易踩的细节：**根目录 `/` 本身必须可读**（只给 `subpath` 规则不够，dyld 解析绝对路径时会因根目录不可读而 abort，进程在 `main` 之前就死掉）；**`/.gitconfig` 里可能藏 token**（`url.*.insteadOf`、`http.extraheader`、credential helper）——如果你把密钥写在那儿，就把 git 文件从白名单拿掉，改用 `GIT_AUTHOR_*` 环境变量或提权执行提交。

工具链不在上述路径时（conda / pyenv / sdkman / nvm 都在 `$HOME`，或你的缓存盘如 `/Volumes/XXX`），用 `GOLDER_SANDBOX_READABLE` 追加（冒号或逗号分隔的绝对路径）：

```bash
export GOLDER_SANDBOX_READABLE=/Volumes/KIOXIA:$HOME/miniconda3
```

注意它只放宽**读**；写默认只限项目与 `TMPDIR`，编译产物写到别处（如 `CARGO_TARGET_DIR=/Volumes/…`）用下面的 writable_roots 授权，或提权执行。

### 工具侧读边界（工作区外读取需确认）

沙箱管的是 bash 命令；读取类工具（`read` / `grep` / `find` / `ls` / `view_image`）另有一条边界：**工作区根目录**（启动目录）。这几个工具默认只读工作区，越界不是硬拒绝，而是征求你的同意——这是范围/同意问题而非风险问题，因此**与权限模式无关**：只读、每次询问、自动审批、完全放行都会问（`full-access` 关掉的是审查与沙箱，不是知情同意）。没有可问的人时失败关闭，错误消息里写明路径与可用的配置办法：`headless` 与进程隔离的子 agent 无人可问；TUI/REPL 里的子 agent 则把问题送进同一个弹窗，标题标出来源（`bash (from task)`），因为人就在旁边看着这一批任务。

两种回答粒度不同：

- **只读这一次**：随该次调用生效（上下文里的单次授权），不留记录；
- **本会话允许读该目录**：作为本会话行记进统一授权表（`grants.json`，进程内即刻生效），此后该目录下的读取不再询问，并**同时放宽沙箱读白名单**——否则会出现"工具读得到、bash 读不到"的分裂。

候选目录取最细粒度：调用命名的路径本身就是目录时（`ls`/`grep`/`find` 的操作数、bash 命令里的目录参数、带尾斜杠的写法）取该目录本身，是文件时取「文件所在目录」，不会上提到更粗的一层；一次调用点名多个路径时（`ls ~/work ~/work/proj`、`grep` 的 `path` 数组）取其中最细的那个目录，先写的路径只在并列时胜出；通配符操作数（`ls ~/work/*/*.go`）取模式之上**实际存在**的最深目录。只有该目录太宽（`$HOME`、`/`、`$HOME` 的父目录）时才退回到只提供"只读这一次"。**凭据路径（`~/.ssh`、`~/.zshrc`、`~/.aws`、`~/Library/Keychains` 等）不进入询问**：静态硬拒绝层直接拦下，弹窗不会给出一个按键就交出它们的机会。

命令里的操作数按**shell 的实际语义**读：前导 `~` 与 `$HOME` 都会被展开（`~/x`、`~/"x"`、`$HOME/x` 都算家目录下的路径，而引号包住波浪号的 `"~/x"` 按字面算——与 bash 一致；别人的 `~name` 不展开）。不这样做的话，`ls ~/Desktop` 会被当成工作区内的相对路径，家目录下的读取反而**不会**被询问，而同样位置的绝对路径会。

需要预先授权（避免反复弹窗）时有两条途径，汇入同一张授权表：

- **批准读授权时选「记住」**：写入 `~/.golder/grants.json`，重启后仍生效（`/boundary readable list|add|rm` 查看与维护）；
- **`GOLDER_READABLE_ROOTS`**：冒号/逗号分隔，规则一致（`~/` 展开，相对路径与过宽根被丢弃），给无法编辑文件的机器用。

它与 `GOLDER_SANDBOX_READABLE` 不是一回事：后者只作用于 OS 沙箱，前者是工具自己的白名单并顺带放宽沙箱。**读授权不影响写**：`apply_patch` 越界问的就是写；shell 里带重定向（`echo hi > /outside/f`）或写动词（`rm`、`mv`、`cp`、`mkdir`、`chmod`…）的操作数，现在也按**写**来问——以前它们要么被当成"读"、要么根本不被抽取，于是一个读授权可以给写放行，而裸跑（信封）的命令更是没有沙箱兜底。读授权只覆盖读取；会话读授权覆盖**弹窗给出的那个目录及其子目录**，所以批准后该目录下后续读取不再询问（这正是"会话读"的含义）。

### 写白名单（项目 + 临时目录 + writable_roots）

沙箱内默认可写的只有两处：项目目录与 `TMPDIR`。需要写别处（编译缓存盘、`CARGO_TARGET_DIR=/Volumes/…`、`~/go/pkg/mod` 之类）时，有两条授权途径，它们汇入同一张授权表（`grants.json`；本会话行在进程内即刻生效，并发安全，下一个命令就看得见）：

- **审批对话框**（四条相关路径；路径授权只影响**那一个路径**，批准不是"无沙箱放行"——命令始终收容执行）：
  - **重准入**：越界申请被评审拒绝、或命令被沙箱拒绝时，弹窗把被拒路径拿给你选：`a` 只读/本会话、`s` 只读/记住；**只有命令本身需要写**时才额外给出 `w` 写/本会话（读写共用一个授权，标签写明"reading included"）。题面写明被拒的是读还是写（`denied writing` / `denied reading`），**需要写时写行在首行**；批准后命令**收容重跑**，同一路径每次运行只问一次。
  - **请求本次不隔离运行**（命令被沙箱拒绝时才有）：`e`（REPL 里同键）**发起一次越界申请**，再由 permission gate 决定——`ask` 模式会再问一次很窄的确认，`auto` 模式交给 reviewer 重新看这个"在沙箱外跑"的请求；批准后才在沙箱外重跑一次。它只在没有任何路径授权能解决问题时才是答案——例如 macOS 用 `Operation not permitted` 报出的 SIP 保护文件（`/private/var/…`），或静态底线拒绝交出的一类凭据文件（`~/.ssh/known_hosts`）——因为这些路径不会被登记为可授权项。该决定只对这一次生效（不登记、不记忆），并在转写里作为"沙箱外运行"播报。read-only 模式下不提供该选项。
  - **写确认**（`apply_patch` 写工作区外）：`y` 仅本次、`a` 写/本会话、`s` 写/记住。
  - **越界申请的写行**（ask 模式的 escalation 弹窗）：`w` 该路径可读写/本会话、`s` 记住。
- **`/boundary writable add <path>`**：写入 `~/.golder/grants.json`（原子写），重启后仍生效；`rm` 撤销会话授权并删除持久化记录。`/boundary writable` 列出全部条目及来源（`grants.json` / 仅本会话），加 `warning:` 行报告读不出来的坏条目。TUI 里输入 `/boundary writable`，或从 `/permissions` 菜单最后一行 `Sandbox writable paths…`，打开同一列表（↑↓ + Enter 删除，`Add path…` 把命令填回输入框）。

可写路径**同时可读**（写规则与读白名单一起追加）。路径不要求预先存在（新编译缓存是常见场景），但必须是绝对路径或 `~/` 开头，相对路径直接拒绝并在启动时报警告。

### 网络与 golder 自身状态

**网络默认关闭，且按次授权**：沙箱内 `network*` 默认 deny / `--unshare-net`。需要联网的调用由模型在 `bash` 参数里声明 `network:"on`（例如 `git push`、包下载），授权门在**同一次**审批里决定是否授予这一次的网络能力——授予后仍留在文件沙箱内，不需要"脱离沙箱提权"。auto 档下被评审为 Allow 的调用直接携带网络（`/network follow`，默认开；`off` 恢复逐次询问），`ask` 与 Confirm 仍按次；评估器无法给出明确网络答案时问人（`ask`/TUI 弹窗），无人可问则失败关闭。环境变量 `GOLDER_SANDBOX_NETWORK` 已移除（不再有全局开网开关）。

**Bare 的例外**：已批准的 Bare 命令跑在宿主上，网络是自然能力；Bare 的 Allow 和自动批准的 Confirm 不再单独问网络（记住的网络拒绝规则仍优先），模型也不必再声明 `network:"on"`。contained 调用同样随 auto 的 Allow 一起获得网络（`/network follow`，默认开）；`/network follow off` 恢复逐次询问，`/network add host|command|tool <key>` 预授权，`/network rm` 删除。

**`/network` 的三种用法**：`/network add host|command|tool <key>` 预先记住"允许"（host 可带工具名）；`/network rm` 删除；`/network follow [on|off]` 切换"auto 的 Allow 直接携带网络"（**默认开**，保存在 `network.toml`；`off` 是明确选择"每次单独问"；记住的拒绝仍优先，`ask` 不变）。复合命令的 command key 只有在这种形状才成立：每个部分要么是同一个可记住的程序，要么是非联网程序（`cd … && GOCACHE=… go test … | tail` → `go test`；`git push && curl evil` 不记）。

**golder 自身状态始终拒读写**：`ProtectedGolderPaths()` 返回**整个状态根**（`~/.golder`，或 `$GOLDER_HOME`）。它平时就不在白名单里，但若项目根恰好在状态根内，项目绑定会连它一起覆盖——所以在可写绑定**之后**再显式拒一次（macOS 用 deny 规则，Linux 用空 tmpfs 覆盖，后挂载生效）。拒的是目录而不是文件清单：清单式的旧写法已经落后过一次——`grants.json`（决定 agent 能读写什么的那份文件）当时不在清单里，而 `trust.json` 在。按目录拒之后，以后往状态根里新增任何文件都自动受保护。

**沙箱自动提权申请（对齐 codex 的 require_escalated）**：模型撞到沙箱拒绝（输出含 `operation not permitted` / `permission denied` / `read-only file system` / `sandbox` 等特征词）时，bash 结果里会附一行提示，告知可用 `sandbox_permissions: "require_escalated"` + `justification` 重试。权限路由的优先级是固定的：**enforce 模式与权限门要求的隔离永远优先于提权请求**（门在调用参数里能看到提权请求，判分时已把理由纳入考量）——提权只在本地 judge 路由这一层生效，并且审查器（LLM）对命令本身的判定依然照走，所以"申请提权"不等于绕过审查。

当模型确实申请了提权、而审查器给的却是 **Contain** 档（该调用本应被收容）时，由**模式**决定谁裁决：

- **auto**：评审的话作数。若请求点名了路径，先把"要不要把该路径放回沙箱"问你——`a` 只读/本会话、`s` 只读/记住、`w` 读+写/本会话、Esc 拒绝；题面写明被拒的是读还是写（`denied writing` / `denied reading`），需要写时写行就在首行。批准后命令**收容重跑**（同一路径每次运行只问一次）；**没有可提的路径**时则静默重新收容并在转写里播报"越界请求被拒，改为在沙箱内运行"（越界是能力问题，不是风险问题）。
- **ask**：把整个越界交给你（TUI 弹审批对话框，REPL 走 stdin 提示），默认项就是"收容重跑"，`e` 才是故意出沙箱——弹窗里还会多出 `Allow reads and writes to <path> (this session)`（按 `w`）等写行，把那个路径放进沙箱，比出沙箱更省事。

**命令被沙箱拒绝后的处理**：先按上述重准入问答一次（`a` / `s` / `w` / Esc）；当被拒路径无法通过授权解决时，还可以用 `e` 让这条命令在沙箱外跑一次。拒绝或无人可问时，命令的失败会带上明确说明（是凭据材料、还是被系统保护），提示改用一次不隔离运行或换一个可读的副本，而不是让你去授权一个批准了也没用的路径。

评审明确给出 **Direct** 授权时才会真的在沙箱外跑，且无论批否都会播报。没有交互界面时按最安全的默认收容或失败关闭；bash 的结果里会给模型贴标签防死循环（auto 下还会点明"重试无用，要出沙箱得让用户切 `/permissions ask`"）。

**命令没有申请越界、却被沙箱拒绝**时同理（模型只是照常跑命令）：只要命令或理由里点得出路径，就当场弹同一个重准入窗，批准后原命令在沙箱内**自动重跑**；拒绝、或重跑后仍失败，则维持原结果与给模型的提示。

### 用户直通命令（`!command`）

输入框里以 `!` 开头的一行是**用户自己的** shell 直通，对齐 codex 的 `user_shell_command`：不走模型、不看权限档位、不进沙箱，用登录 shell（`$SHELL -lc`）在项目目录执行。它在 TUI 与 REPL 都可用，且运行中也能随时输入——TUI 开一张 `• Running …` 卡片流式显示输出，结束后变 `• You ran …`；截断到约 30 KB 的结果以 user 角色的 `<user_shell_command>` 记录（命令、退出码、耗时、输出）进入上下文，模型因此知道用户跑过什么。这是用户显式操作而不是模型发起的调用，所以权限门与审查器都不经过它；↑ 呼回的是原始 `!cmd` 行，可回车重跑，单独的 `!` 只显示用法提示。Ctrl+C 中断正在跑的直通命令；Ctrl+Z 则在任何状态把 golder 挂起交还给终端作业控制（`fg` 回前台）。

### TUI 审批对话框

TUI 没有 stdin，REPL 的 `[y/N]` 提示无法使用，因此需要用户拍板的调用在 TUI 里统一走一个**审批对话框**（`internal/cli/tui/approval.go`），形态对齐 codex：渲染在输入框正上方（与斜杠菜单同槽位，不遮上下文），单键作答。

```
bash needs approval
  $ npm install
  Reason: needs the network for package installs
  Reviewer: installs dependencies (medium, auth: high)
› Approve once                      (y)
  Approve for this session          (p)
  Allow writes to ~/cache/build     (w)   ← 仅当沙箱拒绝提到项目外路径时出现
  Deny                              (esc)
```

覆盖的场景：

| 场景 | 触发 | 说明 |
|---|---|---|
| 权限门 Confirm 档 | `ask` 模式下的中等风险调用 | auto 模式不弹（模型自行批准） |
| Contain 档但无法隔离 | 无 runner / 非 bash 工具 | 对话框明确标注"批准将以非沙箱方式运行" |
| 审查失败且无处隔离 | LLM 审查超时/报错 | 对话框标注"自动审查不可用" |
| **未信任目录的副作用工具** | 每次 bash/apply_patch | 此前 TUI 完全没装这道门；现在是"批准一次 / 本会话信任此目录 / 拒绝" |
| **首启信任** | 目录未决时启动 | 同一个对话框的 trust 形态：`s` 记住、`p` 本会话、`n` 记住不信任、Esc 取消（不落盘）。运行中的信任问句另一套键：`y` 仅这一次、`a` 本会话允许本目录的读取、`p` 本会话允许全部工具——运行中这一问**不落盘**（持久化是启动选择器的职责） |

行为约定：对话框是**模态**的（打开期间所有按键归它，输入框收不到字符）；同一时刻只允许一个待审批（第二个请求 fail-closed）；run 被中断时对话框随之关闭，未作答一律按 fail-closed 处理（沉默不等于批准）。远程控制连接时，信任确认优先路由到浏览器（原有行为）。

这套规则有真实的端到端测试覆盖：macOS 用 `sandbox-exec`、Linux 用 `bwrap`（写项目内成功、写项目外失败、可写树内的 `trust.json` 仍被拒、devpts 存在）。Linux 上运行需要安装 bubblewrap（Debian/Ubuntu：`apt install bubblewrap`；Fedora：`dnf install bubblewrap`；Arch：`pacman -S bubblewrap`），且允许非特权 user namespace；未安装时 `GOLDER_SANDBOX=auto` 走回退路径（确认/拒绝），`enforce` 则 fail-closed。

### 在 macOS 上验证 Linux 沙箱（Apple container）

用 Apple 官方 `container` 跑 arm64 Linux 即可本地验证 bwrap 路径。一次性构建内置 bubblewrap 的镜像（国内网络可把 `golang` 换成本地镜像源）：

```bash
mkdir -p /tmp/golder-linuxtest && cat > /tmp/golder-linuxtest/Dockerfile <<'EOF'
FROM golang:1.27-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends bubblewrap \
 && rm -rf /var/lib/apt/lists/*
EOF
container build -t golder-linuxtest:1.27 -f /tmp/golder-linuxtest/Dockerfile /tmp/golder-linuxtest
```

之后每次验证（镜像已内置 bubblewrap，无需再装）：

```bash
container run --rm --cap-add ALL -m 6G -v "$PWD":/src -w /src golder-linuxtest:1.27 \
  bash -c 'export PATH=$PATH:/usr/local/go/bin; go test -count=1 -v -run Bwrap ./internal/seatbelt/'
```

> `--cap-add ALL` 是必须的：Apple 容器默认给 root 的 capability 不含 `CAP_SYS_ADMIN`，bwrap 建不了 mount namespace（报 `Creating new namespace failed`）；默认权限下探测会把沙箱判为不可用并优雅跳过，这是预期行为，不是 bug。

```bash
export GOLDER_PERMISSIONS=auto  # read-only | ask | auto（默认）| full-access
export GOLDER_SANDBOX=auto      # off | auto（默认，仅 contain 档隔离）| enforce（全部 bash 进沙箱，无 runner 则 fail-closed）
export GOLDER_REVIEW_TIMEOUT_MS=30000  # 审查超时（3s–120s）
# GOLDER_JUDGE=off 为兼容保留：等价于默认 full-access
```

灰度建议：先用默认 `auto` 跑一周看卡片理由是否合意；太吵或太慢就 `/permissions ask`（改回人肉确认）或 `read-only`；确认模型判分可靠后再按需 `enforce` 沙箱。

---

## 模式一：Plain Docker（整进程隔离）

最简单、最常用。整个 golder 进程跑在容器里，工作区通过挂载进入，密钥通过环境注入且不落盘。

### 构建镜像

```dockerfile
# Dockerfile
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY . .
RUN go build -o /out/golder ./cmd/golder

FROM alpine:3.20
RUN adduser -D -u 10001 agent
COPY --from=build /out/golder /usr/local/bin/golder
USER agent
WORKDIR /workspace
ENTRYPOINT ["golder"]
```

```bash
docker build -t golder-agent .
```

### 运行（只读挂载 + 禁外联 + 非 root）

```bash
# 密钥放临时 env 文件（用完即删，不要提交进仓库）
printf 'OPENROUTER_API_KEY=sk-or-real\n' > /tmp/golder.env && chmod 600 /tmp/golder.env

docker run --rm -i \
  --user 10001:10001 \
  --read-only \
  --tmpfs /tmp:size=64m \
  --network none \
  --env-file /tmp/golder.env \
  --mount type=bind,source="$PWD",target=/workspace,readonly \
  golder-agent -p "阅读 /workspace 并总结架构" \
  ; rm -f /tmp/golder.env
```

要点：

- `--read-only` + `--tmpfs /tmp`：容器根文件系统不可写，模型只能通过显式挂载点写文件；`bash`/`apply_patch` 的副作用被限制在 `tmpfs`（会话结束即消失）。
- `--network none`：彻底禁止模型外联（容器内连 Provider 都不能访问——适合"离线审阅"类任务）。若任务本身需要调用 Provider，去掉 `--network none` 并改用出网白名单（见 compose 示例）。
- `--mount ... ,readonly`：工作区只读挂载，物理上禁止改写源码。

### 允许写工作区 + 只放行 Provider 出网（docker-compose）

```yaml
# docker-compose.yml
services:
  golder:
    build: .
    user: "10001:10001"
    read_only: true
    tmpfs:
      - /tmp:size=128m
    volumes:
      - ./:/workspace:rw          # 允许模型改工作区
    env_file:
      - .env.golder                 # chmod 600，并加入 .gitignore
    networks: [golder-net]
networks:
  golder-net:
    driver: bridge
    # 出网范围自行收紧到 Provider 域名（配合防火墙/eBPF 策略更佳）
```

```bash
docker compose run --rm golder -p -a "修复失败的测试"   # -a 只授予容器内目录信任
```

---

## 模式二：micro-VM（Gondolin 模式，高级）

pi 的 Gondolin 扩展把**工具执行**路由进一台本地 Linux micro-VM（Firecracker/qemu），agent 与 Provider 鉴权留在宿主。golder 的等价做法（简化形态）：

1. 宿主准备一个最小 VM/容器镜像（含 golder 二进制 + 工作区快照）；
2. 宿主进程只负责编排：把任务 prompt 通过 `golder -p --output-format stream-json` 送进 VM 内的 golder；
3. Provider 鉴权由宿主代理转发（VM 内不持有真实 key），或 VM 内注入一次性 env。

```bash
# 以 qemu/firecracker 之外的轻量近似为例：在一次性 VM 的 ssh 会话里跑
ssh vm0 "GOLDER_HOME=/tmp/golder-home OPENROUTER_API_KEY=\$FORWARDED_KEY \
  golder -p '阅读 /workspace，列出所有 TODO' --output-format stream-json" \
  > run-$(date +%s).jsonl
```

> 这是"agent 在宿主、副作用在 VM"的近似：主 agent 进程持有 key，VM 只接收任务与产出产物。pi 的 Gondolin 扩展实现了更严格的"逐工具路由"形态，可参照其设计移植。

适用场景：多租户/不可信仓库分析、需要强文件系统与网络隔离的长任务。

---

## 模式三：策略沙箱（进程级，无容器运行时）

用 OS 级沙箱包裹 golder 进程本身。以 bubblewrap（Linux）为例：

```bash
bwrap --dev-bind / / \
  --tmpfs /tmp \
  --bind "$PWD" /workspace \
  --ro-bind ~/.golder /home/user/.golder \
  --unshare-net \
  --die-with-parent \
  golder -p "总结 /workspace 的模块划分"
```

要点：`--dev-bind / /` 后逐项收紧（生产请改为白名单式挂载）；`--unshare-net` 断网；`~/.golder` 若不需要持久化也应改为 tmpfs。

macOS 可用 `sandbox-exec`（profile 限制文件写入与网络）：

```bash
cat > /tmp/golder.sb <<'EOF'
(version 1)
(deny default)
(allow process-exec)
(allow file-read* (subpath "/usr") (subpath "/Users/you/project"))
(allow file-write* (subpath "/tmp/golder-sandbox"))
(allow network*)
EOF
sandbox-exec -f /tmp/golder.sb golder -p "总结当前项目" 
```

> OpenShell、nsjail、Landlock 等同属此类：按"文件系统白名单 + 网络开关 + 进程能力"三件事配置。

---

## 决策指引

| 场景 | 建议 |
|------|------|
| 日常开发，目录可信 | trust + `--approve`，不需要沙箱 |
| 跑陌生仓库 / 竞赛代码 | 模式一（Docker，只读挂载 + 断网） |
| 让 agent 改代码但限制爆炸半径 | 模式一（可写工作区挂载 + `--disallowed-tools bash`）+ `--approve` |
| 长任务、多租户、强合规 | 模式二（micro-VM），宿主持有 key |
| 无容器运行时、需要进程级隔离 | 模式三（bwrap/sandbox-exec） |

## 已知限制

- golder 的 `--github-review` webhook 模式自身即为"只读工具集"设计（仅 read/grep/find），可与任一沙箱模式叠加。
- 容器/micro-VM 内运行时，`bash` 转为会话的长命令（返回 `bash_id`）随容器/VM 生命周期结束，不与宿主共享。
- 沙箱可能阻断 Provider 出网——务必先验证容器内能访问所选 Provider 的 base URL，再交给模型。
