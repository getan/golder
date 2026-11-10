# 技术方案：Tailscale 私网下的 Golder 手机远程控制

> 状态：设计提案，尚未实现或完成安全验收。需求来源：本次关于 iPhone／Android、Tailscale 与 TUI／GUI 复用的讨论，而非独立 PRD。
>
> 代码基线：Golder `6a44cd2`；设计日期：2026-11-10。实现前需再次核对实际代码及 Tailscale 当前版本的接口。

## 1. 摘要和范围

在 Mac 上运行的 Golder TUI 或 GUI 中，由用户主动开启远程控制；用户在同一 Tailnet 的 iPhone 或 Android 手机扫描二维码，经双端确认后，查看**明确选定的当前会话**、发送文字消息、处理可安全呈现的审批。电脑仍运行 Agent、项目、工具和安全门；手机不运行 shell，也不需要公网服务器或开放公网端口。

方案将**会话适配与业务状态**留在原来的 TUI／GUI 会话内，把**传输、配对、认证、协议、重放防护和审批投递**收敛到共享的 `internal/remotecontrol` 组件。Tailscale 负责 Tailnet 网络，HTTPS/WSS 提供应用层 TLS，Golder 配对凭证和原有审批链共同控制权限。三者缺一不可，任何一层失败均不降级。

### 1.1 首版边界

| 项目 | 首版决定 |
| --- | --- |
| 电脑端 | macOS 上的 TUI 和 GUI 均支持；Linux／Windows 主机留扩展点，不宣称已验证 |
| 手机端 | Safari 和 Android 浏览器的响应式 Web UI；不是原生 App |
| 通信 | 同一 Tailnet 内 HTTPS + WSS；本机入口仅 `127.0.0.1` |
| 会话 | 每次明确绑定一个已存在的 TUI 或 GUI 会话；Mac 上同时只允许一个远程服务占用该入口 |
| 手机 | 首版一个已配对控制设备、一个活跃控制连接；本地用户始终可停止／撤销 |
| 能力 | 会话快照及后续事件、文字输入、连接状态、符合条件的精确审批与拒绝 |
| 不做 | 公网／Funnel、多设备并发控制、跨设备漫游、手机侧本地 shell、图片上传、原生 App、全屏终端或浏览器远程画面 |

同一 Tailnet 不等于可信操作者：可能存在共享节点、过宽的 ACL，或已被攻陷的设备。首版仍须 Golder 内部的显式配对和会话授权，不能把“可连到 Tailnet”直接当作可执行命令或可批准操作。

### 1.2 与当前代码的差异

- `internal/remotecontrol` 已有 HTTP、WebSocket、一次性配对、简单网页及 `input/output/confirm/decide/status` 帧，但当前 URL 是明文 HTTP，令牌在 query 中，不能直接作为新安全方案的生产入口。
- 当前 [lanaddr.go](../internal/remotecontrol/lanaddr.go) 优先普通 LAN 地址并跳过点对点接口，不能把它当成 Tailscale 地址选择。
- TUI／REPL 已接入旧远控；TUI 当前只把 trust 确认路由到手机，其余审批仍在本地，且运行中的远程输入会被拒绝。GUI 尚无同等远控接入。不能把现有功能宣传成“手机能处理全部审批”。
- [GUI SessionHub](../gui/appsession/sessionhub.go) 已消费每个 Controller 的 `Events()`；再让远端并发读取同一个 channel 会**分抢事件**。新增订阅必须是广播，不是第二个消费者。
- `judge.ApprovalRequest/Answer` 包含精确 `Needs/Scope/Exec`，而 GUI 当前 `ApprovalView.Needs` 是字符串、`RespondApproval` 是简化枚举；不能据此声称已完整保留所有授权细节。
- 当前远端输入缓冲满时会丢弃最旧输入；v2 禁止静默丢弃。当前 `/pair?t=…` 和普通 cookie 也不能复用为安全配对协议。

## 2. 决策与非目标

| 决策 | 选择及原因 |
| --- | --- |
| 加密 | Tailnet 上 HTTPS/WSS，优先验证可用的 Tailscale Serve；不把链路已加密当作应用授权 |
| 部署 | 首版使用**用户预先配置／确认**的 Serve 入口；Golder 不自动修改现有 Tailscale 配置 |
| 监听 | 固定可配置的 `127.0.0.1:18787`；占用时明确失败，不自动换端口或绑定所有网卡 |
| 生命周期 | 每次启动创建新的服务实例和一次性设备凭证；停止或进程退出即撤销。长期可信设备后置 |
| 协议 | 独立版本的结构化 `/remote/v2`，与旧的文本镜像协议区分；采用已有 `coder/websocket` |
| 控制 | 保留原有模型、工具和策略；远端只是**携带人类决定的输入界面** |
| 用户体验 | 连接前本机显式授权，随后可离开电脑；只有现有策略明确要求本机确认的事项继续留在本机 |
| 故障 | 协议错误、身份不明、状态不一致、答案过期、网络断开均 fail closed；不靠重试完成未知副作用 |

