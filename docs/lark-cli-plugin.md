# 用标准 MCP 插件接入飞书协作与飞书项目，并通过内置浏览器扫码授权

Status: Implemented locally; real-account and Linux runtime acceptance pending
Last updated: 2026-10-28
Discussion: 本次设计讨论；用户确认工单指飞书项目工作项，默认用户身份，不自动安装，优先内置浏览器扫码。
Related: [extensions-mcp.md](extensions-mcp.md), [extensions-state-setup.md](extensions-state-setup.md)

## 实现记录（优先于下文原始提案细节）

代码集中在 [`plugins/lark/`](../plugins/lark/README.md)，入口为
`plugins/lark/cmd/golder-lark`；宿主通用能力仍在现有 extensions/seatbelt/gate/UI 包。
已提供 22 个 MCP 工具、可选随包 Skill、私有状态、命名 setup variant、依赖指纹、
精确人类确认与命令长尾限制。实际配置以 `golder-lark print-config` 生成结果为准。

根据用户已有应用的情况，首版只复用应用，以 `--app-secret-stdin` 配置，不执行 `--new`。
默认授权协作，`setup lark project` 单独授权项目。内置浏览器显示宿主生成的离线二维码页，
不创建浏览器控制 socket；扫码后在手机的官方页面授权。这替代原提案的通用桌面 OAuth 登录页，
避免桌面页面重定向、cookies 与模型控制的隔离难题。不可启动浏览器时显示同一 URL/终端二维码。
`skills` 是统一来源的可选本地目录声明，纳入指纹，不要求另行手动注册独立 Skill。

已用本地 fixture 验证协议和认证编排；没有使用真实账号扫码，也没有改远端业务数据。
下文保留需求与设计依据；与上述收敛有差异时，以实现 README 为准。

## Abstract / 摘要

我们交付一个标准 MCP stdio 插件 `golder-lark`，内部包装两个官方 CLI：
`lark-cli` 操作文档、群聊/消息、Sheets 和 Base；`meegle` 操作飞书项目的需求、缺陷、任务等工作项。
所有业务仍经现有扩展目录、策略、信任和沙箱，不新增 `runtime=lark`，不让插件裸跑。

默认用户身份。用户点击一次「授权」，宿主用内置浏览器显示官方扫码页；扫码后插件自动校验登录，
凭据保留在该源自己的私有 HOME，后续在服务商允许的期限内刷新。两种服务的授权分别管理，
不假设同一个飞书扫码能同时授予协作和项目全部权限。

本文是设计，不是已交付功能。新增的持久状态与 setup 契约见通用提案；浏览器弹出、刷新和真实写入
仍需实现及 macOS/Linux 实测。只做单机按需检索，不做向量、全量同步或后台工作流。

## Background / 背景：工单是飞书项目，不是审批

上一版把工单映射到了 `approval tasks`，这是错误的。此版删除审批工具与审批权限要求。
官方 `larksuite/cli` 将项目管理指向独立的 `larksuite/meegle-cli`；后者提供 `workitem query/get/update`、
工作流状态/节点流转和用户 Device Code 登录。协作用户令牌不能直接当作项目令牌。

我们还修正两条过度承诺：

- HOME 隔离不等于系统钥匙串隔离。macOS 上 `keychain-downgrade` 会尝试读取全局主密钥，
  在钥匙串被阻止的沙箱内本来就可能失败；不应自动运行它。
- 当前宿主只对顶层 command 做指纹，不能声称 helper 调用的 `lark-cli` 已自动纳入信任。
  依赖必须在用户 Review 前声明、解析和 hash，不能由插件启动后上报路径换取放行。

## Design / 设计

### 1. 一个插件，两个后端，分别诊断

```text
用户 / Agent
     │ 标准 MCP tools + 现有 gate
Extension Service ── stdio ── golder-lark（沙箱中）
                                ├── lark-cli → 飞书文档 / IM / Sheets / Base
                                └── meegle   → 飞书项目工作项 / 工作流

用户点击「授权」 → 通用 setup executor（同源沙箱 / 同源 HOME）
                       ├── CLI 发起 Device Code，插件持有 device_code 与轮询
                       └── URL 事件 → 宿主内置浏览器 → 用户扫码确认
                                          ↓
                          CLI 落盘、服务端验证成功 → UI 显示已登录
```

