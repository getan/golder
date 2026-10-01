# 沙箱与容器化

> 对标 pi 的 [containerization.md](https://github.com/earendil-works/pi-mono/blob/main/packages/coding-agent/docs/containerization.md)（issue #571）。中文文档，示例均可复制执行。

## 默认立场（与 pi 相同）

pigo **不内置权限沙箱**。默认情况下，pigo 以启动它的用户与进程的全部权限运行：`bash` 能做你用户能做的任何事，`write`/`edit` 能改工作区内外的文件。pigo 内置的护栏（trust 三态、`--approve`、`--allowed-tools`/`--disallowed-tools`）是**准入控制**——它们约束"模型是否被允许发起某个工具调用"，而不是操作系统层面的隔离。需要更强的边界时，把 pigo 容器化或沙箱化。

## 三层防线的分工

| 防线 | 约束对象 | 防什么 | 不防什么 |
|------|---------|--------|---------|
| 项目信任（trust 三态）+ `--approve` | 模型发起的副作用工具调用 | 未信任目录下的静默副作用 | 已信任目录内的任何工具调用（`--approve` 放行全部） |
| `--allowed-tools` / `--disallowed-tools` | 模型可见/可调用的工具集合 | 误用与越权调用（黑名单优先、子 Agent 继承、`--approve` 不可绕过） | 工具内部的系统调用（`bash` 仍能做任何事） |
| 容器 / 沙箱边界 | 整个 pigo 进程 | 文件逃逸、网络外联、资源滥用 | 模型层面的误判（边界内仍可为害） |

三层是**叠加**关系，不是替代：容器内仍应配合 `--disallowed-tools bash`（如果该会话不需要 shell），`--approve` 只应授予容器内进程。

## 进程内第四层：risk judge + seatbelt（macOS 先行）

> 这是 Codex 没有的东西：Codex 把“这条命令有多危险”留给人肉弹窗，pigo 用 Jev 高速分类器先定档，人只看升级件。

调用链是 `trust → judge → hooks`：trust 只认目录（信了不代表全放行），judge 给每次调用定 `Allow/Confirm/Sandbox/Deny` 四档，hooks 维持用户自定义。deny 赢，judge 只做升级不降级（trust 的 block 会短路掉后面的 grading）。

| 档位 | 含义 | REPL（可交互） | TUI/headless（无 stdin） |
|------|------|----------------|--------------------------|
| Allow | 低风险，直放 | 直接跑 | 直接跑 |
| Confirm | 中风险，需确认 | 弹窗 `[y/N]` | 在启动信任下放行 |
| Sandbox | 高风险，强制隔离 | 弹窗确认后进 `sandbox-exec` | 无 runner 则直接拒绝 |
| Deny | 极危，直接拒 | 直接拒 | 直接拒 |

分级逻辑（`internal/judge`，纯标准库叶包）：

1. **StaticFloor**：不可逆硬拒（`sudo`、`rm -rf /`、`mkfs`、写 `~/.ssh`/`~/.gnupg`/`trust.json`、整环境导出 piped 到网络、引用 grading key 本身），只拒不放，剩下全交模型。单半边合法（`echo $PATH` 调试、`curl` 正常下载、`env FOO=bar ./cmd` 传参）一律放行给 Jev；
2. **JevJudge**：一次 `choice` 四选项调用，state 带工具名、截断参数、目录信任备注；`Allow` 需高置信（≥0.75），低置信就地升级，阈值按误放成本定；
3. **兜底**：无 key（零配置）退化为纯静态地板、行为与今天一致；超时/坏响应一律升档到 `Confirm`，永不故障放行。同 `(tool, args)` 进程内缓存，门和执行层共用。

执行隔离（`internal/seatbelt`，macOS 生效、他处空实现）：CLI 与工具参数都不变，只是 `bash` 的后端从 `bash -c` 换成 `sandbox-exec -f <现场生成的 profile> bash -c`。profile 读放宽、写只限项目目录与 `TMPDIR`、默认拒写 `~/.ssh`。

可观测性：交互式门给每个副作用工具打一行 verdict（`[judge: allow] bash: go test ./...`），只读工具的 Allow 保持静默；`bash` 的流式 partial 是增量 delta（消费者直接 append），与 codex 的 `ExecCommandOutputDelta` 同契约。

```bash
export TYPESAFE_API_KEY=...   # Jev 分类 key，只经环境变量透传，不打印不落盘
export PIGO_SANDBOX=auto      # off | auto（默认，仅 sandbox 档隔离）| enforce（全部 bash 进沙箱，无 runner 则 fail-closed）
export PIGO_JUDGE=off         # 逃生舱：完全关闭 judge（只剩 trust+hooks）
# 可选：PIGO_JEV_ENDPOINT / PIGO_JEV_MODEL / PIGO_JEV_TIMEOUT_MS（默认 8s）
```

灰度建议：先零配置跑一周（只有静态硬拒生效，行为不变），再配 key 开 `auto`，最后按需 `enforce`。

---

## 模式一：Plain Docker（整进程隔离）

最简单、最常用。整个 pigo 进程跑在容器里，工作区通过挂载进入，密钥通过环境注入且不落盘。

### 构建镜像

```dockerfile
# Dockerfile
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY . .
RUN go build -o /out/pigo ./cmd/pigo

FROM alpine:3.20
RUN adduser -D -u 10001 agent
COPY --from=build /out/pigo /usr/local/bin/pigo
USER agent
WORKDIR /workspace
ENTRYPOINT ["pigo"]
```

```bash
docker build -t pigo-agent .
```

### 运行（只读挂载 + 禁外联 + 非 root）

```bash
# 密钥放临时 env 文件（用完即删，不要提交进仓库）
printf 'OPENROUTER_API_KEY=sk-or-real\n' > /tmp/pigo.env && chmod 600 /tmp/pigo.env

docker run --rm -i \
  --user 10001:10001 \
  --read-only \
  --tmpfs /tmp:size=64m \
  --network none \
  --env-file /tmp/pigo.env \
  --mount type=bind,source="$PWD",target=/workspace,readonly \
  pigo-agent -p "阅读 /workspace 并总结架构" \
  ; rm -f /tmp/pigo.env
```

要点：

- `--read-only` + `--tmpfs /tmp`：容器根文件系统不可写，模型只能通过显式挂载点写文件；`bash`/`write`/`edit` 的副作用被限制在 `tmpfs`（会话结束即消失）。
- `--network none`：彻底禁止模型外联（容器内连 Provider 都不能访问——适合"离线审阅"类任务）。若任务本身需要调用 Provider，去掉 `--network none` 并改用出网白名单（见 compose 示例）。
- `--mount ... ,readonly`：工作区只读挂载，物理上禁止改写源码。

### 允许写工作区 + 只放行 Provider 出网（docker-compose）

```yaml
# docker-compose.yml
services:
  pigo:
    build: .
    user: "10001:10001"
    read_only: true
    tmpfs:
      - /tmp:size=128m
    volumes:
      - ./:/workspace:rw          # 允许模型改工作区
    env_file:
      - .env.pigo                 # chmod 600，并加入 .gitignore
    networks: [pigo-net]
networks:
  pigo-net:
    driver: bridge
    # 出网范围自行收紧到 Provider 域名（配合防火墙/eBPF 策略更佳）
```

```bash
docker compose run --rm pigo -p -a "修复失败的测试"   # -a 只授予容器内目录信任
```

---

## 模式二：micro-VM（Gondolin 模式，高级）

pi 的 Gondolin 扩展把**工具执行**路由进一台本地 Linux micro-VM（Firecracker/qemu），agent 与 Provider 鉴权留在宿主。pigo 的等价做法（简化形态）：

1. 宿主准备一个最小 VM/容器镜像（含 pigo 二进制 + 工作区快照）；
2. 宿主进程只负责编排：把任务 prompt 通过 `pigo -p --output-format stream-json` 送进 VM 内的 pigo；
3. Provider 鉴权由宿主代理转发（VM 内不持有真实 key），或 VM 内注入一次性 env。

```bash
# 以 qemu/firecracker 之外的轻量近似为例：在一次性 VM 的 ssh 会话里跑
ssh vm0 "PIGO_HOME=/tmp/pigo-home OPENROUTER_API_KEY=\$FORWARDED_KEY \
  pigo -p '阅读 /workspace，列出所有 TODO' --output-format stream-json" \
  > run-$(date +%s).jsonl
```

> 这是"agent 在宿主、副作用在 VM"的近似：主 agent 进程持有 key，VM 只接收任务与产出产物。pi 的 Gondolin 扩展实现了更严格的"逐工具路由"形态，可参照其设计移植。

适用场景：多租户/不可信仓库分析、需要强文件系统与网络隔离的长任务。

---

## 模式三：策略沙箱（进程级，无容器运行时）

用 OS 级沙箱包裹 pigo 进程本身。以 bubblewrap（Linux）为例：

```bash
bwrap --dev-bind / / \
  --tmpfs /tmp \
  --bind "$PWD" /workspace \
  --ro-bind ~/.pigo /home/user/.pigo \
  --unshare-net \
  --die-with-parent \
  pigo -p "总结 /workspace 的模块划分"
```

要点：`--dev-bind / /` 后逐项收紧（生产请改为白名单式挂载）；`--unshare-net` 断网；`~/.pigo` 若不需要持久化也应改为 tmpfs。

macOS 可用 `sandbox-exec`（profile 限制文件写入与网络）：

```bash
cat > /tmp/pigo.sb <<'EOF'
(version 1)
(deny default)
(allow process-exec)
(allow file-read* (subpath "/usr") (subpath "/Users/you/project"))
(allow file-write* (subpath "/tmp/pigo-sandbox"))
(allow network*)
EOF
sandbox-exec -f /tmp/pigo.sb pigo -p "总结当前项目" 
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

- pigo 的 `--github-review` webhook 模式自身即为"只读工具集"设计（仅 read/grep/find），可与任一沙箱模式叠加。
- 容器/micro-VM 内运行时，`bash` 的后台任务（`run_in_background`）随容器/VM 生命周期结束，不与宿主共享。
- 沙箱可能阻断 Provider 出网——务必先验证容器内能访问所选 Provider 的 base URL，再交给模型。