首版不设计无需 Tailscale 的互联网中继，不接收任意客户端的文件路径／shell／浏览器控制指令，不暴露内部 IPC、配置、凭据、Tailscale 管理 socket 或任意文件读取 API。模型仍可通过现有工具工作，但远控不能为模型新增权限。

## 3. 架构与职责

```text
 iPhone Safari / Android Chrome
  TLS 证书验证 + 一次性配对 + 本机确认 + Secure cookie
                    │  HTTPS / WSS，Tailnet 内
                    ▼
  用户配置且确认的 Tailscale Serve 入口（不可 Funnel）
                    │  HTTP，仅 Mac 本机回环；不信任代理声明身份
                    ▼
  Remote Server v2：认证、连接锁、快照/事件、审批路由、审计
                    │  同进程接口，不直接执行工具
        ┌───────────┴────────────┐
        ▼                        ▼
  TUI SessionAdapter       GUI SessionAdapter
  Model.Update 所在线程    appsession.Controller / SessionHub
        │                        │
        └──── 既有 agent runtime / judge / trust / sandbox ────┘
```

TLS 终止于 Tailscale Serve，不是直接终止于 Golder：Serve 到 Golder 的 HTTP **仅限本机回环**，因此“端到端 TLS”不能用来描述整个进程路径。Tailnet 传输和 TLS 保护线上链路；本机内核、同用户恶意进程、拥有管理员权限者、受攻击的 Mac 或已授权手机不在这一隔离的保护范围内。

### 3.1 共享核心

在 `internal/remotecontrol` 按职责拆分，而非由 GUI 导入 `internal/cli/tui`：

1. `Service`：一条选定会话的生命周期、受限监听、入口校验及关闭；不访问 TUI `Model` 和 GUI 控件。
2. `SessionAdapter`：受限的 `Snapshot`、`Subscribe`、`Submit`、`AnswerApproval`、`Done`；每个调用都带 `instance_id`、`session_id` 和请求上下文。
3. `ApprovalBroker`：统一 ID、原始能力、状态和竞争仲裁；借用现有 gate 产生的唯一问题，不创建一条绕开本地策略的新 gate。
4. `Pairing/Auth`：一次性码、双端确认、活跃连接、失效与撤销；凭证只授予特定远控实例／会话。
5. `Transport`：HTTPS 入口适配、HTTP handler、WSS、origin 校验、消息上限和 backpressure；不解释 `approve` 为系统权限。
6. `Web UI`：同源静态资源；无第三方脚本、分析服务或远程字体；其输入、输出均按不可信处理。

### 3.2 TUI 适配

TUI `Model.Update` 仍是 UI／会话状态唯一所有者。远端 goroutine 只能投递带 request ID 的消息到有界 mailbox，由 Update 在线程内完成输入和返回 ACK；不能直接从网络 goroutine 修改模型。已有本地审批 UI 和手机共享同一个 broker 槽，先合法回答者获胜。TUI 现有 remote-only 的 trust 特例应逐步替换为统一审批接线，而不是先走远端 trust 再跳过其余 gate。

远端快照来源是结构化会话状态，不依赖将终端 ANSI 反解析为业务对象。对还没结构化的输出，允许带序号的纯文本增量，但不得假称它是完整的工具调用或全部历史。

### 3.3 GUI 适配

绑定 `SessionHub` 中被用户选择的 **controller 指针 + 稳定 session ID + 实例 epoch**；Mac 上切换 GUI 标签不会让手机悄悄转到新会话。原 controller 关闭后远端结束，不能用相同磁盘 session ID 自动接续另一个 controller。

必须改造 Controller／SessionHub 为**独立订阅者的广播**：现有 GUI 与远端各有独立缓冲和取消；不得同时从 `Controller.Events()` 或 `SessionHub.Events()` 分别读取。保证状态变更、事件序列分配、订阅边界的一致性：当前 `c.emit` 和部分状态修改分开持锁，单纯先调用 `Snapshot()` 再读原 channel 会丢事件；必须提供原子快照+订阅，或先注册有界缓冲再获取带水位快照、丢弃 `seq <= watermark`，并证明不存在“状态已改但序号未分配”的窗口。不能满足时先重构提交点，再启用远控。

## 4. 网络接入与加密

### 4.1 用户配置的 Tailnet HTTPS 入口