`golder-lark` 的初始化只声明固定工具目录，不自动登录、不读取全部文档或启动业务请求。
没有 `meegle` 时只有项目工具返回缺依赖，文档/消息/表格仍可使用；反之亦然。
`status` 的 `services.lark` / `services.project` 分别记录 installed/configured/authenticated/error。
来源的 MCP `ready` 只表示连接可用，不等于两个服务都已登录。

### 2. 配置继续属于标准 MCP，业务知识留在插件

以下是**拟议的新版本配置**，不是当前宿主已支持的示例。路径按安装位置修改；
`executables` 的审查语义是对用户已接受的 setup 方案的补充，需随实现评审。

```toml
[mcp.servers.lark]
enabled = false
transport = "stdio"
command = "/opt/tools/golder-lark"
state_dir = true
setup = ["/opt/tools/golder-lark", "setup", "--events"]
executables = ["/opt/tools/lark-cli", "/opt/tools/meegle"]
network = true
call_timeout = "120s"

[mcp.servers.lark.env]
LARK_MCP_CLI = "/opt/tools/lark-cli"
LARK_MCP_PROJECT_CLI = "/opt/tools/meegle"
LARK_MCP_IDENTITY = "user"
MEEGLE_HOST = "project.feishu.cn"
MEEGLE_NO_UPDATE_CHECK = "1"
MEEGLE_AI_HANDOFF = "disabled"
LARKSUITE_CLI_NO_UPDATE_NOTIFIER = "1"
LARKSUITE_CLI_NO_SKILLS_NOTIFIER = "1"
```

`LARK_MCP_*` 是传给插件的非敏感包专用设置，拟实现；不是新增宿主权限或上游 CLI 环境变量。
不使用宿主保留的 `GOLDER_*`。程序直接从私有 HOME 推导配置目录，不新增 `{{state}}` 模板语言；
所有 CLI 依赖用绝对路径执行。例子假定两个依赖都已安装；只用协作可不声明 meegle。

工具注册/运行使用 `golder mcp add/enable/test`、`/mcp connect` 等既有入口；新增：

```text
golder mcp setup lark      # 用户配置 / 授权；可选协作或项目
/mcp setup lark            # TUI 同义；详情页的「授权」按钮指向这里
golder mcp reset lark      # 确认后清除此源本地状态
```

不增加 `golder lark` 或单独 Lark runtime。模型业务工具返回 `AUTH_REQUIRED` 后只提示点击授权；
不擅自弹窗、改身份、扩大 scope。用户明确说“帮我登录”时，引导到同一授权入口。

### 3. 授权体验是“点一下、扫码、自动完成”，不是复制 token

详情页建议展示：

```text
Lark                    [MCP · stdio] enabled / approved
  飞书协作              未登录   [授权：文档、消息、表格]
  飞书项目              未登录   [授权：项目工作项]
  操作身份              用户本人（不自动切换机器人）
```

用户点击对应按钮后：

1. 用户审阅程序、服务商/租户、权限目的；宿主授权一个有限时长的 setup 会话。
2. setup 取得官方 verification URL，立即发出结构化事件；UI 不等模型整轮结束。
3. 宿主复用现有 browser launcher / BrowserTool 的授权链，打开独立的敏感授权实例，
   显示“请用飞书扫码并确认权限”。网站如要求账号选择、MFA 或管理员批准，交给用户操作。
4. 插件按服务商 interval 轮询。用户扫码成功后，CLI 保存令牌并验证身份，UI 自动显示“已授权”。
   不要求用户回来贴 device_code 或输入“我已完成”。
5. 到期、拒绝或取消停止轮询，只关闭本次 owned 授权页；不自动重发新的授权请求。

浏览器只显示页面，不提取 cookies/token，不替用户点击同意。插件拿不到浏览器 socket；
授权页面不对模型开放 snapshot/network/截图。此 sensitive 实例隔离是需要补齐的宿主接线，
不能直接把现在 agent 可控制的普通浏览器页拿来当密码输入页。

浏览器不具备启动许可、缺少可用 Chromium 或 SSH 无法展示时，退回同一官方 URL + 二维码，
手机扫码仍由 Device Code 完成。不开沙箱后门、不自动装浏览器、不依赖本地回调端口穿透。

