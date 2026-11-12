# 让浏览器默认持续自动化，并把必要的人工介入变成一次可验证的恢复

- Author: Golder
- Last updated: 2026-11-11
- Status: 代码已实现、依赖已发布锁定、本地回归已完成；未声明飞书实站验收通过
- Baseline: `27fe62f`，包含浏览器目标审核修复 `eaf1e0e`
- Discussion: 本次关于 v1.5.79 浏览器暂停、授权后无法继续，以及 auto／ask 体验的用户反馈；尚未关联 Issue。

## 实施状态（2026-11-11）

本文后续章节保留设计约束；实际交付和验证状态以本节及
[本地验收记录](browser-automation-continuation-validation.md) 为准。

- 配套依赖已通过 [kitty-browse PR #5](https://github.com/getan/kitty-browse/pull/5)
  合入，发布 `v0.2.12`（`0064264`）。Golder 的 `go.mod`／`go.sum` 已锁定该
  公开版本；最终验证使用 `GOWORK=off` 与 `-mod=readonly`，不依赖本地 fork。
- host 已实现条件恢复、每 view 的 epoch／pause generation、受限回执、
  私有新鲜 challenge 证明与显式 Retry-After 保留。公开 `interaction.resume`
  不再解除暂停，能力广告为 `interaction.status.v2`，保留 `/v1` envelope。
- Golder 已接入每会话的 coordinator、GUI 抽屉／composer 按钮、TUI 主会话卡片、
  pane 按钮及 Tab／Enter／Space、本地命令、已展示请求下的窄确认短语。
- 等待意图保存在前端 run 状态中，而非可重放的旧 browser action；一次恢复只
  调度一次合法接续。关闭、只读、新任务与取消保持原权限边界。
- 归属恢复要求私有 view 证明及本次有效运行环境审核。profile 标签不再创建归属；
  launch／reuse 的事实由宿主生成，deny／contain／sandbox enforce 不放宽。
- kitty-browse 的全仓 race、build、vet 和 Linux/arm64 交叉编译已通过。
  Golder 本地 build、vet、交叉编译及全仓 race（明确排除一个基线失败）已通过。
  `TestControlledPaneActualOSIsolation` 在未修改的 `27fe62f` 上同样复现失败；
  未删除、弱化或将其计作通过。Linux 容器预检按用户要求暂停。
- 真实飞书账号任务及 GUI／终端复用器的现场人工验收未执行；不把 fixture、
  renderer 事件测试或本地 Chromium 成功等同于实站全链路完成。

## Abstract / 摘要

我们将浏览器工作流的默认值明确为 Agent 持续自动操作。普通登录页、已登录页面、页面跳转、标签页切换和短时加载不要求用户接管。只有用户明确要求，或遇到确实不能由 Agent 完成的验证与阻塞时，才进入人工介入流程。

现有权限审核仍然检查实际动作。我们不把“尽量自动化”实现成关闭审核、导出 Cookie、绕过验证码或允许模型自行解除人工暂停。改造重点是分离浏览器归属、实际运行环境、交互暂停和单次动作授权，并通过可信前端建立一次性恢复闭环。用户完成必要操作后，只确认一次即可在同一浏览器继续原任务，不必切换全局权限模式，也不必反复回复“继续”。

**接管是例外，暂停不是所有权转移，恢复不是重放。**

## Background / 背景与动机

### 1. v1.5.79 的拒绝不是用户没有表达意愿

用户报告的链路如下：

```text
Agent: browser pause → paused=true, reason=manual
用户: 已授权
Agent: 再回复“继续”
用户: 继续
Agent: browser resume
Gate: requires a person's exact-call confirmation; switch /permissions to ask
```

当前两段代码直接构成这个闭环缺口：

```go
// internal/agenttool/browser_profile.go
return a.Action == "resume" || a.Profile != "" && !t.Control.profileAllowed(a.Profile)

// internal/judge/consent.go
if call.RequireOperatorConfirmation && mode != permissions.Ask {
    return blockCall(..., "... switch /permissions to ask")
}
```

自然语言表达的意愿没有转成执行器认可的精确确认。要求用户再说一遍同一个意思不能生成这个能力。auto 模式又直接拒绝所有此类工具调用，因此 Agent 只能让用户切模式或寻找 pane 的快捷键。

日志中的 `reason=manual` 说明那次暂停由显式 pause 路径触发，不能据此断言站点检测到了验证码。需要限制不必要的主动暂停，而不只是改进暂停后的文案。

### 2. 旧代码把执行环境问题也报告成“不是 Agent 的浏览器”

v1.5.79 的 `ControlGuard.checkBrowser` 将归属与可收容性放在同一成功分支。即便找到了正确归属，若调用要求外层 OS 沙箱，而浏览器已在 macOS 的 browser-runtime 环境运行，也可能落入 `browser is not owned by this agent`。这会误导模型去申请 `host_control`；该能力并不能把已运行的 Chromium 放进外层 Seatbelt。

`eaf1e0e` 已拆开错误分类，增加目标定向审核证据、实际可收容性判断和 GUI／工具会话绑定同步。本文建立在这些修复上，不重复替换它们。

### 3. 基线具备的能力与尚未完成的能力必须分开

| 能力 | 基线状态 | 本方案的工作 |
|---|---|---|
| 持久项目默认 profile、共享 broker、每会话独立 view | 已有；`kitty-browse v0.2.11` | 保留，不另建登录身份体系 |
| GUI／TUI 使用同一会话绑定，目标在审核前固定 | 已有；含 `eaf1e0e` 修复 | 补充恢复时的代际校验 |
| 页内人工输入不等于所有权转移 | 已有设计约束 | 增加正向端到端回归，避免错误诊断 |
| locator 的定向证据与执行前复查 | 已有；`browser_review.go` | 保留并覆盖恢复后的首个动作 |
| auto 中人的精确恢复确认，不切全局模式 | 未完成 | 新增可信恢复请求与私有确认通道 |
| GUI 明确显示暂停原因并提供恢复按钮 | 当前界面未形成完整闭环 | 新增小型状态区，不扩展大号顶部按钮 |
| broker 证明同一 view 的归属和运行环境 | 当前重连／发现不等于完整证明 | 新增私有证明契约，不凭 profile 标签恢复权限 |
| 恢复绑定 pause generation 且不重放旧动作 | 当前公共接口未提供本文规定的完整契约 | 需要配套 host 能力，不能只改 Golder 提示词 |

上述“已有”不代表真实飞书全链路已通过验收。近期本地协议 fixture 的测试与真实站点登录、授权、恢复必须分开报告。

## Goals / 目标与非目标

### 4. 普通任务不增加人工步骤

我们承诺以下产品行为：

1. Agent 打开的受控浏览器是默认工作面。正常任务从导航到结果读取尽量自动完成。
2. 普通登录表单、OAuth 跳转、二维码页面本身不是暂停依据。能继续的合法流程继续；缺少用户身份材料、需要 MFA 或用户决定授权范围时才请求必要帮助。
3. 用户在同一受控 view 完成一次登录，不丢失原归属，不要求逐次申请 `host_control`。
4. 必要介入后，GUI 和 TUI 都提供可见、可点击的“恢复自动化”按钮作为主要入口，一次点击即可恢复。快捷键、命令和有绑定的用户回复只是辅助入口；auto／ask 都不要求切全局模式。
5. 恢复后重新观察当前页面，再规划后续动作。不得自动重发暂停前的点击、表单提交或执行结果未知的动作。
6. 拒绝必须说明是目标变化、暂停、归属、审核、能力缺失还是运行环境不满足，而不是统一归咎于用户未授权。

本次不做：个人 Chrome profile 导入、Cookie 导出、验证码破解、代理／身份轮换、关闭 Chromium 沙箱、通用外部浏览器永久控制授权、远程手机恢复浏览器的能力扩展，以及代替用户盲目批准新的账号／应用权限范围。

ask 仍然保留其应有的审批。减少无意义接管，不等于把 ask 变成 full-access。

## Design / 设计

### 5. 四种状态分别保存，不再互相推断

```text
用户任务 → Agent 规划
             ↓
       BrowserTool.PrepareCall
             ↓
      归属／运行环境证明 + 目标证据
             ↓
      ConsentGate：审核本次实际效果
             ↓
      host：复查目标、暂停代际、调度输入

必要人工介入 → 可信 GUI／TUI ContinuationCoordinator
             → 私有 operator 能力 → 同一 host 的条件恢复
             → 结果通知原会话 → Agent 重新观察后继续
```

| 维度 | 所有者 | 代表什么 | 不代表什么 |
|---|---|---|---|
| `Ownership` | Golder 控制注册表及 broker 私有证明 | 当前会话可控制这个确切 view | 不批准页面内的任意写操作 |
| `RuntimeEnvelope` | 可信启动器、broker | `outer_os_sandbox`／`browser_runtime`／`external` 等实际环境 | 不把“Chromium 自身沙箱”称为外层文件沙箱 |
| `InteractionState` | host | 正在执行、暂停原因、暂停代际、人工交互占用 | 不改变归属和 profile 授权 |
| `ActionDecision` | 权限门禁 | 本次目标与效果是否准许、需要哪些能力 | 不自动恢复暂停，不永久授权其他动作 |

`browserbinding.Binding` 继续只负责选择和展示，不存 operator credential、恢复令牌或授权结果。GUI 选择一个 view、用户点击网页、页面登录成功，都不能单独制造控制权限。

### 6. Agent 只在有具体阻塞时请求介入

工具说明、系统工作流和错误提示统一遵循：

| 情况 | 默认处理 |
|---|---|
| 普通登录页、登录跳转、已有持久会话 | 自动检查并走正常流程；不调用 pause |
| 页面尚未加载、DOM 更新、stale ref | 有界等待或重新观察，不要求用户接管 |
| 已证明未发送的定位失败 | 收窄 locator、重新审核新调用；见第 12 节 |
| 用户直接点击／输入 | 由短时输入仲裁避免冲突；不自动升级成持久“用户控制” |
| 缺少密码、设备确认、短信码、MFA | 只请求缺失步骤；用户敏感输入留在可信浏览器表面 |
| 授权页需要新增应用权限 | 核对任务授权与账号、应用、范围；无法建立对应关系时让用户决定，不能因“自动化优先”擅自同意 |
| host 锁存可见验证、明确访问限制或主文档 429 | 停止新变更，保留状态，给出一个绑定的介入请求 |
| 用户明确说“暂停／我来操作” | 暂停该 view；等待用户主动恢复 |
| 站点明确禁止或经过有界恢复仍无法完成 | 报告阻塞，不循环挑战，不另开浏览器绕过 |

模型调用 `pause` 增加 `reason` 和简短 `justification`，其中 reason 仅允许 `user_requested`、`assistance_required`。这些字段是 Agent 的声明，不是 host 证明，也不构成恢复授权。历史调用未带字段时按 `agent_requested` 记录，不能伪装成验证码或用户要求。

只有真实前端输入事件才能标记 `operator_requested`；只有 host 检测才能标记 `verification`、`rate_limit`。普通的文档标题或页面里的“验证码”字样不能直接触发持久暂停。已有检测规则保留，并在本地普通登录／文章／隐藏组件 fixture 上检验误报。

### 7. 交互状态按 view 管理，恢复必须匹配当前代际

拟增加的安全状态快照如下。字段名是新契约，不是当前 v0.2.11 已提供的保证：

```go
type InteractionSnapshot struct {
    BrowserID       string
    ViewID          string
    InstanceEpoch   string // 进程／view 重建后改变
    State           string // running | paused | resuming | closed
    PauseGeneration uint64 // 每次新暂停递增；恢复不复用旧代际
    PauseSource     string // agent | operator | detector | unknown
    PauseReason     string // user_requested | assistance_required | verification | rate_limit | unknown
    Busy            bool
    RetryAfterMS    int64  // 仅当存在明确限流信息；0 代表未知
}
```

人类需要帮助与否由工作流记录；它不通过一个模型可填写的 `human=true`、`approved=true` 字段实现。

状态转换：

```text
running --显式暂停／host 检测阻塞--> paused(g)
paused(g) --可信人的绑定确认--> resuming(g)
resuming(g) --同一实例、代际匹配、无冲突动作--> running
resuming(g) --新暂停／实例变化／能力缺失--> paused 或 failed，不发页面输入
running --新的验证仍存在--> paused(g+1)
任意状态 --view 关闭--> closed
```

- 暂停禁止新的自动变更；已经执行的动作可能结束，不能谎称已撤回。
- 可用的只读 `status` 不触发页面变更。暂停期间不为审核器反复抓取 locator 或探测挑战。
- 恢复先等待正在结束的动作进入稳定点，最多 5 秒；超时返回 `browser_busy`，不取消或重放那个动作。
- 用户确认只解除所确认的一次暂停。挑战消失本身不自动恢复；下一次新暂停必须有新的人的决定。
- host 仍在实际输入前检查验证与限流。恢复确认不会关闭检测器，不会清除服务器指定的冷却期限。显式 `Retry-After` 未到期时不发送请求。
- 页面发生正常登录跳转可继续保留待恢复请求；请求绑定 view 和暂停代际，不将登录前文档 URL 当作恢复目标。后续动作必须从跳转后的新观察开始。
- 新 view、换会话或重新暂停会撤销旧请求，A → B → A 也不能让旧决定重新生效。

### 8. 可信前端保存恢复请求，模型拿不到确认能力

新增 `ContinuationCoordinator`，供 GUI 和 TUI 共用。建议作为 `internal/browsercontinuation` 包实现，与 `browserbinding` 分离。它保存有界、仅内存的待决请求：

```go
type PendingContinuation struct {
    RequestID       string
    ConversationID  string
    BrowserID       string
    ViewID          string
    InstanceEpoch   string
    BindingRevision uint64
    PauseGeneration uint64
    TaskRunID       string
    CreatedAt       time.Time
    ExpiresAt       time.Time
}
```

`RequestID` 用安全随机值；请求有效期 5 分钟，每个 view 仅一个，每个 controller 最多 8 个。重复观察同一暂停只更新展示状态，不重复弹窗或重置有效期。超过上限不淘汰一个已展示请求而悄悄批准另一个。

请求的公开视图仅包含可读的浏览器标识、站点 origin、原因和过期时间；不含 URL 查询令牌、网页内容、profile 路径或任何 operator credential。公开 RequestID 是关联标识，不是授权令牌。

可信用户确认后，coordinator 调用私有 operator seam 完成条件恢复。恢复凭证只在可信组件与 broker 之间传递，绑定上述身份及 `operation=continue_automation`，一次性消费；不写入工具参数、argv、对话、页面 DOM 或普通 workspace 元数据。模型无法通过抄 RequestID 产生确认。

失败分两类处理：

1. host 明确报告未消费、未恢复：保留当前请求，并显示可恢复原因；目标或代际变化则撤销。
2. 传输超时等结果未知：不重发恢复操作，先读取同一 view 的 interaction state 和私有请求消费状态。若无法判定，显示“恢复结果待确认”，不把另一操作当作成功证明。

用户停止 Agent run 时，取消该 run 的自动续跑意图。即便用户后来解除浏览器暂停，也不自动重启已取消的 run；可在下一条用户任务中继续使用该 view。会话关闭、进程重启、view 关闭、权限转只读均使未消费请求失效。

#### 8.1 恢复浏览器和恢复任务是两个独立结果

coordinator 另存一份 `ContinuationIntent`，包含 `TaskRunID`、原任务标识、`RequestID`、`awaiting_browser`／`cancelled`／`consumed` 状态及用户决定序号。它不保存一个可重放的旧 browser action。

当 browser 工具返回可信 `browser_paused` 时，运行层把该任务标记为等待浏览器；先完成当前批次的安全收尾，不让执行器无限阻塞，也不让模型继续发变更请求。其他会话正常运行。恢复确认可以在 Agent 已结束本轮回复后处理，不依赖一个仍阻塞在原 `resume` 调用中的 goroutine。

host 确认恢复后，前端把不含凭证的 `BrowserContinuationReady` 事件投递到原会话：

- 原 run 仍活跃时，在工具批次后的安全边界消费一次通知，要求重新观察，不并发启动另一个 run。
- 原 run 已因为等待浏览器而结束、且等待意图仍有效时，用户本次恢复决定同时授权发起一个原任务的接续 turn。前端用当前权限模式和最新会话配置启动，不沿用旧调用的能力位。
- 原任务已完成、被用户停止、已被新任务取代、会话只读或已关闭时，只更新浏览器状态，不启动接续 turn。
- 同一决定序号只能调度一次接续。按钮、pane 事件和聊天确认同时到达也不得产生两轮任务。

因此“浏览器已恢复，但原任务未自动续跑”是合法且必须说明的结果，不应误报为完整继续。用户取消与通知消费发生竞态时，取消优先于尚未启动的接续。

### 9. 用户只确认一次，auto 和 ask 都走同一个恢复入口

#### 9.1 GUI 将“恢复自动化”放在浏览器抽屉的状态区

暂停时显示一条紧凑状态信息和“恢复自动化”（`Resume automation`）／“保持暂停”（`Keep paused`）按钮，正常运行不占据大块界面。沿用现有单色手机／浏览器顶部图标，不新增大号控制按钮。这是 GUI 的主要恢复入口，不要求记住 Ctrl+G 或回到聊天输入“继续”。

点击恢复是可信前端事件，直接确认当前展示的待决请求。不再让 Agent 生成一次 `browser resume`，再把同一问题交给通用审批重复询问。

按钮必须绑定构建该帧时的 RequestID、view epoch 和 pause generation；慢回调不能作用于后来选中的页面。UI 在线程外请求 host，主线程应用结果时再次校验会话与绑定。若当前仍有需要用户决定的账号授权范围，状态区不能将“恢复自动化”伪装成“批准全部权限”。

#### 9.2 TUI 也提供可点击按钮，不依赖 Ctrl+G

TUI 主会话在输入框上方显示紧凑的浏览器暂停状态卡，包含浏览器标识、站点 origin、简短原因，以及可点击的 `[恢复自动化]`、`[保持暂停]` 按钮。状态卡独立于滚动 transcript，暂停未解除前保持可见；不要求用户切换到浏览器 pane 才能操作。多个待决请求各自对应确切的状态卡，不能用一个不指明目标的按钮恢复任意浏览器。

浏览器 pane 自身的暂停状态栏也提供同名的可点击恢复按钮，调用同一 host 条件恢复接口，不模拟 Ctrl+G 按键。主会话与 pane 两个入口绑定同一 view、epoch 和 pause generation；点击其中一个成功后，两处状态同步，另一处旧按钮失效，不产生第二次恢复或接续 turn。

按钮支持鼠标点击及键盘聚焦后 Enter／Space 激活。TUI 命中区域随重绘、终端缩放、窗口尺寸变化更新；鼠标按下和释放之间若目标请求变化，取消该次点击。点击状态卡不能穿透到输入框，点击 pane 状态栏不能向网页发送一次额外点击。恢复请求正在提交、会话只读或请求失效时有明确的禁用状态和原因，不能出现“看起来点击了但没有结果”；Agent 正在等待浏览器不能成为禁用恢复按钮的原因。

点击后立即显示“正在恢复…”并禁用重复提交；收到 host 回执才显示“自动化已恢复”。若原任务仍满足接续条件，则另行显示“正在继续原任务”；若原任务已取消，则说明只恢复浏览器、不重启任务。失败或结果未知时显示具体状态并保留诊断入口，不能静默返回暂停画面。

`Ctrl+G` 保留为 pane 的辅助快捷键，不是恢复按钮的实现方式。再提供 `/browser continue [request-id]`、`/browser keep-paused [request-id]` 作为终端未开启鼠标、鼠标事件被复用器拦截或快捷键冲突时的替代入口；单一已展示请求可省略 ID，多个请求须明确选择。命令由本地前端处理，不转成模型自行构造的 operator 调用。

这些入口都产生可信状态变化通知。Golder 接到成功通知后撤销对应待决请求，不再要求同一个用户回到聊天里再回复“继续”。

#### 9.3 “已授权／继续”只在明确的待决上下文中成为确认

允许用户在原会话直接回复“继续”或“已授权”。前提必须同时成立：

1. 前端此前已经展示一个具体的浏览器介入请求，并说明“完成后回复继续／已授权将恢复这个浏览器”。
2. 当前会话恰好一个有效待决请求；用户输入来自可信本地 composer／stdin，不是工具结果、引用网页、模型消息、其他 Agent 或远程 API。
3. 输入去除首尾空白后，是完整的单行确认短语，例如 `继续`、`已授权`、`已授权，继续`、`continue`；不是在长消息里寻找子串，也不让 LLM 判断“用户似乎同意了”。
4. 提交时重新校验会话、view、pause generation 和有效期。绑定变化先撤销旧请求，不自行挑选另一个浏览器。

符合条件时前端生成与按钮相同的类型化决定，并记录“用户已确认恢复；这不批准页面的后续写操作”。若多个目标或已过期，展示一次目标选择／更新请求，不反复要求同一句话。没有待决请求时，这些词仍是普通会话输入，不授予能力。

此窄解析入口不推广到支付、应用权限扩大、主机控制或命名 profile 初次授权。手机远控暂不接入此确认来源；其发来的同名文本保持普通消息，不自动产生本地 operator 身份。

#### 9.4 模型的 resume 保留，但不能制造人的确认

旧 `browser resume` 保留兼容：没有可信确认时返回 `browser_continuation_required` 以及当前目标的操作提示，不再只建议切换全局 ask。

若 ask 已经提供了针对这个确切恢复动作的本地人类确认，可由适配器转交 coordinator，仍必须验证 pause generation 并一次消费。auto 的普通审核 `allow/confirm` 不能代替这份确认。已经在前端完成恢复时，Agent 无需再调用 resume，只获取一次最新状态及快照。

具体接入必须避开当前“先被通用 operator-policy 拒绝、后面才知道是恢复”的顺序问题：无可信决定的 auto resume 在 BrowserTool 的只读准备阶段即返回结构化 continuation 请求，不进入普通执行。已有可信决定由 coordinator 的私有路径消费，而非把通用 `RequireOperatorConfirmation` 改成可在 auto 自动满足。ask 的兼容 resume 则先固定同一暂停代际，在一次本地确认后转入同一 coordinator；若已恢复只返回当前状态，不再发一次恢复命令。其他工具和命名 profile 的精确确认规则不变。

初次使用非默认命名 profile 的精确人类确认规则本次不放宽；也不得将一般性的“以后尽量自动化”当作所有未来暂停的恢复凭证。

### 10. 受控浏览器不会因为人工登录而变成外部浏览器

同一运行期内，`ControlGuard` 的归属记录与 login／pause／foreground selection 无关。人工输入、网站登录、抽屉开关只改变交互或展示状态；不删除 `controlledBrowser`，也不把它改成 `external`。

对于工具或前端重绑定、同一会话恢复后需要重建记录的情况，增加私有 `ViewAttestation`：

```text
project_key + conversation_id + view_id + browser_id + instance_epoch
+ canonical_socket + profile_identity + runtime_envelope
+ proxy_mode + runtime_policy_digest + proof_generation
```

证明必须来自认证的 broker 管理通道，绑定随机挑战和当前实例，并由 Golder 的控制注册表核验。`LoginProfile` 字符串匹配、仓库 `.golder` 元数据、socket 文件存在、同 Unix 用户、浏览器标题或网页声明，都不足以恢复 Agent 归属。

重建流程：读取候选定位记录 → 用可信 conversation／project 派生 view ID → 私有 broker 重连并验证证明 → 校验当前 profile 授权及 policy → 只为该确切 view 注册控制 → 发布新的 binding。不得恢复另一会话的 view，命名 profile 初次确认也不能被这一步绕过。

运行环境证明是事实，不是新的授权。宿主只能证明“这个 view 实际运行在 browser-runtime 环境”；Golder 仍须检查本会话的有效启动决定或走当前模式的一次精确环境审核。不能把一个外部启动、仅自称相同环境的进程当作以前已经批准的启动，也不能从重连自动得到 bash／终端权限。

现有 `ConnectSessionBrowser`／`ReconnectShared` 可作为私有连接基础，但当前返回的公开字段并非上述完整证明。需要在依赖新增类型化能力；不能把“拿到了 RemoteView”或“metadata 对上了”写成已经完成环境证明。

若 broker 无法证明环境，则诚实报告 `browser_ownership_unverified`，保留观察／手动表面能力的原有限制，不请求 `host_control` 来掩盖受控记录损坏。真正外部浏览器仍按现有外部授权流程；本方案不自动把用户日常浏览器纳入 Agent 控制。

### 11. 审核实际效果，不能承诺一个不存在的沙箱

#### 11.1 普通操作的模式语义保持清楚

| 情况 | auto | ask |
|---|---|---|
| 只读观察、合法状态读取 | 既有安全快路径 | 同左 |
| 目标明确且符合任务的普通操作 | 审核 allow／confirm 后执行，不问用户接管 | 沿用低风险快路径；其余只问一次明确动作 |
| macOS 首次 browser-runtime 启动 | 既有审核通过后授权这个浏览器启动 | 一次明确 browser-runtime 能力确认 |
| 复用已证明的受控 view | 不重新索取启动／外部主机能力 | 不重新索取已建立的 view 运行环境能力；仍审核动作 |
| 真正暂停后恢复 | 仅可信人类决定解除；不用切 ask | 同一可信恢复流程，不再重复工具确认 |
| 审核 deny、静态拒绝或强制 sandbox enforce 不满足 | 拒绝 | 拒绝，不能展示一个不可兑现的允许选项 |
| 审核失败、环境不明 | 有界只读诊断后报告不可执行 | 可表达的原有能力问题按既有规则询问；不能把未知当已证明 |

“零重复能力提示”不是“零审核”。正常 click 可能有外部写入效果，不能仅凭按钮标签或“这是 Agent 自己开的浏览器”作固定 allow。

#### 11.2 Launch 与 reuse 要由宿主区分

当前 `controlcap.BrowserLaunch` 把所有 `browser open` 都视作可能启动，包括已复用的 view。新增宿主生成的 `launch_required` 和 `runtime_attested` 事实，只有前者触发启动能力申请；缺少证明时保留旧的保守路径。

这些事实不放进模型可填写的 schema。`host_control=true` 只表达真实外部目标的单次权限请求，不能把运行环境不满足修成“归属已恢复”。

#### 11.3 ask 的一次批准不能再被悄悄改成不可用的环境

先计算这次调用可实现的执行环境，再构建审批选项。对已证明的受控 macOS view，审核器看到 `browser_runtime` 和 `can_outer_sandbox=false`；若动作本身可按任务允许，则返回 allow／confirm，而不是机械地要求无法实现的 contain。

保留 `Contain` 的严格含义：现有 contain 表示必须收容，不因用户按一次“允许”就改成裸执行。若仍要求外层 OS 收容且该 view 不支持，审批前即返回明确拒绝，不再展示一个注定失败的“允许”。本次不新增“忽略 contain”选项。

批准后执行器必须携带同一 call digest、目标证据和已批准的能力集合；不得遗失已批准的 browser-runtime 事实，也不得在门禁合并时偷偷再加 `Sandbox=true`。执行前只复查身份、代际和证据；若发生真实变化，返回 `not_dispatched`，重新规划，而不是重复弹出相同批准框。

### 12. 自动恢复只修定位，不盲目重放副作用

| 状态 | 自动处理上限 | 禁止行为 |
|---|---|---|
| `stale_ref`／目标变化，且已证明未发送 | 本次任务对同一逻辑动作最多一次重新观察、重新规划和重新审核 | 在执行器内部用旧授权换目标执行 |
| locator 歧义／没有匹配 | 最多一次只读收窄；仍不唯一就报告具体缺口 | 依次点击候选“试试看” |
| 普通加载未完成 | 单次等待最多 10 秒；后续仅按任务合理规划 | 固定刷新、无上限轮询 |
| `browser_busy` | 最多等待一次稳定点，5 秒 | 以人工输入身份抢占 Agent 动作 |
| `browser_paused` | 仅登记／更新一个待决介入请求 | 自动 resume、反复 snapshot 探测挑战、重开实例 |
| 明确 429／访问限制 | 保留状态并显示限流信息 | 更换代理／profile、忽略冷却期限重试 |
| timeout／transport failure／`dispatched_unknown` | 只读确认当前状态，不能证明则停止该副作用步骤 | 自动重复提交、删除、发送、支付或授权 |

自动修定位后的调用是新调用，新 ref、目标证据和门禁决定必须重新建立；旧结果里一句“未发送”只有通过可信执行阶段验证才可作为恢复依据。`eaf1e0e` 的保守阶段判定继续适用。

### 13. 增加 host 能力，而不是把安全逻辑藏在提示词里

以下为拟议的增量协议，实施时需要同时修改 kitty-browse 与 Golder，并发布可锁定的新依赖版本。本文不预先指定尚未发布的版本号。

| 能力 | 通道 | 契约 |
|---|---|---|
| `interaction.status.v2` | 普通只读 automation | 返回第 7 节的非敏感状态和当前 epoch／generation |
| `ContinueAutomationChecked` | 私有 operator／管理 API，非模型 schema | 输入确切 view、epoch、pause generation、RequestID、一次性人的决定证明；原子验证后恢复 |
| `ContinuationOutcome` 查询 | 私有 operator／管理 API，只读 | 按 RequestID 与 epoch 返回 `not_consumed`／`resumed`／`stale`／`unknown`，用于超时后的结果核查 |
| `ViewAttestation` | 私有管理 API | 认证 conversation 与 view 的归属、运行环境和 policy，不下发到模型 |
| `InteractionChanged` | 认证的本地事件或有界状态订阅 | 通知暂停／恢复／关闭；不得用网页文本充当事件 |

`ContinueAutomationChecked` 的校验与 generation 更新必须在 host 的同一交互状态临界区完成。动作调度器在分派前复查 pause 状态，避免“确认旧暂停→新暂停→旧恢复仍放行”的竞态。锁内不做网络或页面 I/O。

暂定保留 `/v1` envelope，并以 info capability 明确协商增量契约；上表名称是提案名称。没有能力广告的 host 不发送新字段后假定成功。若需要改变旧动作语义，上游必须发布新 capability，而不是仅靠同名方法存在。

认证复用现有每 view 的私有 operator capability，而不是新建一个模型可见的 bearer token。前端从真实 UI 事件构造 `ContinuationDecision`，私有请求中同时携带 conversation、view、epoch、pause generation、RequestID 和 `continue_automation` 操作。host 先验证认证通道所绑定的 view，再验证请求字段；同一 Unix 用户或公开 socket 连接不等于 operator 来源。

host 保存已消费 RequestID 的结果回执，每个 view 最多 64 条、保留 10 分钟，关闭 view 后删除。重复的同一 ID 不再执行恢复；只读查询可以返回已保存结果。回执被淘汰或 broker 重启后返回 unknown／epoch 失配，不据此重新消费旧请求。客户端确认请求的 5 分钟期限与回执期限分别计时。

证明与恢复决定均通过认证私有连接交换。若上游需要跨连接传递证明，使用 broker 私有密钥对绑定字段和客户端随机挑战作 MAC，并在接收端校验；密钥不得放入仓库元数据或模型上下文。普通 `/v1/info` 的字符串与布尔值不能替代这条认证链。

Golder 内部可定义以下类型化 seam；它们不暴露给 Agent 工具：

```go
type ContinuationHost interface {
    InteractionState(context.Context, BoundView) (InteractionSnapshot, error)
    ContinueChecked(context.Context, ContinuationDecision) (ContinuationOutcome, error)
    ContinuationOutcome(context.Context, BoundRequest) (ContinuationOutcome, error)
    AttestView(context.Context, ViewChallenge) (ViewAttestation, error)
}
```

人工输入仍使用 `BrowserOperator`／`RemoteView` 的私有能力。模型的 click／fill 不得借此绕过 automation pause。GUI 按钮、TUI 状态卡、pane 状态栏按钮和辅助 Ctrl+G 使用相同的 host 校验逻辑，不维护第二份暂停真相。

每个 view 最多一条状态订阅。没有事件流的兼容实现可每秒读取一次轻量 interaction state，错误后退避到 2／4／8 秒，并在三次连续失败后显示连接不可用；不把这些读取替换成 locator snapshot 或页面刷新。关闭 controller／view 必须取消订阅和待决 goroutine；状态变更必须有递增序号，丢失事件后可按完整状态重新同步。

## Error Handling / 错误与诊断

### 14. 错误告诉调用方下一步，而不再统一要求切模式

| 错误码 | 条件 | 处理 |
|---|---|---|
| `browser_continuation_required` | 已暂停且没有可信确认 | 展示确切 pending 请求；提示本地一次恢复，不泛化为切 ask |
| `browser_continuation_expired` | 请求过期 | 重新读取同一 view 状态，更新请求；不发送输入 |
| `browser_pause_changed` | 确认对应的代际已失效 | 撤销旧请求；显示新原因，不自动消费新暂停 |
| `browser_instance_changed` | view／进程 epoch 变化 | 不复用旧许可；重新建立会话 view |
| `browser_busy` | 稳定点等待超时 | 保留状态，不重放动作 |
| `browser_ownership_unverified` | 无法证明受控归属 | 检查私有重连链路，不诱导 host-control 绕行 |
| `browser_not_owned` | 已知是真正外部目标 | 进入现有确切外部授权流程 |
| `browser_socket_mismatch`／`browser_policy_changed` | 身份或政策变化 | 旧授权失效，禁止重新解释成外部控制请求 |
| `browser_outer_sandbox_unavailable` | 审核或 enforce 必须外层收容，但做不到 | 明确环境不匹配；不声称用户没授权 |
| `browser_runtime_capability_missing` | 旧 host 缺少条件恢复／证明能力 | 使用已支持的本地 operator 恢复，或提示升级；不轮询失败动作 |
| `browser_target_changed`／`not_found`／`ambiguous_locator` | 已证明执行前失败 | 按第 12 节有界重新观察 |
| `browser_action_outcome_unknown` | 已分派但结果无法证明 | 只读核查，不自动重试副作用 |

结果同时携带 `stage`（prepare／review／authorization／dispatch）、`execution_state`、`recovery`（observe／continue／select_target／upgrade_host／stop）等非敏感枚举。它们由宿主填充，不接受模型或网页覆盖。

日志和状态统计只记录请求关联 ID、模式、原因、目标身份摘要、执行环境、阶段及结果。禁止记录输入文本、密码、Cookie、operator token、授权 URL 查询参数和完整页面快照。记录同一请求是否被展示／确认／消费，便于判断“用户已经确认为何还被问”。

## Rationale / 理由与取舍

### 15. 不把 resume 改成 auto 无条件允许

这是最短的补丁，也是错误的补丁。它会让模型在用户主动暂停或真实验证后自行恢复，失去暂停边界。我们选择可信前端确认：普通任务几乎不暂停，真正暂停后由一次人的决定恢复，而不是重复的人机对话。

### 16. 不用聊天全文猜授权，也不靠 profile 名称恢复归属

全文关键词／LLM 意图分类会把网页中的“继续”、旧用户消息、其他会话回复误当成新许可。窄的完整短语加待决请求，既支持用户原有表达习惯，也明确了授权对象和期限。

profile 名称相同只说明共享网站登录身份，不说明拥有同一浏览器控制权。私有 broker 证明增加了协议工作量，但避免了把仓库元数据伪装成能力，或把另一个会话的浏览器接管过来。

### 17. 不通过全局模式切换或 blanket allow 修浏览器

要求用户切 ask 才能恢复，把一个局部动作变成全局配置变更。full-access 更不应该成为正常浏览器任务的前提。我们保留模式语义，只让明确的本地用户决定进入同一个 view 的恢复路径。

固定 allow 所有 click 也不可取。网页动作可能是提交、删除或授权。准确目标证据与运行环境可以减少误判，但不能把页面标签当作授权。

### 18. 接受协议改造与旧 host 降级的成本

仅修改 Golder 可以改善文案和避免主动 pause，却不能原子校验 host 内部的新暂停。完整闭环需要 kitty-browse 的配套变更和发布。旧 host 暂时保留本地恢复路径，比假装支持新契约更可靠。

## Compatibility / 兼容性

### 19. 既有浏览器调用保留，恢复行为逐步迁移

- 普通 browser action、已有 profile 目录、每会话 view 命名不变。不搬移登录数据，不导入个人资料。
- `status`、pause 旧字段保留；新字段只在能力协商成功时使用。
- 旧 host 的确切恢复确认仍可通过既有 operator 操作完成；新 coordinator 不用未校验的公共 resume 模拟新保证。
- 过渡期若旧 host 只支持 pane 内 Ctrl+G，应将其明确标为旧版本的临时兼容入口，而不是新版验收通过的依据。GUI／TUI 仍展示状态卡；缺少私有条件恢复能力时按钮禁用并提示升级，不模拟快捷键、不调用未校验的公共 resume。新方案只有两个版本都具备可点击按钮才可称交付，不能以“有快捷键”替代。
- 命名 profile 初次授权、read-only、sandbox enforce、静态拒绝、外部主机能力规则不被本方案放宽。
- 普通用户消息不受影响。仅在当前已有且明确展示的单一介入请求下，完整短语才会被作为恢复确认消费。
- Headless／无交互前端没有人类决定来源。遇到真实人工阻塞返回结构化 blocked；不自动恢复。不得因为启用了 auto 就假定有人确认。
- 手机远控协议不新增恢复授权；继续遵循本机能力边界。
- 每个会话隔离暂停与恢复。共享 profile 的站点登出或账号变化仍可能影响其他 view，这属于既有网站身份共享，不隐瞒为完全隔离。

## Implementation / 实现与过渡

### 20. 先建立状态与恢复契约，再减少工作流打断

| 阶段 | 主要改动 | 交付条件 |
|---|---|---|
| P0：固定回归基线 | 为用户报告的 pause→已授权→继续链路建立失败测试；区分发布版与 master | 可以稳定复现 auto 恢复硬拒绝与旧归属误诊 |
| P1：host 增量能力 | kitty-browse 的状态代际、条件恢复、私有证明、事件通知；更新 Ctrl+G | 不依赖 Golder 即可测试 CAS、一次消费与多 view 隔离 |
| P2：可信恢复服务 | `internal/browsercontinuation`、operator adapter、typed user decision、绑定失效 | auto／ask 都能一次确认，模型无确认凭证 |
| P3：GUI／TUI 接入 | GUI 抽屉按钮、TUI 主会话状态卡及 pane 状态栏按钮、辅助命令和聊天确认、状态事件 | 两个版本均可一次点击恢复；普通流程零接管；必要介入后无需切模式或重复确认 |
| P4：归属与审核整理 | broker attestation 恢复、launch／reuse 事实、错误 taxonomy、能力合并回归 | 人工登录不变 external，批准后不暗中更换环境 |
| P5：自动化策略与验收 | 最少暂停提示、恢复预算、真实浏览器及明确授权的飞书任务 | 符合第 22 节验收矩阵后才称完整修复 |

不以“先发布关闭保护的临时补丁”缩短 P1。P2 和 P3 可以使用 fake host 开发，但只有 P1 发布且 Golder 锁定新依赖后才开放新版恢复路径。

### 21. 修改范围保持在浏览器边界，通用工具审批不放宽

| 文件／模块 | 计划职责 |
|---|---|
| kitty-browse `browserhost`、TUI pane 状态栏及交互调度 | 权威状态、条件恢复、私有证明、pane 可点击恢复按钮与辅助 Ctrl+G 统一实现 |
| `internal/browserctl` | 新状态及错误解码、能力协商、保留未知执行结果 |
| `internal/browsercontinuation`（新增） | 待决请求生命周期、一次性确认、过期／取消、结果核查 |
| `internal/agentcore` | 必要的可信来源／绑定类型；不把 capability 加进模型参数 |
| `internal/runtime` 及 GUI／TUI run 调度入口 | 等待浏览器的任务意图、一次接续 turn、取消与通知的竞态处理；不重放旧动作 |
| `internal/agenttool/browser_profile.go`、`browser_tool.go`、`browser_control.go`、`browser_session.go`、`control_guard.go` | 最少暂停规则、resume 兼容、受控证明恢复、launch／reuse 分离 |
| `internal/agenttool/browser_review.go` | 暂停时不探测 locator，恢复后重新获取证据 |
| `internal/judge/consent.go`、`executionfacts.go`、`containment.go`、`llm.go` | 审核实际环境、正确能力传递、原因明确；不降低 deny／contain 约束 |
| `gui/appsession/browseroperator.go`、`browser_shared.go`、`browsersurface.go`、`approvals.go` | 类型化私有恢复 adapter、会话事件与前端来源校验 |
| `gui/mygoui/browserpreview.go`、composer 接入 | 暂停状态小型展示、一次确认、用户回复的待决绑定 |
| `internal/cli/tui` 及 CLI 交互入口 | 暂停状态卡、按钮绘制及鼠标命中、本地命令／确认、独立于 Agent run 的事件处理；复用现有组件，不重构 TUI 主循环 |
| 相关浏览器文档与工具说明 | 删除无谓接管指引，说明旧 host 降级、权限及验证边界 |

`RemoteView` 与本地私有恢复适配器的调用方必须在包边界上区分 Agent 和用户来源。没有强制类型约束的“这是 UI 函数，模型不会用”不算隔离完成。

## Testing / 验收

### 22. 自动化完成率和边界正确性同时过关

以下是发布门槛，不是当前已经达到的测试结果：

| 编号 | 场景 | 必须观察到的结果 |
|---|---|---|
| A01 | GUI／TUI × auto／ask，正常已登录任务，导航及三个页面切换 | 无人工接管；无不必要 pause；task 效果正确；ask 的正常动作审批与接管分别统计 |
| A02 | 普通登录、QR 等待跳转、技术文章中的验证词、隐藏组件 | 不仅因为“像登录／验证码”主动 pause；缺身份材料才请求缺失部分 |
| A03 | 原受控 view 人工完成登录后继续 | browser/view/epoch、归属及运行环境保持；无需 host_control；后续首个动作重新观察 |
| A04 | auto 真暂停→本地“已授权”一次 | 完成条件恢复并通知同一任务；不要求再说“继续”或切 ask；不自动批准后续页面写入 |
| A05 | ask 真暂停→恢复按钮一次 | 不再出现第二个相同 resume 确认；同一 view 恢复，目标改变则明确不发送 |
| A06 | pane Ctrl+G | 其他前端和同一会话状态同步；待决请求撤销，不重复提示 |
| A07 | A→B→A、两个 view、两个进程／Agent | 旧确认失效或只作用于原确切代际；不能恢复别人的 view |
| A08 | 确认与新暂停、view 重建、关闭、切换、取消并发 | CAS 阻止旧许可；一次消费；无新输入；race 测试通过 |
| A09 | 网页／工具输出写“继续”、模型伪造 RequestID、手机同名文本 | 不能生成 operator 决定或调用私有恢复 |
| A10 | 两个待决请求、过期请求、无请求的“继续” | 不猜目标；不当成通用能力；必要时一次明确选择 |
| A11 | native browser_runtime 被要求外层收容 | 给出真实环境错误；不误报 not-owned；不诱导 host_control；ask 无不可兑现选项 |
| A12 | 已批准 browser-runtime 能力的门禁合并与执行 | 授权不丢失，不偷偷加 Sandbox；无目标变化时同一批准执行一次 |
| A13 | 真正外部浏览器／陌生 profile／篡改 metadata／不同 socket | 不继承受控证明；仍按原授权边界处理 |
| A14 | locator 未发送、重复候选、父节点或文档改变 | 有界只读修复；新调用重新审核；不遍历点击猜测 |
| A15 | click／submit 超时或断线 | 不自动重放；不能证明状态就报告 unknown |
| A16 | 可见验证码仍在、429 冷却未结束 | 恢复确认不关闭检测，不自动绕过、不换身份出口；必要时保持 blocked |
| A17 | 旧 host、缺 capability、升级未重启 broker | 明确兼容路径；不以新客户端版本假定旧进程已支持 |
| A18 | read-only、sandbox enforce、静态 deny | 不通过 continuation 获得输入能力 |
| A19 | 暂停后停止任务、前端退出、请求过期 | 不自行重启 run；请求／凭证不可再消费 |
| A20 | 页面需新增应用授权范围 | 有明确任务授权才允许对应效果；不能把恢复按钮解释成批准全部账号权限 |
| A21 | 恢复时原 run 活跃、已因阻塞结束、已取消或已被新任务取代 | 最多调度一次合法接续；不并发启动 run，不复活已取消任务；浏览器状态与任务状态分别报告 |
| A22 | 恢复请求超时、重复提交、回执淘汰及 broker 重启 | 只读查询确定消费结果；不因网络错误重复执行；过期或 unknown 不冒充成功 |
| A23 | GUI、TUI 主会话及 pane 暂停栏的恢复按钮 | 均可见、可点击并有提交／成功／失败反馈；不能用快捷键代替按钮验收 |
| A24 | Ctrl+G 被终端／复用器拦截，或终端未开启鼠标 | 可点击按钮不依赖快捷键注入；无鼠标时键盘聚焦激活或本地命令仍可恢复 |
| A25 | 终端 resize／缩放、按下释放间目标变化、双击、两入口同时点击 | 无错位命中、无网页点击穿透；旧目标取消；一次恢复、最多一次合法接续 |

测试分层：

1. 单元／race：coordinator 一次消费、过期、source 校验、状态机、proof 校验、能力合并与错误分类。
2. 协议 fixture：真实 `PrepareCall → ReviewEvidence → ConsentGate → Execute`，覆盖 auto／ask 的允许、拒绝、contain、review failure，不能只 mock 最后一层返回成功。
3. 本地真实 Chromium：使用现有安装或共享缓存中的测试运行时，普通登录 Cookie、模拟 OAuth 跳转、假挑战和明确 429；同一 broker 下两个会话 view；人工输入来源通过可信测试驱动注入，不在 Agent schema 增加后门。
4. 前端集成：GUI、TUI 主会话和 pane 状态栏的真实按钮点击；TUI 键盘聚焦激活、命令、辅助 Ctrl+G、聊天单行确认，以及对运行中／已取消 run 的正确通知。必须额外覆盖鼠标／快捷键冲突和布局变化。PTY 不可用时报告未测，不用单元测试冒充 PTY 验收。
5. 飞书人工授权的测试任务：仅在用户明确指定账号、任务和操作范围后验证；记录各次切换的最终状态，不把登录成功或截图当成业务操作成功。

确定性 fixture 的目标：普通工作流人工接管次数为 0；必要介入每个暂停代际最多一次人类恢复确认；正常路径不需要 `/permissions` 切换；未知副作用自动重放次数为 0；跨会话或跨代际错误恢复次数为 0。真实站点只报告实测次数、失败阶段和耗时，不承诺验证码自动通过。

### 23. 本机预检与真实链路分别记录

实施后运行格式、构建、vet、全量 race 及 Linux ARM64 交叉编译；保持用户当前要求的 `Linux preflight: suspended`，不自行启动容器。真实 Chromium／PTY／飞书测试使用显式 opt-in，报告是否运行、实际 host 版本及 broker 是否重启。

本文只有方案，没有执行新的真实浏览器或飞书操作，也没有把未来测试写成已通过。

## Rollout / 发布、回退与风险

### 24. 只在能力齐备时启用，不默默降级保护

- 开发分支默认跑完整 fake-host 矩阵；新恢复协议在 host capability 协商成功后启用。
- kitty-browse 先发布契约及测试，再在 Golder 更新 `go.mod`／`go.sum`。发布说明同时写明客户端与正在运行的 broker 版本要求。
- 旧进程的 Cookie 不迁移；需要升级 broker 时先等现有 view 正常退出，不为升级而杀另一会话的浏览器。
- 回退时禁用新 coordinator 的能力协商路径、撤销内存中的未消费请求，恢复旧 operator 快捷键入口；不能将不完整证明视作有效，也不能恢复已撤销的令牌。
- 对正常任务统计 pause 次数、人工接管次数、每代际确认次数、归属错误分类及未发送／未知执行结果；不收集页面和输入内容。先用可控 fixture 验收，再以用户授权的真实任务评估可用性。

| 风险 | 影响 | 处理 |
|---|---|---|
| 前端确认来源混淆 | 模型或网页伪造人类恢复 | 类型化事件、窄入口、完整短语、来源与目标绑定、负向测试 |
| pause generation 与 input 调度竞态 | 旧确认放行新暂停 | host 原子校验、调度前复查、epoch 及一次消费 |
| 归属证明依赖元数据 | 误接管其他会话／用户浏览器 | 元数据仅定位；私有 broker 证明；失配失败封闭 |
| 过度收紧导致 ask 仍不可用 | 反复批准但无法执行 | 在审批前计算可执行环境；准确证据；不可执行不弹虚假允许 |
| 站点挑战误判 | 正常页面被暂停 | 本地正反例、明确检测来源；不对真实挑战放宽边界 |
| 多 view 共享站点身份 | 一处登录／退出影响另一处 | 保留既有共享语义并展示；不宣称网站身份隔离 |
| 升级后仍连接旧 broker | 用户误以为新闭环已支持 | capability 和实际运行版本诊断；明确一次兼容提示 |

## References / 参考与评审重点

- [已有目标审核与环境修复](browser-review-evidence.md)
- [现有会话绑定](browser-binding.md)
- [共享 profile 与暂停规则](browser-sharing.md)
- [macOS 受控浏览器运行环境](browser-runtime.md)
- [输入兼容性与最少人工操作的前期方案](browser-compatibility-automation-design.md)
- [人工输入回归的实际验收方式](browser-manual-input.md)

评审时优先看第 8～11 节：确认来源是否足够严格、用户是否确实只需确认一次、归属是否与暂停解耦，以及 ask 的批准是否能在真实环境兑现。上述方案没有依赖一个“暂时允许所有 browser 调用”的开关。