1. 用户通过本地 UI／命令选择 Tailnet 模式；不使用 `DetectRoutableIP()` 自动猜 Wi‑Fi 或从 `100.x` 地址前缀猜测 Tailscale 身份。
2. 服务先检查可信的本机 Tailscale 状态：进程可访问、已登录、节点身份与当前 Tailnet、MagicDNS／HTTPS 能力可用；权限不足或无法验证则停止，告知用户检查 Tailscale。实现须针对**实际支持的 Tailscale 版本**选用本机官方状态接口或本地 CLI 的结构化输出，解析失败不得用字符串猜测。
3. 在本机回环端口加进程间排他锁并监听。一个进程服务一个会话；若另一 TUI／GUI 正在服务，报告冲突、既有服务的安全显示信息及如何在本地停止，绝不接管或关闭它。锁与监听必须一起处理 TOCTOU 和崩溃残留，不可信记录不能成为杀进程凭据。
4. 用户自行配置或确认一个**专用 Tailnet HTTPS origin**，将其路由到 `127.0.0.1:18787`。Golder 验证本机 Serve 状态中确切的 HTTPS origin、端口、上游回环目标及路径映射；拒绝任何相关 Funnel／公开共享配置、冲突路由、不明状态。若不能可靠证明，拒绝启用，不提供“继续并假设安全”。
5. 首版**不自动调用修改型 Serve 命令**、不覆盖既有路由、不清除别的应用配置。配置步骤由用户完成；未来若增加一键配置，须单独精确确认具体修改、保存来源和回滚，仅清理本实例拥有的配置，并处理并发变化。
6. 手机使用二维码中的 `https://<已验证的 Tailscale DNS 名称>[:端口]/remote/v2/`；浏览器／原生客户端必须正常验证证书、主机名、有效期，不允许忽略证书错误或 HTTP 降级。WSS 使用同一 origin／端口。Tailscale Serve 的端口、TLS、WebSocket、状态输出和 ACL 的兼容性是首阶段**真实环境验证项**，不是当前已证明的能力。
7. Tailscale 断开、节点身份或 Serve 路由改变时停止接受新操作、撤销该实例连接；若重新建立，须重新校验并本机授权。Tailnet ACL／设备共享规则由用户管理；Golder 展示检查结果，不承诺能替用户证明整个 Tailnet 的策略安全。

监听 socket 只接受回环，处理到达的 `Host` 和浏览器 `Origin` 时使用服务端预先固定的规范 HTTPS origin；仅在验证真实 Serve 行为后使用其转发方式。`X-Forwarded-*`、自定义设备头和访问者声称的 Tailscale 身份均不可单独作为授权依据。任何其他本机进程即使能连接回环，也必须通过配对凭证；同用户恶意进程的内存读取、劫持或本机管理员能力不属于此方案能独立防御的范围。

### 4.2 安全响应与浏览器隔离

- 外部地址只能是规范 `https` origin，不允许嵌入用户名、密码、可疑路径、明文端口降级或重定向到第三方；绑定多个入口时首版拒绝。
- `Cache-Control: no-store`、`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff`；配置仅允许自有静态资源的 CSP（含 `frame-ancestors 'none'`、不允许外部脚本／内联脚本），禁止宽松 CORS。所有请求验证预先固定的 `Host`；状态变更 POST 和 WS Upgrade 必须携带精确匹配的 `Origin`，不接受 `null`、缺失或其他站点。浏览器顶层导航及部分同源 GET 本来可能不带 `Origin`，这些请求不能仅因缺少该头而失败；它们不得产生状态变更，受保护数据仍须有效 cookie 且不允许跨源读取。
- QR 中一次性 token 放 URL fragment，而非 query／路径；静态页面读取后立即 `history.replaceState` 清除，再经同源 HTTPS `POST` 发起配对。不得把 fragment、token 或 cookie 写日志、历史事件、错误消息、分析数据或 URL 重定向。
- cookie 使用 `Secure; HttpOnly; SameSite=Strict; Path=/remote/v2/`，不带 `Domain`；配对 POST 与 WS 均从同源页面发起。不以 GET 修改状态。配对前的 POST 需要精确 Origin、JSON 内容类型及随机配对 token／证明；配对后由受 cookie 保护的同源 `GET /bootstrap` 返回实例内随机 CSRF nonce，所有已认证状态变更 POST 同时校验 nonce 和 Origin。WS 在升级时验证 cookie 和 Origin，首个认证帧携带 nonce，验证成功前不发会话数据或接收操作；nonce 不放 URL、日志或错误消息。首次扫码跳转与 iOS cookie 行为须真机验证；不以放宽认证或禁用证书验证作为兼容方案。