#### 3.1 飞书协作：应用配置和用户授权是两件事

如果已为此私有安装配置应用，直接进入用户扫码；否则 setup 先引导：

```sh
lark-cli config init --new
```

该命令输出配置 URL 并等待浏览器完成。插件对锁定版本做专用输出适配，提取可信 URL 后交给宿主，
不让 CLI 打开系统浏览器，不假造不存在的 `config init --no-wait`。
“创建新应用”要向用户说明；已有企业应用可通过私有配置或 `${credential:...}` 引用接入，
secret 不经过对话框明文、工具参数或日志。无创建/授权资格时说明需管理员处理。

配置完成后，按本次功能请求最小权限，不用 `--recommend` / `--domain all`：

```sh
lark-cli auth login --domain docs,drive,im,sheets,base --no-wait --json
lark-cli auth login --device-code <由插件保存的device_code> --json
lark-cli auth status --json --verify
```

域名是当前源码中的业务域写法；实际发布锁定 CLI 版本，并核对 `auth login --help`。
如某版本不接受这些域，使用该版本已核实的 scope 映射，不靠猜 flag 重试。
第二条由后台 setup 在 URL 已展示后执行；CLI 自带轮询，不把原始 JSON 中的秘密传给模型。
新增权限时再次扫码/确认；token 刷新不等于管理员批准或用户新授权。

#### 3.2 飞书项目：单独的 Meegle 用户授权

已核对 `meegle` 源码，支持拆分 init/poll（不是猜测它支持 `--no-wait`）：

```sh
meegle auth login --host project.feishu.cn --device-code --phase init --format json
meegle auth login --host project.feishu.cn --device-code --phase poll \
  --device-code-value <内部device_code> --client-id <内部client_id> --once --format json
meegle auth status --format json
```

init 返回 verification_uri_complete、expires_in、interval。setup 使用该原始 URL 打开内置浏览器，
按 interval 调 poll --once；pending 不是错误，slow_down 需要延后，ok 后再校验 auth status。
私有租户使用用户明确配置的 HTTPS host，不从工单正文或任意链接自动切换授权站点。

`meegle auth status` 退出码 1 表示本地无 token 或被服务器拒绝；退出码 2 表示网络/服务器问题，
不能将后者误报成“请重新扫码”。仅装 `lark-cli` 或仅完成协作授权不代表项目已可用。

### 4. 登录态用独立存储，不能靠导出全局钥匙串

用户已接受 `state_dir`，不再把“是否接受”列为开放问题。每个源一个 HOME，内部再分两种服务：

```text
$GOLDER_HOME/extensions/mcp-lark/home/          0700
  .lark-cli/                                  协作配置、用户登录信息
  Library/Application Support/lark-cli/        macOS 协作加密文件
  .local/share/lark-cli/                       Linux 协作加密文件
  .meegle/                                    项目配置与加密登录文件
```

**文件加密不等于硬件密钥保护**。密钥也在同一源目录里时，保护来自 0700/0600、来源沙箱和不泄露日志；
可信插件进程本身能够使用它们，不能声称对恶意插件或同 UID 的裸进程保密。

| 后端 | 源码事实 | 推荐适配与验收要求 |
|---|---|---|
| `lark-cli` / Linux | AES-GCM 文件 + `master.key` | 私有 HOME 中首次登录，重启和刷新回写均需实测 |
| `lark-cli` / macOS | 本地 `master.key.file` 存在时优先使用，否则会触及系统钥匙串 | 不调用全局 downgrade；对固定版本，在空的私有 store 中由插件以 crypto/rand 创建独立的 32 字节文件密钥（0600/O_EXCL），再交给官方 CLI 管理凭据；已有内容异常则拒绝，绝不覆盖已有 store。属于版本耦合适配，需双平台 P0 测试；上游提供显式 file-store 选项后替换它 |
| `meegle` / 双平台 | 原生 credential store 优先，失败才落文件 | 对固定版本，以绝对路径执行原生 CLI，子进程 PATH 设为专用空目录，使通过 PATH 查找的 `security`/`secret-tool` 不可用，触发官方加密文件 fallback；profile 使用安装专属名。源码支持此路径，但需验证无系统钥匙串读写、无外部 helper 依赖；新版改用绝对路径访问原生 store 时拒绝兼容，优先推动上游公开 file-store 选项 |

