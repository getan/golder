# 沙箱与容器化

> 对标 pi 的 [containerization.md](https://github.com/earendil-works/pi-mono/blob/main/packages/coding-agent/docs/containerization.md)（issue #571）。中文文档，示例均可复制执行。

## 默认立场（与 pi 相同）

golder **不内置权限沙箱**。默认情况下，golder 以启动它的用户与进程的全部权限运行：`bash` 能做你用户能做的任何事，`apply_patch` 能改工作区内外的文件。golder 内置的护栏（trust 三态、`--approve`、`--allowed-tools`/`--disallowed-tools`）是**准入控制**——它们约束"模型是否被允许发起某个工具调用"，而不是操作系统层面的隔离。需要更强的边界时，把 golder 容器化或沙箱化。

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
| `ask` | 低风险放行，其余询问 | 弹 `[y/N]`；可隔离的 Sandbox 档在有 runner 时直接进沙箱 | 无输入可问 → 拒绝（带理由卡片） |
| `auto`（默认） | 模型审查后自动决定 | Confirm 放行、Sandbox 进沙箱、Deny 拒绝，各附一行理由 | 同左 |
| `full-access` | 不审查、不套沙箱 | 直放；仅静态硬拒名单生效 | 同左 |

切换方式：会话内 `/permissions`（TUI 是 ↑↓ + Enter 选择器，带每档说明），启动参数 `--permissions <mode>`，环境变量 `GOLDER_PERMISSIONS`（`GOLDER_JUDGE=off` 作为旧的逃生舱别名等价于 `full-access`）。模式是进程内实时状态：切换后**下一次工具调用**立刻生效，子 Agent 继承父会话的模式（进程隔离子 Agent 通过环境变量继承）。

### `auto` 的审查器（就是会话模型）

分级链（`internal/judge`，叶包）：

1. **StaticFloor**：不可逆硬拒（`sudo`、`rm -rf /`、`mkfs`、写 `~/.ssh`/`~/.gnupg`/`trust.json`、整环境导出 piped 到网络、敏感变量 echo 进网络等），只拒不放，其余全交模型。单半边合法（`echo $PATH` 调试、`curl` 正常下载、`env FOO=bar ./cmd` 传参）放行给审查器。
2. **LLMJudge**：把「工具名 + 截断参数 + cwd 信任状态 + 最近对话（guardian 式挑选：最多 3 条最近 user 消息 + 最近 3 条任意消息，工具输出每条截 1k runes，整段 4k runes 预算）」作为 state，调用**当前 provider/model** 做一次严格 JSON 评审：`{level: allow|confirm|sandbox|deny, risk, authorization, rationale}`。`rationale` 强制用对话语言书写（中文对话给中文理由），直接显示给用户。同 `(tool, args, trust, state)` 进程内缓存：一次调用里门与执行层共用一次判分，上下文变了就重判。
3. **兜底**：审查超时（默认 30s，`GOLDER_REVIEW_TIMEOUT_MS` 可调）、网络错误、JSON 不可解析一律标记为失败，绝不故障放行——`auto` 下能隔离的（`bash`）进沙箱、不能隔离的拒绝；`ask` 下弹窗问人；无 prompt 的驱动直接拒绝。审查器缺失（理论上不会发生）同样按失败处理。

新增依赖为零：审查用的就是会话模型，`/model` 切换后审查器随之切换，不需要 `TYPESAFE_API_KEY`。

### 展示（codex 风格审批卡片）

审查结果以一行卡片显示，中文/英文随对话语言：`auto` 通过 `⚠ 自动审批通过（bash，风险：中，授权：高）：<理由>`，隔离运行 `⚠ …将在沙箱内运行…`，拒绝 `✗ 自动审批拒绝（…）：<理由>`，审查失败 `⚠ 审查不可用，已按保守策略处理…`；`read-only` 阻止显示 `✗ 只读模式：已阻止 bash`。TUI 里卡片插在该调用卡片正上方（与 Codex 的 review 行 + 调用行顺序一致），REPL 直接打印。`ask` 弹窗本身已展示理由，因此不再重复卡片。

### 执行隔离

Sandbox 档的判定以"执行层**真的**能隔离这次调用"为准：门在放行前会问 `run.SandboxGate()`，它与 `WireBashSandbox` 挂载 runner 的条件逐条一致（`GOLDER_SANDBOX` 非 off、平台有可用 runner、且只有 `bash` 有 runner；`full-access` 下不挂 runner）。`SandboxGateMatchesWiring` 测试把这条一致性钉死。隔离替确认：runner 可用时不再弹窗，因为沙箱本身就是执行约束。

执行隔离（`internal/seatbelt`）：CLI 与工具参数都不变，只是 `bash` 的后端从 `bash -c` 换成平台沙箱。两个平台同一条策略——**读放宽、写只限项目目录与 `TMPDIR`、默认拒写 `~/.ssh`/`~/.gnupg`/`trust.json`，网络保持开放**（外联由审查层负责，不是文件 profile 的职责）：

- **macOS**：`sandbox-exec -f <现场生成的 profile> bash -c …`。profile 按规范 vnode 路径匹配，而 `/var` 是 `/private/var` 的软链，因此项目与临时目录同时写入**符号链接形式与 `EvalSymlinks` 后的规范路径**（只写软链形式会导致该允许的写操作被静默拒绝）。
- **Linux**：`bwrap --die-with-parent --unshare-pid --unshare-uts --unshare-ipc --ro-bind / / --dev /dev --proc /proc --bind <项目> <项目> --bind-try <TMPDIR> <TMPDIR> … bash -c …`。只读根 + 可写绑定就是写白名单；`--dev` 会挂载新的 devtmpfs（含 devpts），所以 `tty=true` 的交互式会话照常工作。secret 路径的只读重挂（`--ro-bind-try`）排在可写绑定**之后**——后挂载生效，即使项目根就是 `$HOME` 或 `$GOLDER_HOME`，`~/.ssh`、trust 存储、凭据文件与会话目录也不会变成可写。bwrap 是否可用在执行前**实探**一次（能否真的创建 userns/挂载；被 AppArmor 或内核开关挡掉时视为不可用，回退为询问/fail-closed，而不是命令跑到一半才报错）。

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
export GOLDER_SANDBOX=auto      # off | auto（默认，仅 sandbox 档隔离）| enforce（全部 bash 进沙箱，无 runner 则 fail-closed）
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