## 5. 配对、认证与撤销

### 5.1 状态机

```text
OFF → STARTING → WAITING_FOR_PAIR → AWAITING_LOCAL_CONFIRM
    → PAIRED → CONNECTED ↔ DISCONNECTED → STOPPING → OFF
任何错误/过期 → EXPIRED 或 FAILED → 清理后 OFF
```

- 本机用户明确启动，生成独立 `instance_id`（随机 128 bit 以上）和一次性 token（CSPRNG 256 bit，原始值仅在二维码和内存，TTL 默认 120 秒，单次使用）。时钟使用服务端单调时间；验证码失败、过期、扫描冲突都不延长有效期。重新生成会先撤销旧 token。
- `POST /pair/start` 原子校验 token、占用该次配对且创建等待态；预览链接或普通 GET 不消费 token。手机提供**不可信的显示名**，经长度和字符限制处理。两端展示相同的短核对码，它仅供人比较，安全性仍来自随机 token 和 TLS，不以短码作为独立凭据。
- 电脑显示当前项目、会话、手机显示名和核对码，必须由**本机操作员**明确允许／拒绝；未确认、超时、Mac 会话变更、停止服务均不给凭据。手机也须明确确认；二维码／确认不得作为模型可见的 tool output、网络页标题或普通 transcript 保存。
- TUI 的二维码不能写入会话 transcript、远程事件、日志、录屏或模型可读取的 pane 捕获结果；须使用经验证的仅操作员可信显示通道。若现有终端／pane 无法保证这一点，必须改用受控本地显示通道，或暂不启用该路径，不能仅靠提示模型不要读取。屏幕肩窥和有本机屏幕读取权限的恶意软件仍是剩余风险。
- 本机确认与 token 消费、凭据签发必须在同一个受锁状态转换中校验，不允许双重提交、预览竞争、先拿 cookie 后补本机确认。确认结果到手机可经配对 ID 查询，不暴露其他会话。
- 首版只签发 **服务实例内有效**的随机 256 bit bearer cookie，服务端只保留哈希；按实例、会话、会话代数绑定。无长期设备凭据、无后台重新授权：Mac 服务重启、进程退出或用户停止后需重新扫码。这是为降低移动端首版密钥管理风险的明确取舍。
- 手机刷新或短时断线可用现有 cookie 重连；一个活跃 WS 连接用原子占位确保并发握手只成功一个。第二个连接得到 `controller_busy`，不抢占。用户可在 Mac 撤销设备或停止服务，立即关闭 WS、清空 cookie 服务端状态及所有待答问题；手机删除本地 cookie 不替代服务端撤销。
- 不把 token、会话 cookie、模型 API key 或 Tailscale 凭据交给 Agent 或记录在 repo。凭据及事件数据只驻留进程内；审计只写可匿名的设备标识摘要、会话 ID、操作类型、时刻与结果，不保存消息内容／二维码／敏感参数。日志异常不得回显 raw header、query 或 frame。

### 5.2 以后若支持长期设备

单独设计真实设备密钥与撤销／轮换、服务器重启和多进程锁；SwiftUI 可使用 iOS Keychain，Android 可研究 Keystore；**Safari 的 HttpOnly cookie 不能被应用代码直接当作 Keychain API 管理**。不得把当前 session cookie 称为持久设备密钥，也不得在首版暗中写入 `grants.json` 或长期存储凭证。

## 6. 协议与数据模型

### 6.1 核心身份

| 字段 | 来源与约束 |
| --- | --- |
| `instance_id` | 服务启动时随机生成；重启必变，不由客户端选择 |
| `session_id` | 原会话真实 ID，仅在本机选择后绑定；TUI/GUI 独立 controller 的所有权仍保留 |
| `session_epoch` | 会话对象／代次随机 ID；相同持久 session 重开也不能继承旧连接 |
| `device_id` | 本次配对成功后服务端随机分配、仅本实例有效；不可从手机上送的名称推导 |
| `seq` | 本实例内严格递增的事件水位；不拿来授权，不跨实例复用 |
| `request_id` | 客户端生成的高熵唯一 ID，服务端按设备、实例、动作及 payload digest 去重 |
| `approval_id` | 本机会话当前待决问题的随机 ID，绑定 `run_id`、`tool_call_id`、完整 request digest 和 session epoch |

### 6.2 HTTPS 与 WSS

以下路径均位于固定 HTTPS origin 的 `/remote/v2`；服务仅以配对页面、设备自有配对状态为公开例外。所有受保护端点验证 cookie、实例、会话和必要的 CSRF；**不存在可枚举其他进程／项目的通用接口**。