这两个文件存储适配只在插件中，不增加宿主读取钥匙串能力，不导出用户既有主密钥。
`meegle` 的文件 KDF 涉及 hostname/USER；setup 与运行必须保持一致，跨机器拷贝失败时要求重新授权，
不能把整个 state 当可移植凭据包。失败时报告具体不兼容，不回退宿主裸执行或降低 DPoP 安全策略。

access token 到期后由各 CLI 在服务商支持的范围内刷新；refresh 被撤销/过期才重新提示扫码。
同一源 setup、刷新和 reset 用锁串行。本地 reset 不声称服务端授权撤销；CLI logout 的实际撤销语义
要按各自版本处理，不能承诺退出终端登录一定不会影响相同应用的服务端授权。

### 5. 高频工具加受控长尾入口，工单全部指项目工作项

模型名称为 `mcp__lark__<name>`。保留 20 个高频工具，增加 `cli_help` / `cli_call` 两个长尾入口，
不把上游几百条命令分别注册成工具。以下为逻辑工具设计，不是允许任意 shell 的万能命令：

| 工具 | 能力 | 后端 |
|---|---|---|
| `status` | 分服务诊断安装、身份、授权；显式调用才验证网络 | 两种 CLI |
| `doc_search` | 按主题/类型/时间搜文档、Wiki、表格资源 | `drive +search` |
| `doc_read` | 读正文、目录、章节、关键词片段 | `docs +fetch` |
| `doc_write` | 创建、追加、局部修改；不默认全文覆盖 | `docs +create/+update` |
| `chat_search` | 搜群名或成员 | `im +chat-search` |
| `message_search` | 按群/发送人/时间搜消息 | `im +messages-search` |
| `chat_history` | 读有限范围群历史 | `im +chat-messages-list` |
| `message_send` | 群或单聊发送；明确展示用户身份 | `im +messages-send --as user` |
| `project_schema` | 解析空间、类型、字段、人员及可用流转信息 | `meegle project search`、`workitem meta-*`、`user search`、`workflow list-*` |
| `ticket_search` | 搜需求/缺陷/任务；默认限定项目空间 | `meegle workitem query`、需要时 `mywork todo` |
| `ticket_get` | 工作项详情/当前字段/状态 | `meegle workitem get` |
| `ticket_update` | 改标题、负责人、优先级、排期等字段 | `meegle workitem update` |
| `ticket_transition` | 合法状态/节点流转，不是审批同意/拒绝 | `meegle workflow transition-state/transition` |
| `sheet_read` | 工作簿信息、区域值、公式/样式 | `sheets +workbook-info/+csv-get/+cells-get` |
| `sheet_write` | typed 写值、追加、公式；保留未修改区域 | `sheets +table-put/+cells-set` |
| `sheet_structure` | 新建子表、显式插入行列；不开放删表/回滚 | `sheets +sheet-create/+dim-insert` |
| `base_resolve` | URL 或标题定位 Base | `base +url-resolve/+title-resolve` |
| `base_schema` | 表和字段类型 | `base +table-list/+field-list` |
| `base_record_search` | 按关键词、结构化 filter、ID 查询投影 | `base +record-search/+record-list/+record-get` |
| `base_record_write` | 增、改、明确 ID 的删除 | `base +record-batch-create/+record-batch-update/+record-delete` |
| `cli_help` | 按服务和命令路径查帮助/Schema，结果有界 | 固定 CLI 的 `--help` / `schema` / `inspect` |
| `cli_call` | 执行经命令级策略和 gate 允许的长尾业务操作 | 固定 CLI + argv，不支持 shell |

#### 5.1 项目工作项不能硬编码字段或状态

输入示意（不是上游 CLI 原生参数）：

```json
{"project_key":"PROJ","work_item_type":"<查询得到的type-key>","query":"登录失败","assignee":"me","limit":20}
```

写入示意：

```json
{"project_key":"PROJ","work_item_id":"12345","fields":[{"field_key":"name","field_value":"修复登录重定向"}],"confirm":true}
```

处理顺序：

1. 用户给 URL 时先 `meegle url decode`，再用 `project search` 把空间简称解析成权威 project_key；
   同名空间要消歧。已有明确 ID/空间时直达，不强制重复扫描所有空间。