| 操作 | 请求 | 语义 |
| --- | --- | --- |
| 页面 | `GET /` | 自有静态页面；无会话数据；禁止缓存和外链资源 |
| 发起配对 | `POST /pair/start` | 一次性 token + 手机显示名；返回配对 ID 与核对码，不授予操作权限 |
| 等待确认 | `POST /pair/status` | 配对 ID + `pair/start` 返回的限期随机证明，返回等待／拒绝／过期／已确认；确认后签发 cookie |
| 建立页面状态 | `GET /bootstrap` | 已认证 cookie 获取当前实例的 CSRF nonce；不允许跨源读取 |
| 初始视图 | `GET /snapshot` | 当前实例、绑定会话、状态、可安全显示的消息和 pending 审批、epoch + seq |
| 实时连接 | `GET /events` 升级 WSS | cookie + 精确 Origin；`hello` 带实例/epoch/上次 seq；响应快照或连续事件 |
| 用户消息 | WSS `input.submit` | `request_id` + `session_id` + `epoch` + 文本；显式返回 accepted／rejected，不是直接执行命令 |
| 审批决定 | WSS `approval.answer` | `request_id` + 完整身份 + `approval_id` + 服务端允许的精确选项；返回结果与失效原因 |
| 撤销自己 | `POST /disconnect` | 撤销当前连接，Mac 可独立撤销；不停止 Agent 或关闭其他会话 |

MVP 文本上限按 UTF-8 字节计 64 KiB，WSS frame 上限 128 KiB；单条事件和总缓冲均有界。超限明确返回错误，不能截断后当成批准或成功输入。首版不支持客户端传文件路径或上传图片。用户输入由会话原有规则处理；若远端不能完整、安全支持 slash 命令及其附件／确认，应**显式拒绝该命令并保留手机草稿**，不能把未处理的 `/命令` 静默发给模型。具体允许的命令白名单由本机控制器提供，不把模型生成的字符串当控制权。

### 6.3 事件结构与恢复

服务器发出的帧携带 `v:2, instance_id, session_id, session_epoch, seq, type, payload`；至少覆盖 `snapshot`、消息开始／增量／结束、工具开始／摘要／结束、审批请求／解决、运行状态、远控关闭和安全错误。客户端操作另带 `request_id`；敏感输出可标记不可远传并只显示占位。

- `snapshot` 与后续事件必须满足 §3.3 的原子边界。客户端仅应用同实例、同会话 epoch、`seq` 连续的事件；发现缺口、重复或乱序时冻结操作，重新取快照，不按时间猜测。
- 按实例维持有界 ring（建议 1 MiB、最长 5 分钟）；客户端 last seq 仍在 ring 内时，原子订阅并按顺序补发。窗口外、ring 溢出、服务重启或 epoch 改变时返回 `resync_required`，拉取快照；不能从一个实例的序号接入另一个实例。
- 慢消费者不得无限占用内存或拖住 Agent UI。缓冲到上限时断开该连接并要求快照恢复，不静默丢事件后继续显示“已同步”；待答审批仍按 §7 的断线规则处理。
- 手机提交先经有界队列和会话绑定验证，再交给会话单一所有者；`accepted` 只表示已被会话接收／排队，不表示模型或工具执行成功。忙碌时 TUI 和 GUI 都应沿用本地明确的队列语义；适配器暂时不能安全排队时返回 `busy`，保留草稿，不丢弃旧消息。
- 同实例中以 `(device_id, request_id)` 和规范 payload digest 做原子去重。相同 ID 同内容返回已记录状态，不再次投递；同 ID 不同内容拒绝。记录从首次接收到实例结束**不提前过期、不驱逐**，上限默认 4096 条；满时拒绝新的有副作用请求，已有记录仍可查询。服务端必须在会话所有者的串行提交边界关联去重记录与入队，无法确认是否入队时返回 `outcome_unknown`，不自动重发。进程重启／epoch 变化后，旧请求永不自动重放；用户先查新状态再决定是否手动重发。

### 6.4 错误分类

| 错误 | 客户端行为 |
| --- | --- |
| `tailscale_unavailable` / `serve_unverified` | 本机配置页报具体阶段，不降级为 LAN／公网 |
| `port_in_use` | 说明存在的服务或占用；不自动换端口、不终止占用者 |
| `pair_expired` / `pair_rejected` | 返回本机重新发起；不重复使用旧 token |
| `unauthorized` / `revoked` | 断开并要求本机重新配对，不尝试旧凭据 |
| `controller_busy` | 保持本机当前控制者；不能抢占 |
| `session_changed` / `resync_required` | 禁止提交与审批，重新确认绑定或拉取快照 |
| `approval_resolved` / `approval_expired` | 明示已由本地／其他路径处理或过期，不能再次批准 |
| `busy` / `queue_full` / `too_large` | 不丢草稿；用户处理后再明确提交 |
| `outcome_unknown` | 不自动重放，先观察会话再由用户决定 |