2. 从真实 `workitem get` 取类型；按需查 `meta-fields/meta-roles`。负责人用项目 user_key，
   不拿飞书协作的 open_id 直接代替；优先级/状态从真实选项解析。
3. 搜索把用户筛选条件编译成有限的只读 MQL，正确转义关键词与标识符；不提供任意 CLI/MQL 执行后门。
   分页和 SELECT 字段按发布版本的 inspect/skill 适配，不把示例字段当跨项目常量。
4. 更新只发变化字段。上游 field_value 是字符串协议，数组/对象需 JSON 字符串化；
   不能直接把 Base CellValue 搬过来。标题示例映射为：

```sh
meegle workitem update --project-key PROJ --work-item-id 12345 \
  --params '{"fields":[{"field_key":"name","field_value":"修复登录重定向"}]}' --format json
```

5. 状态流转先查可用目标与必填项，不能普通字段 update 假装已流转；节点流转与状态流转分开。
   角色不足/必填未填就停止，给出当前状态、失败原因、下一步，不强行完成所有节点。
6. 写前展示项目/工作项链接、用户身份、before→after；写后读回目标字段/状态。
   无法确认则报告 submitted/unknown，不自动重放。首版单条修改，不做批量结单/删除工作项。

#### 5.2 Sheets 与 Base 保留在首版范围

- Sheets 先读 workbook-info，明确 sheet-id/name、A1 range、数据类型再写。
  默认只改指定区域或 typed append；编号/前导零用文本，金额/日期/百分比显式 dtype/format。
  公式写后检查 formula 和计算错误；保留样式/已有数据。抽样首中末只证明 sampled，
  不能当全范围 verified；小范围可完整回读，异步计算给 pending。
- Base 先解析 base/table/schema 再读写。record ID、人员/关联字段用真实 ID，
  单选/多选保持类型，不写 formula/lookup 等只读字段。`ignored_fields` 作为部分结果报告。
  单批最多 200 条、同表串行；默认查询 100 条，超过限制返回分页与 partial。
- 删除 Base 记录必须列明 ID 和影响；Sheets 删表/清空/整表回滚、Base 改权限/Workflow/仪表盘
  不提供首版专用包装；长尾也默认阻止破坏性操作、权限管理与 Workflow 启用，
  必须有明确的 operator 命令级授权才可扩展，不能凭模型传 `confirm` 或 raw API 绕过。

#### 5.3 长尾命令按需发现，但权限不能按需绕过

示意参数：

```json
{"service":"lark","command":["sheets","+sheet-info"]}
```

`cli_help` 只允许明确的帮助/本地 Schema 查询路径；返回命令说明、必要参数和输出形态，
不拉整套命令手册，不执行 schema 中的副作用。模型读完才构造 `cli_call` 的 argv：

```json
{"service":"lark","argv":["sheets","+sheet-info","--url","https://example.feishu.cn/sheets/token","--sheet-id","actual-id"]}
```

`service` 只能选择配置中批准的 lark/project 二进制；不能传路径、shell、可执行脚本或任意环境变量。
内容含 `@file`、输出文件、网络目的地或身份切换的参数由包装器校验，不能借 CLI 读宿主路径、
上传凭据、改 cwd 或切身份。相同私有 HOME、沙箱、输出限额和副作用不重试规则继续适用。

**长尾不意味着所有命令默认放开**：

- 安装/更新、自更新、登录/登出、凭据导出、任意 API、任意外部命令和配置修改默认禁止，
  setup 仍只由用户启动。不得把安装器放进长尾入口自动下载。
- 常规长尾业务可按命令族审阅放行；未知命令需人工审阅其参数和影响，不信任上游只读标记作为授权。
  新版本命令只有通过版本检查、参数检查和策略允许后才可执行，不保证无条件自动可用。
- 专用工具与长尾共用同一份业务能力/命令策略。禁止 `message_send` 时，相应发送命令也必须被拒绝；
  禁止 `ticket_update` 时，`meegle workitem update` 也不能从长尾执行。
  当前宿主只有工具名过滤，尚不具备这种语义映射；实现必须补命令级执行校验，
  未补齐前有这种限制的配置禁用 `cli_call`。不能声称现有 deny 自动约束所有等价命令。