HTTP 状态：身份失败 401、权限／origin 失败 403、绑定或控制冲突 409、请求过大 413、速率限制 429、内部不可用 503；响应使用稳定错误码，不包含 token、内部堆栈或未经脱敏的命令数据。

## 7. 审批语义：不能削弱现有边界

### 7.1 单一问题、精确回答

将 `judge.ApprovalRequest`／`ConsentRequest` 中的 **Kind、Source、Mode、Level、Tool、Summary、Justification、Rationale、Origin、TrustDir、精确 Need 的 Cap/Path/Dir/Key、RequestedExec、CanContain** 和服务端生成的可选答案，映射为双方都能完整显示的结构化视图。目标 ID、浏览器／pane、页面 URL、网络目标等事实在原 gate 对应位置继续校验；手机不能提交或覆盖这些事实。

服务端维护不可变的原始 request 及其摘要，手机只提交该 request 当前列出的 `choice_id`，不自行提交“扩大后的 needs、路径或 scope”。仅当 `instance_id/session_id/epoch/run_id/tool_call_id/request_digest` 与当前待决对象完全匹配，且问题仍 pending 时，才可将 choice 映射回原生 `judge.ApprovalAnswer`。任何不可完整表达的能力、平台提示或特殊宿主控制确认，首版只能提示“请在 Mac 完成”，**不得合并成笼统的 Allow**。

首次命名浏览器 profile、人工验证暂停、pane/外部控制、系统权限、风险升级等各自仍遵循既有的精确确认及平台规则；是否能从远端完成取决于原 gate 的完整表达和宿主策略。远端不能替用户绕过 `browser_paused`、沙箱拒绝或 host-control 授权。

### 7.2 竞争与失败

1. 同一问题建立一次 broker 状态，由本地 UI 和手机消费同一不可变视图；手机不可单独跳过其他 gate。
2. 本地／手机先到的**合法且针对当前问题的答案**通过原子 compare-and-set 胜出；失败的一侧收到 `approval_resolved`，不能覆盖。若用户在本地模式改变、窗口目标改变、run 结束、超时或 context 取消，先使问题失效。
3. 若连接断开且问题尚未回答，远端不可继续授权；原 gate 接收 `Answered:false` 或其约定的 fail-closed 结果，**不得把断线误记为用户永久拒绝**。本地可视情况重新发起新问题，不能复用旧 ID。
4. V1 手机仅提供 gate 明确允许的**拒绝、单次／本会话**选项；持久授权、强制关闭外部进程、安装、泄露凭据等高风险操作如需远端支持，必须逐项做显式设计和精确确认，不能默认勾选或自动扩大生命周期。既有自动审批／read-only 模式不因手机连接而变化。
5. GUI 的 `ApprovalView`／`Answer` 当前丢失了能力结构；实施时先扩充并贯通本地与远端，以保持原语义。不能把 trust 的 session/remembered 语义映射为通用布尔值，也不能因 GUI 当前枚举限制而偷偷缩小或放大权限。

## 8. 客户端与生命周期

### 8.1 手机页面

- 显示 Mac 名称（不可信的显示名需标记）、TUI/GUI、项目和绑定会话，显示“连接／同步／断线／重连／停止”；操作只有在身份和事件水位有效时可用。
- 使用逐条消息与工具摘要，不通过插入 HTML 渲染服务端文本；Markdown 若后续加入，禁用任意脚本和自动外链／远程资源。避免图片／工具输出泄漏到第三方。
- iPhone 安全区域、触摸、软键盘与 Safari 会话 cookie、Android Chrome 至少验收；后台切换断线要明确显示，回前台后认证＋重同步，不假定手机常驻或依赖推送。
- 按需使用 `wss://`；TLS 无效时展示失败，不提供“忽略证书”按钮。移动端浏览器的 HTTP 限制、未来原生 iOS ATS 等须以真实平台测试为准。

### 8.2 本地入口

- TUI：扩展 `/remote-control` 的安全模式，支持明确显示所绑定的 session、固定入口地址和二维码；`status` 不重新打印已消费令牌；`stop` 幂等。
- GUI：当前会话的明确“远程控制”入口与状态面板；选择当前 controller 并确认后才开始，二维码与设备撤销仅本地用户可见。
- 服务在会话关闭、进程退出、Tailnet／Serve 失效或本机 stop 时停止；停止只撤销远控和待决远程确认，**不自动停止 Agent、关闭另一个窗口、清除 Tailscale Serve 配置或修改网络 ACL**。
- 验收“关闭／重开”必须区分：WSS 关闭、Golder loopback listener 消失、Serve 配置仍存在但后端不可用；不要把 HTTP 断开误判成系统网络配置已被撤销。