- `cli_call` 可整体禁用，用户仍可使用高频工具。未明确授权的破坏性/共享权限变更返回阻止原因，
  不是换 raw API 继续试。

#### 5.4 上下文渐进披露：Skill 正文按需，工具 Schema 目前全量声明

“动态加载”有四件不同的事，不能混为一谈：

| 内容 | 当前宿主行为 | Lark 设计 |
|---|---|---|
| 工具名称、简短 description、input schema | `InstallExtensions/SyncTools` 在轮次边界把全部可用扩展工具加入 `ac.Tools`，随模型请求声明 | 高频工具和两个长尾入口保持精简，但可用的 22 个 Schema 不是命中飞书意图后才出现 |
| Skill 索引 | `FormatSkillsForPrompt` 只写 name/description/location | 一条 lark 工作流索引，描述飞书文档、消息、项目工单、Sheets、Base |
| Skill 正文与分域参考 | 模型通过 `read` 按需加载；非硬编码关键词路由 | 命中任务后读 SKILL.md，进入项目/表格/长尾阶段才读对应 reference |
| 全量 CLI 帮助/复杂参数 | 不预注入 | `cli_help` 按当前命令返回，避免把上游几百条手册塞进上下文 |

MCP server instructions 不自动变系统提示；MCP prompts 也不是 Skill，仍是用户选择后预览/附加，
不能拿它们暗中注入一整套飞书规则。目录缓存减少探测进程，不等于工具声明没有上下文成本。
Skill 正文读入后属于会话内容，之后按宿主正常压缩/预算管理，不承诺下一轮自动卸载。

自动调用链路：

```text
用户：“查一下项目 A 没解决的登录缺陷”
  → 模型已看到简短 lark Skill 索引 + ticket_search 等工具定义
  → 模型按语义匹配，read lark/SKILL.md 与项目 reference
  → 发出 mcp__lark__ticket_search 工具调用
  → 宿主 Schema/策略/gate 校验
  → 经允许后，若进程尚未运行，按需启动 MCP server、校验目录版本
  → golder-lark 调 meegle；只返回所需工作项和链接
```

这不是后台监控关键词后启动进程，也不是插件自己要求执行。模型的语义选择可能有误，
必须用清晰描述、示例与评测提高命中率；`/lark`（Skill slash）可作为用户显式入口，
不是另造宿主 Lark 管理命令。

前置条件：来源已启用/信任，完成一次显式 test/connect 并有有效工具目录。
未知 MCP server 在未探测前没有业务工具定义，模型不可能凭空调用；未登录则返回对应授权入口。
禁用/工具 deny/`--no-tools` 继续阻止执行；`--no-skills` 或禁止 read 时不广告自动读 Skill 正文，
但若工具仍可用，模型仍可基于工具定义选择它，二者不是同一开关。

实现上先不做新的“隐藏所有工具 → 意图分类器 → 注入一组工具”框架；它是可独立讨论的宿主通用优化，
不能当当前已有特性。先量测精简 Schema 的 token 成本，再决定是否需要真正的工具级延迟发现。

Skill 安装也需遵守现状：当前 `PackageSkills` 从 manifest 读取包内 skills，并不会从 MCP initialize
自动导入本地 Skill。首版把 lark Skill 随发布包提供，由用户安装到既有技能目录并在 `/skills` 可见；
不假设配置一个 MCP server 就自动执行下载/装 Skill，后续统一包归属可独立优化。

### 6. 写入确认、身份与错误

默认 `LARK_MCP_IDENTITY=user`，Lark 每条命令显式 `--as user`，项目使用自己的用户 OAuth。
协作 user 授权未完成时不悄悄降级 bot；项目缺权限不切另一个账号试探。

`confirm=true` 仅是模型的执行意图，**不是用户授权凭据**。先给用户看具体变更，再走宿主 gate；
需要强人类确认的发送/删除/流转操作在 auto/full-access 下也不能凭模型布尔值声称“用户已同意”。
实现需把 operator confirmation 绑定到目标/正文/字段摘要并在 gate 层验证；未具备该能力时
强确认工具只能走 ask，或在该模式隐藏/拒绝，不能靠 Skill 文案保证。
exit 10 必须停下重新确认，不能静默补 `--yes`；写操作不自动重试。

错误归一化至少包含 service/code/message/execution_state/next_action：