## 9. 数据、隐私与边界

会话正文、项目路径、工具输入输出和审批摘要可能包含私有代码或密钥。用户在 Mac 确认连接时必须知道**手机将能查看本次会话的文本及相关审批**；仅传输已有可见内容，不发送完整进程环境、provider 凭据、浏览器 cookie、无关会话或原始控制 socket。

- 服务端按明确的**可远传事件 allowlist**投影，默认未知字段不发送；普通 transcript 和工具结果中仍可能由用户或工具带入秘密，现有脱敏不是保证。私有授权、MCP／OAuth 密码、配对流程及 system dialogs 一律本地完成、不进入远端事件流。
- 限制消息、审批、WS 连接及事件队列数量；首次连接的配对请求有速率限制与过期清理。不得把未批准用户输入静默丢弃后返回成功。
- 本实例数据仅内存：随机 token 使用系统 CSPRNG，服务端仅保存凭据摘要；停止和关闭清空有效凭据与缓存。不要把它当作安全擦除已复制内存或手机截屏的保证。
- 隐私边界不扩大 `GOLDER_HOME`、`grants.json` 和系统密钥目录的 Agent 访问；不依靠把秘密写到项目目录或 `TMPDIR`。相同用户的恶意进程、root、受控设备、截图／通知泄漏属于剩余风险。

## 10. 兼容、迁移与回滚

1. `/remote/v2` 与旧的 `/pair`、`/ws` 不复用 token、cookie、状态字段和授权语义；服务以显式协议版本握手，未知版本拒绝，不协商回不安全的 v1。
2. 旧 LAN HTTP 功能是既有行为，不可悄悄声称已经安全：上线前给出迁移说明。新入口默认安全 v2；若因兼容需要保留旧模式，须是明确的本机开发／局域网 opt-in，有醒目的**未加密**提示、不同名称和令牌，默认关闭且不暴露公网。保留范围、移除时间由发布决策确认；不自动回退。
3. 本方案不改历史 session 文件格式作为 v2 上线前提；如果后来要持久设备或 request 去重，须单独设计格式迁移、锁、权限和回滚，不能在旧 `grants.json` 中复用宽泛条目。
4. 配置和 Serve 状态有冲突／不可验证时，保持旧本机 TUI/GUI 正常工作，仅拒绝远控。上线设独立功能开关和状态提示；回滚关闭 v2，撤销所有实例内会话，不清除用户已有的 Serve 配置。

## 11. 错误场景与测试验收

### 11.1 必须通过的自动测试

| 范围 | 验收 |
| --- | --- |
| 接入 | Tailscale 未安装／未登录、IPv4/IPv6／多网卡、Serve 状态不可解析、Funnel 开启、目标不是回环、端口被占时 fail closed；不访问公网、不修改既有 Serve |
| 配对 | token 过期、重放、并发双扫、预读取、手机与 Mac 核对码不同、用户拒绝、Mac 会话切换／服务关闭均不签发凭据；日志不含秘密 |
| Web 安全 | 非 TLS 公网请求无法进入；错误 Host／有副作用请求的错误或缺失 Origin／CSRF、跨站 WS、无 cookie、cookie 重放和过期均拒绝；同源普通 GET 与顶层扫码导航可正常工作；CSP、cookie 属性正确 |
| 连接并发 | 同时握手只能一个成功；撤销后当前连接关闭，新帧不可执行；未授权设备不能用另一个设备 ID |
| 会话隔离 | TUI／GUI 多 session、同 ID 不同 epoch、切换 controller、close/reopen、独立进程和占用端口：不交叉读写／误关闭 |
| 数据一致性 | 快照与并发事件无缺口，ring 边界、重复／乱序、慢客户端、重连与实例重启都按 §6.3 同步或失败；事件广播不影响本地 UI |
| 输入 | 同 request 同内容只入队一次，不同内容冲突；去重表满后不能驱逐旧记录或重放；超限、满队列、忙碌／未知状态不丢消息、不自动重试；TUI 所有状态变更仅 Update 线程 |
| 审批 | 精确 capability／scope／目标映射、本地与手机并发回答、取消／断线／过期／目标替换、重复回答及子 Agent 来源；每条原 gate 仍执行 |
| 隐私 | 令牌、凭据、私有 OAuth／MCP 信息不出现在日志、QR 以外的 UI、事件流、transcript 或模型可读取的 pane 捕获结果；未知字段默认不广播；无法隔离二维码显示时不得启用 |
| 兼容 | v1/v2 不混用凭据、不降级；不可用时 TUI/GUI 本地功能不受影响；启动／停止并发和崩溃释放锁可重复测试 |

测试中不使用用户的真实 Tailnet、真实令牌或个人浏览器数据。Fake transport、固定时间和临时证书应覆盖异常分支；iOS/Android 真机与 Tailscale 联网测试必须单独 opt-in，展示要做的网络及系统配置，不自动更改个人设置。

### 11.2 真机／集成验收门槛

在明确授权的测试 Tailnet 中，至少完成 iPhone Safari + Mac GUI、iPhone Safari + Mac TUI、Android Chrome + 两种前端：

1. 手机和 Mac 不在同一 Wi‑Fi（可使用手机蜂窝网络）时，验证 DNS、证书、HTTPS、WSS、配对、收发消息；不做公网端口映射。
2. 分别验证目录信任、工作区外读／写、网络、沙箱外执行等在支持的精确语义下的真实审批；不支持的宿主确认必须明确要求回 Mac，不能假装已批准。
3. 前后台切换、网络丢失、Mac 睡眠／唤醒、手机重连、关闭会话、重启 Golder、撤销配对、Tailnet 切换、证书错误和 Serve/Funnel 配置变化均符合 fail-closed。
4. 两个会话和两个设备同时尝试连接／回答，检查事件、审批和文件不会跨会话；长输出下原生界面与 Agent 不被慢手机拖死。
5. 验证 Tailscale 版本、MagicDNS／HTTPS、可用端口及 Serve 是否保留 Host、Origin 和 WSS Upgrade；任何假设不成立，先改设计，不能把测试标绿当作安全证明。

## 12. 实施阶段与交付

| 阶段 | 内容 | 完成条件／依赖 |
| --- | --- | --- |
| 0：可行性 | 测试 Tailnet、Serve 状态读取、专用 HTTPS 入口、WSS、iPhone cookie 与系统权限 | 写明实际支持的 Tailscale/OS 版本；无安全降级 |
| 1：共享核心 | v2 身份、原子配对、origin/CSRF、单连接、生命周期、协议和错误 | fake transport + 并发安全测试；与 v1 隔离 |
| 2：事件 | 快照一致性、独立订阅、多播、恢复、背压和去重 | 先重构 GUI 单消费者风险；TUI 单线程语义不变 |
| 3：审批 | 抽取完整 request/answer，统一 broker，本地／远端竞争与 fail-closed | 逐项比对现有 gate、拒绝不支持的精确语义 |
| 4：TUI／GUI | 各自会话适配器、开启／停止／撤销、二维码、本地状态 | 同一套协议契约测试通过；两个进程冲突处理可见 |
| 5：移动网页 | Safari/Chrome 响应式 UI、重连、错误与无障碍 | 真机四组合通过，不能以交叉编译替代 |
| 6：灰度 | opt-in、文档、旧版迁移、安全复核、发布开关 | 安全验收通过才作为正式能力，不宣称已验证超出范围 |
| 后续 | SwiftUI App、Android App、持久设备、会话切换／图片上传 | 分别做独立需求和安全评审；不在 V1 顺带扩大权限 |

## 13. 风险、开放问题与评审结论

**必须在实现前做的可行性验证：**目标 Mac 的 Tailscale 版本是否支持非侵入式读取 Serve 状态、HTTPS 专用 origin、WSS 转发和可靠识别 Funnel；iPhone Safari 通过所选 origin 扫码时是否满足证书和 Strict cookie 行为。若某个事实无法可靠验证，暂停该实现选择，不能把不受信任的代理头或用户输入当成安全证明。

**需要产品确认的非阻塞取舍：**V1 是否接受一次只共享一个会话／一次仅一个远端设备；手机断线时是否结束所有待决问题；是否需要长期设备而不是重启后重新扫码。本文推荐上述保守默认值；变更需重新评审权限和恢复语义。

**主要风险：**GUI 单消费者事件与状态水位需要重构；TUI/GUI 审批语义不完全一致；已认证设备或 Mac 本身被攻陷无法由本方案阻止；用户配置的 Tailnet ACL 过宽可能带来额外访问者；网络断线和慢客户端可能造成状态不确定；iOS 后台会暂停连接。每项都必须按 §11 实测，不能因同网段、一次成功或一个二维码而宣布安全。

**明确不承诺：**本文不是现有功能清单；尚未实现或运行真实 Tailnet／iPhone 测试；HTTPS 不替代配对或本机风险审批；Tailscale 不替代 Golder 的权限与沙箱；不保证同用户恶意软件或持有手机的人无法操作已授权会话。