| 情况 | 用户文案/处理 |
|---|---|
| 缺 `lark-cli` / `meegle` | 指出哪个组件未安装，显示官方手动安装方式；其他服务不禁用 |
| 未配置协作应用 | 先完成应用配置，再用户扫码；可能需要管理员 |
| 未登录/refresh 失效 | 显示对应服务「授权」按钮，不自动换身份 |
| scope 不足 | 缺少的 scope/console_url，区分应用开通和用户授权 |
| 项目空间/角色/字段不可写 | 当前用户无该项权限，申请空间/角色/字段授权；不套用协作 scope 文案 |
| 对象不存在或不可见 | “目标不存在或当前身份不可见”，不凭 not found 断言文档一定存在 |
| 工作项已变化 | 刷新当前字段/状态，让用户重新确认；不沿用旧变更 |
| `meegle auth status` 网络失败 | 网络/服务不可达，不要求重新登录 |
| 部分写入/回读不完整 | 列明成功、失败、pending/unknown，保留链接；不重复整批提交 |
| 超时/断线 | 写入结果未知，先核实；读操作才允许有限退避重试 |

两个 CLI 分别解析信封：`lark-cli` 的 `{ok,identity,data|error}` 与 `meegle` 默认数据/错误输出
不是同一种协议，不能共用一个“顶层 ok=true”判断器。日志不记录 token/device_code/正文。
管理 `list/inspect/doctor` 保持无启动无网络；业务 `status` 显式经 gate 才校验服务端。

### 7. 只提示安装，优先直接调用原生二进制

```text
文档/消息/表格： npm install -g @larksuite/cli
飞书项目工作项： npm install -g @lark-project/meegle
```

官方向导也可由用户手动运行，但向导可能同时安装 Skill、配置或打开登录；提示时解释影响。
Golder 本身不执行安装器，不修改 npm prefix/PATH，不下载 Chrome。

npm 的命令可能先启动 JS 包装器，并在原生二进制缺失时自动下载。为守住“不自动安装”：
注册阶段只做文件解析，找到已安装的原生 payload，将它的绝对路径写入配置和 executables；
找不到则只提示安装不执行 wrapper。版本、路径、二进制哈希变化需重新 Review。
不需要每次业务调用启动 Node，不依赖 Node 运行时读取用户全局 HOME。
依赖可装在用户目录，只暴露所选只读文件，不要求用户为此给整个 HOME 加权限。

### 8. Mac / Linux 的共同保证与真实差异

- macOS 保持 Seatbelt，Linux 保持 bwrap；setup 与业务采用相同 state、环境、USER/hostname。
- 两个平台 state 都必须读写，token 刷新不能只读挂载；protected Golder root 仍然封锁其他内容。
- 浏览器走现有 macOS browser-specific 许可，不让 Lark 继承这个例外；Linux 无 bwrap 时失败关闭。
- SSH/headless 优先 Device Code + 二维码/URL；不需要回环端口，不依赖桌面钥匙串解锁。
- Apple Silicon/Intel、Linux amd64/arm64 和上游二进制 ABI 按发布资产验证；不承诺所有 musl 环境可用。

## Rationale / 理由与取舍

| 选择 | 为什么不选另一条路 |
|---|---|
| 标准 MCP + 通用 state/setup | 不做 Lark 专有 runtime，不复制授权框架 |
| 同一个插件包装两个官方 CLI | 用户只管理一个飞书入口；代价是项目需单独安装和授权，明确分状态 |
| 用户扫码，按需双授权 | 不要求用户复制 token，也不假装协作授权涵盖飞书项目 |
| 私有文件登录态 | 多一次授权，换取不挂载全局凭据；密钥同盘的保护边界如实说明 |
| 内置浏览器只做 UI | 比读取 cookie 或自动点击同意更可控；没有浏览器也可继续扫码 |
| 平台搜索 + 按需读取 | 不搞向量、企业级同步或权限缓存，召回差一些但单机边界清楚 |
| 高频薄包装 + 受控长尾 | 保留日常体验和 CLI 覆盖面，不维护所有上游命令的重复包装；代价是长尾需查帮助、逐命令授权 |

## Compatibility / 兼容性

业务 MCP wire 不变；基础扩展规范的 state/setup 采用 opt-in，旧配置保留临时 HOME。
新增依赖审查与 setup 事件是宿主通用能力，明确记录在 [通用提案](extensions-state-setup.md)。
不提供 MCP 自动 OAuth/sampling/elicitation；不把第一方插件当成沙箱豁免。
现有 CLI/MCP/legacy、工具策略、禁用即时撤销和子 Agent 不自动继承规则继续有效。

## Implementation / 实现顺序与验收

1. **认证可行性先行**：锁定两个 CLI 发布版本；验证私有 file store、init/poll、应用配置 URL、
   DPoP、刷新、重启。macOS 不接触用户全局钥匙串，Linux 不依赖桌面会话。
   固定版本行为不满足时先修适配/推动上游选项，不把 token 手工复制作为默认产品体验。
2. **通用能力**：state/依赖指纹/setup executor、受限 URL 事件、浏览器敏感实例、取消/超时/回退。
   先用假 OAuth server 验 pending/slow_down/denied/expired/恶意 URL，再做用户参与的真实扫码。
3. **基础业务**：文档和消息搜索/读取，项目 schema/search/get，Sheets/Base 结构与读取。
4. **写能力**：文档/消息、项目字段修改/流转、Sheets/Base 写入、强确认与结果核验。
5. **长尾与上下文**：cli_help/cli_call、命令级策略和等价操作 deny 校验；精简 Skill 索引与工具描述，
   测试首次语义命中、分域按需读取、缓存目录后首次按需启动以及长尾新命令的审查。

代码拆分：独立 `cmd/golder-lark` + `internal/larkmcp`，其中 `auth_setup`、`lark_runner`、
`project_runner` 分开；MCP server 用官方 MCP SDK，不用 legacy `extension/plugin.Serve` 冒充 MCP。
通用改动只触及 extensions 配置/进程/状态、extensioncmd、TUI 和 browser launcher 的授权展示接线。

验收必须覆盖：

- `/mcp setup lark` 用户选择服务后弹出内置浏览器扫码页，成功自动更新；无浏览器可用同 URL/二维码完成。
- 首次协作需要应用配置时清楚说明额外步骤，扫码不会自动绕过租户管理员限制。
- 协作/项目分开授权、分开失效，已授权协作不代表项目可调用；默认全程 user。
- 重启 golder 后仍可用、过期可刷新，其他来源无法读状态；无权限给具体申请途径。
- 项目工单可搜、查看、修改真实字段、合法流转；没有审批 `approve/reject` 工具。
- Sheets 区域读写/追加、Base 查询增改删均受确认与权限限制，partial/unknown 如实报告。
- `status` 在依赖缺失时给安装指引，但没有 helper 的来源无法启动时由管理诊断给出错误，
  不声称模型总能调用一个尚未连接的 MCP 工具。
- 不自动下载/安装、不向模型暴露二维码授权会话的控制端点、不会把 pending 当失败重发。
- 高频工具未覆盖的普通命令可先 cli_help 后 cli_call；安装/认证/任意 API 等受阻，
  禁用专用写能力不能从 cli_call 绕过。Skill 全文不预注入，工具 Schema 的当前全量行为如实计量。

## Appendix / 已核对的依据与待实测项

2026-10-28 阅读官方源码快照（不是实际扫码验收；正式实现应记录具体 release/hash）：

- [larksuite/cli](https://github.com/larksuite/cli)：`cmd/auth/login.go` 的 `--no-wait --json` / `--device-code`，
  `cmd/config/init.go` 的配置 URL 流程，`internal/keychain` 的 file-first 逻辑与 downgrade 限制。
- [larksuite/meegle-cli](https://github.com/larksuite/meegle-cli)：`commands/auth.go` 的 `--phase init/poll --once`，
  `auth/token_store.go` / `fallback_store.go` / `keychain_store.go`，官方 workitem/update skill 的字段协议。
- 本仓库 `internal/extensions/process.go` / `config.go`、`internal/agenttool/browser_tool.go`、
  [browser-runtime.md](browser-runtime.md)：私有 HOME、依赖指纹现状、浏览器启动和授权边界。

尚未做真实账号登录或修改任何远端业务数据；两种服务能否复用页面里的 SSO 只影响扫码次数，
不作为授权正确性的前提，也不承诺“第一次一定只扫一次码”。
