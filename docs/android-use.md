# AndroidUse：Android 设备操作层设计

> 状态：前台无障碍控制和单独授权的后台虚拟屏控制已接线；后台控制采用手机本机无线调试与受限 shell helper。主屏隔离、预览、接管后的点击与滑动以及交还流程已在单台 Android 14 设备上测试；虚拟键盘和其他系统、机型仍需验证。
>
> 目标：为 Golder Android 增加类似桌面 BrowserUse / ComputerUse 的基本能力，让 Agent 能观察和操作其他 Android App。
>
> 参考：Mika 的公开设计（AccessibilityService、后台虚拟屏、无线调试 helper）。本文只借鉴架构，不复制其代码。

> 用户和模型接口使用“前台／后台控制”语义：现有 `observe`、`tap` 等动作操作可见屏幕，`background_*` 动作操作经用户单独授权的后台虚拟屏。下文的 Phase 1 / Phase 2 仅为历史设计阶段名称，不是用户操作名称；后台控制仍需显式授权和无线调试配对。

后台 App 启动后，Golder 聊天页右上角会出现实时画面缩略图，点按可放大并选择“接管操作”。Golder 先等待已开始的工具调用结束并暂停新调用，然后用户可在画面上点击、滑动、使用虚拟屏上的键盘或按返回；“交还 Agent”恢复运行。接管期间不能由模型解除暂停；接管前规划但尚未执行的调用不会在交还后重放。交还后，再次打开、点击、释放或停止后台显示前，必须先通过 `background_observe` 取得新画面及 `snapshotId`，再使用该快照执行下一次后台操作。

预览不是跨应用悬浮窗：最新帧只保留在本机内存，不写入文件，也不会自动发送给模型；模型主动调用 `background_observe` 时，该帧才作为图片发送给模型。用户接管期间模型不能读取后台画面。释放虚拟屏或助手退出后预览会清除；系统保护的窗口可能显示黑屏。接管只支持单指操作，具体键盘行为和隔离能力依赖设备系统。

更新或重装 Golder 后，系统会把已启用的无障碍服务摘掉（本机实测会直接从“已启用服务”列表里移除）。这不是应用能自动恢复的：需要在系统无障碍设置里把 Golder 关掉再打开一次，然后回到 Golder 设置点“刷新状态”。设置页的第 1 步会提示这一点。

## 1. 结论

AndroidUse 采用 **Go 核心 + Kotlin 平台适配层**：

- **Go** 负责工具协议、观察结果建模、ADB 配对/连接、helper 启动与交接、虚拟屏会话、动作选择、状态机、重试、审批、审计和清理决策。
- **Kotlin** 只负责无法在 Go 中可靠实现的 Android framework 对象：AccessibilityService、截图/ImageReader、Surface、Binder helper、`DisplayManager`、`ActivityTaskManager`、`InputManager` 和 overlay。
- **Phase 1** 只做前台/可见屏幕上的 Accessibility 操作，不需要无线调试，风险和实现复杂度较低。
- **Phase 2** 增加 Mika 式后台虚拟屏：用户显式开启“高级手机控制”，通过纯手机无线调试配对启动 shell helper，不要求 root 或电脑。

不追求“Kotlin 业务层”。`AccessibilityService` 是 Android framework 的生命周期组件，`DisplayManager`、`Surface`、`ImageReader` 和输入注入都需要 Java/Kotlin 平台对象；但这些对象只通过窄 JNI 适配层暴露给 Go。**ADB 协议、helper 编排、会话状态、策略和清理决策都在 Go，不在 Kotlin。**

## 2. 参考案例：Mika

Mika 的能力链路可以概括为：

1. 主进程负责 UI、Agent、Accessibility、审批和预览。
2. 通过无线调试配对获得 `shell` 身份，启动内置 helper 进程。
3. helper 以 shell 身份创建隐藏虚拟屏，App 在虚拟屏中运行。
4. 主进程从 `ImageReader` 读取虚拟屏画面，用户当前主屏不受影响。
5. Accessibility 读节点和执行语义动作；OCR/视觉作为兜底。
6. 登录、验证码、支付等敏感操作交还用户。
7. helper 退出、Binder 断开或任务结束时释放虚拟屏、临时资源和输入状态。

参考源码（Apache-2.0，只借鉴架构，不复制代码）：

- `core/adb/`：纯手机 ADB 配对、TLS、连接和 exec。
- `core/shell/HelperHandoff.kt`：一次性 nonce、签名 pin、启动命令。
- `core/shell/HelperHub.kt`：Binder 交接校验和死亡监听。
- `cap/phone/display/AgentDisplay.kt`：`ImageReader` 与虚拟屏帧。
- `cap/phone/shell/ShellService.kt`：`DisplayManager`、任务启动、输入注入和清理。
- `setup/BuiltinShellManager.kt`、`setup/UsbStartScript.kt`：无线调试启动和 USB 后备。

上游：<https://github.com/wangzan101/Mika>

值得借鉴的是“观察、动作、用户接管”的交互模型；不应直接照搬其权限范围或把所有用户都引导到 shell helper。

## 3. 总体架构

```text
┌──────────────────────────────────────────────────────────┐
│ Go: internal/androiduse                                  │
│  ADB pairing/connect · helper launch/handoff             │
│  display session · policy · action loop · cleanup        │
└───────────────────────┬──────────────────────────────────┘
                        │ narrow AndroidUseHost (JNI)
┌───────────────────────▼──────────────────────────────────┐
│ Kotlin: Android platform only                            │
│  AccessibilityService · overlay · ImageReader/Surface    │
│  :helper ShellService                                    │
│  (DisplayManager / ActivityTaskManager / InputManager)   │
│  Binder handoff provider                                 │
└───────────────────────┬──────────────────────────────────┘
                        │
        ┌───────────────┼────────────────┐
        ▼               ▼                ▼
 Accessibility    ImageReader       Shell helper
 UI tree/actions   virtual frames    display/input
```

Golder 继续使用现有的 in-process JNI 路线。Go 不直接持有 Android `Context` 或 framework 对象；Kotlin 只向 Go 暴露 `AndroidUseHost`，不向 Go 暴露任意 shell 或 Binder 权限。helper 是唯一以 shell 身份运行的进程，只实现受限 AIDL。

## 4. Go / Kotlin 边界

| 层 | 语言 | 职责 |
|---|---|---|
| Tool contract | Go | `android_use` 参数、结果、错误码、快照引用 |
| Agent loop | Go | 观察后决策、动作序列、重试、超时、取消 |
| Policy | Go | 权限门、敏感动作分类、用户确认、审计 |
| Session | Go | 运行记录、截图引用、历史与恢复 |
| ADB | Go | mDNS、配对、TLS/SPAKE2、连接、exec、身份持久化 |
| Helper launch / handoff | Go | nonce、启动命令、交接校验、Binder 死亡处理 |
| Display session | Go | display 生命周期、App 任务、串行化、清理决策 |
| Bridge | Go/JNI | 向 Kotlin 发起请求并等待结构化结果 |
| Accessibility | Kotlin | 节点树、`performAction`、`dispatchGesture` |
| Capture | Kotlin | `ImageReader`、Surface、帧尺寸/旋转、预览位图 |
| Virtual display | Kotlin helper | `DisplayManager.createVirtualDisplay`、释放 |
| App launch / input | Kotlin helper | `ActivityTaskManager`、`InputManager`、剪贴板/音频 |
| Shell helper | Kotlin | 仅在 Phase 2、用户授权后以 shell 身份运行受限 AIDL |

Kotlin 不做业务规划，也不决定哪些 App 能操作、何时清理、是否允许重试。Go 策略层批准后，Kotlin 只执行平台原语并返回可验证结果。Go 也不向模型暴露 helper 的任意命令接口。

## 5. 工具契约

第一版建议只暴露一个 Go 工具 `android_use`，用 `action` 区分动作，避免工具数量失控。

### 5.1 支持的动作

| action | 作用 |
|---|---|
| `observe` | 返回当前 App、屏幕尺寸、可交互节点、截图引用 |
| `screenshot` | 返回截图的本地引用，不直接把大图塞进对话 |
| `tap` | 点击节点 ref；没有 ref 时允许坐标，但必须带快照 id |
| `type` | 向可编辑节点输入文本，不自动提交 |
| `swipe` | 滑动/滚动；节点区域优先，坐标兜底 |
| `open_app` | 按包名或用户可见名称打开 App |
| `back` | 返回键 |
| `home` | 回主屏 |
| `wait` | 有界等待界面稳定 |
| `take_over` | 暂停 Agent，把当前 App 交还用户 |

### 5.2 快照与节点

`observe` 返回一次性快照：

```json
{
  "snapshot_id": "s-42",
  "package": "com.example.app",
  "activity": ".MainActivity",
  "display": {"width": 1080, "height": 2400, "density": 420},
  "screenshot": "artifact://android-use/s-42.png",
  "nodes": [
    {
      "ref": "n-7",
      "role": "button",
      "text": "Send",
      "bounds": [820, 2100, 1040, 2200],
      "clickable": true,
      "editable": false,
      "focused": false
    }
  ]
}
```

约束：

- 每个动作必须带当前 `snapshot_id`；节点 ref 过期后拒绝执行，不猜测新目标。
- 节点树有数量和文本长度上限；超限时保留可交互节点和截断标记。
- 密码、验证码、支付信息等敏感节点在观察结果中脱敏；模型只能看到必要上下文。
- 坐标动作只作为语义节点不可用时的兜底，不能借坐标绕过敏感控件保护。
- 已派发但结果不确定的动作不重放；先重新观察，再决定下一步。

## 6. Phase 1：前台 Accessibility

### 6.0 当前 bridge 实现

Phase 1 使用 **loopback TCP + 每进程随机 token**，而不是 Android `LocalServerSocket` 的抽象命名空间：

- Kotlin `AndroidUseServer` 只绑定 `127.0.0.1` 的随机端口；
- token 和端口写入 App 私有目录的 `android-use.json`；
- Go `internal/androiduse/client.go` 读取该文件并带 token 发请求；
- 不开放 Wi-Fi/TCP 外部监听，不需要网络授权；
- 端点和 token 在 service 停止时删除。

这样 Go 可以通过普通 loopback socket 访问 AccessibilityService，同时避免把控制面暴露给其他 App。

### 6.0.1 已实现的 Phase 1 hardening

当前实现已经包含：

- Settings 中的 AndroidUse 控制开关、暂停/恢复、允许包名和最近活动；
- Go 侧只读模式拒绝控制动作；
- Kotlin 侧敏感文字/密码字段/系统安装器拦截；
- `tap`/`type`/`swipe`/`back`/`home`/`open_app` 的前台包名验证；
- `take_over` 写入持久暂停状态，暂停期间拒绝控制动作，`resume` 恢复；
- loopback endpoint + 每进程随机 token，endpoint/token 在 service 停止时删除；
- 截图写入 App 私有 cache，并在 Go 工具结果中以 image content 返回模型；
- 审计记录只保存动作、包名、结果和错误摘要，不保存屏幕文本；
- 审计文件最多保留 200 条；Settings 只展示最近 8 条，并默认折叠。
- 包 allowlist：留空允许所有 App，非空时只允许列出的包名。

已增加 AndroidUse 常驻状态通知：显示当前动作和包名。此前用作敏感操作确认的悬浮层已删除（Mika 的浮层是胶囊入口，不是确认面板）：遇到支付、删除、验证码一类的敏感控件时，服务会暂停控制并把控制权交还用户，由用户在后台画面的“接管操作”里自行处理，交还后再继续。

Settings 还通过 **服务 heartbeat + loopback status probe** 检测“无障碍已启用但服务未绑定”：只有 heartbeat 新鲜或 bridge 应答成功才显示已连接；否则显示未连接，并提供“重新绑定无障碍”按钮和操作说明。App 回到前台时会重新检查。仍未完成：完整 OEM 设备矩阵和真实长时任务验收。

### 6.1 范围

- 用户手动开启无障碍服务。
- Agent 观察当前可见 App，执行点击、输入、滚动、返回、回主页和打开 App。
- 提供屏幕实时预览和 `take_over`。
- 不上后台虚拟屏，不需要无线调试，不需要 shell helper。
- 目标 Android 12+；Android 14+ 作为推荐验证环境。

### 6.2 组件

```text
mobile/android/app/src/main/java/com/getan/golder/mobile/androiduse/
  GolderAccessibilityService.kt
  AndroidUseForegroundService.kt
  AndroidUseBridge.kt
  UiTreeSerializer.kt
  ScreenshotCapture.kt

internal/androiduse/
  tool.go
  observation.go
  action.go
  policy.go
  host.go
```

### 6.3 用户流程

1. 用户打开 Settings → AndroidUse。
2. 页面说明能读取屏幕和操作 App，要求用户开启无障碍。
3. 首次 `observe` 前显示当前目标 App 和权限状态。
4. Agent 提交动作；敏感动作（发送、支付、登录、验证码、修改设置）必须确认或交还用户。
5. 用户可随时 `take_over`、停止任务或关闭 AndroidUse。

### 6.4 Phase 1 不做

- 不操作后台/不可见 App。
- 不需要无线调试、ADB 或 root。
- 不提供任意 shell/系统设置能力。
- 不绕过 Android 权限弹窗、锁屏或安全窗口。

### 6.5 验收标准

- 在测试 App 上完成：观察、点击按钮、输入文本、滚动、back、home、open_app。
- 节点 ref 过期、快照过期、目标 App 切换时明确失败，不误点。
- 用户开启/关闭无障碍后状态正确；未授权时不暴露工具。
- `take_over` 立即取消未完成动作；用户接管期间 Agent 不注入输入。
- 屏幕内容不写入普通日志或会话文本；截图按可配置保留策略处理。
- 当前验收设备为 Redmi / Android 14；在这台设备上完成基本回归即可，多设备矩阵作为可选扩展。

## 7. Phase 2：后台虚拟屏（Mika 路线，Go 主体）

### 7.0 路线决定

Phase 2 采用 Mika 的实际实现路线，而不是先做电脑 USB 方案：

- **纯手机无线调试为主路径**：用户配对一次后，App 内完成 ADB 配对、连接和 helper 启动，不需要电脑或 root。
- **shell helper**：helper 以 Android `shell` UID 2000 运行，负责 Android framework 的虚拟屏、App 启动和输入注入。
- **后台虚拟屏**：目标 App 运行在隐藏 display 上，用户当前主屏不受影响。
- **USB 仅作后备**：无线调试入口缺失或不可用时，用户可显式改用电脑 `adb` 启动同一个 helper；不能静默降级。
- **Go 主体，Kotlin 平台适配**：ADB、nonce、会话、策略、动作编排和清理决策在 Go；Kotlin 只实现 Android 平台对象和受限 AIDL。

Mika 的源码只作为架构参考（Apache-2.0）；Golder 不复制其代码，而是按本文的 Go/Kotlin 边界重新实现。

### 7.1 触发条件与失败关闭

Phase 2 是独立的“高级手机控制”能力，默认关闭：

- 用户阅读并明确接受“shell 身份强于普通 App 权限”的风险说明。
- 用户完成一次无线调试配对；配对信息由 Go 持久化并加密保存。
- Go 启动 helper、完成 Binder 交接，并确认 helper 的 UID、PID、APK、签名和 nonce。
- helper 不可用、被系统停止、配对撤销或权限变化时，Phase 2 失败关闭，不回退到静默的前台操作。
- 用户可一键停止 helper、释放虚拟屏并清除配对；旧 Binder、旧 display 和旧 nonce 立即失效。

### 7.2 启动链

1. **Go ADB 客户端**
   - 生成并保管 ADB 身份密钥；私钥只在 App 私有存储中使用，Android Keystore 包装静态加密密钥。
   - 通过 mDNS 发现 `_adb-tls-pairing._tcp` 和 `_adb-tls-connect._tcp`，只接受本机 localhost / Wi-Fi 地址。
   - 完成 TLS 1.3、SPAKE2 配对码认证和 PeerInfo 交换。
   - 建立 ADB 连接，执行受限 helper 启动命令；不向模型暴露任意 `adb shell`。

2. **Go 启动 helper**
   - Go 生成一次性 nonce，通过 ADB exec 流的标准输入传给 helper，不放在命令行，避免 `/proc/<pid>/cmdline` 泄露。
   - 启动命令使用主 APK 的 `CLASSPATH` 和 `app_process`，进程名为 `com.getan.golder.mobile:helper`。
   - helper 是主 APK 内的受限 Kotlin 进程，不是独立可执行文件，也没有 root 权限。

3. **Binder 交接**
   - Kotlin helper 实现受限 AIDL `IGolderShell`，只暴露虚拟屏、App 启动、输入、剪贴板和清理等语义方法。
   - Kotlin 的 exported ContentProvider 只负责采集调用方 UID/PID、APK、签名和 Binder；nonce、签名 pin、接受/拒绝决策由 Go 控制。
   - 校验通过后，Go 才取得受限 AIDL 代理；Kotlin 不取得 ADB 或任意 shell 权限。
   - 双方注册 Binder 死亡监听；helper 或主进程退出时，释放显示资源并清除会话。

4. **虚拟屏**
   - Kotlin 在 App 进程创建 `ImageReader` 和 `Surface`。
   - Go 指示 Kotlin 把 Surface 交给 helper；helper 调用 `DisplayManager.createVirtualDisplay()` 创建隐藏 display。
   - helper 优先使用 trusted display flags；系统不支持时退回兼容 flags，并明确报告能力降级。
   - 虚拟屏设置全屏窗口模式、IME 策略、方向监听和保活；不能抢用户主屏焦点。
   - 画面从 `ImageReader` 读取，Kotlin 负责像素/旋转/尺寸，Go 负责帧消费策略、时间戳和截图生命周期；不经过主屏截图接口。

5. **App 启动与输入**
   - Go 先检查包 allowlist、当前主屏是否正在使用目标 App、是否已有同 App 会话。
   - helper 通过 `IActivityTaskManager.startActivityAsUser` + `ActivityOptions.setLaunchDisplayId` 启动到虚拟屏，失败才退回 `am start --display`。
   - 语义动作优先走 Accessibility；坐标和手势由 helper 以 `InputManager.injectInputEvent` + `setDisplayId` 注入，失败才退回 `input -d`。
   - 主屏上的任务不被抢走；同一 App 同时只允许一个虚拟屏会话。

6. **清理**
   - 任务结束时按策略把保留的 App 移回主屏并排在当前任务之后，关闭其余 App。
   - helper 释放所有虚拟屏、Surface、剪贴板恢复、音频恢复和临时文件；`destroy()` 退出 helper。
   - Go 清除 nonce、display 句柄和旧快照；用户关闭 Phase 2 后旧句柄不可复用。

### 7.3 Go / Kotlin 责任边界

| 能力 | Go | Kotlin |
|---|---|---|
| ADB 配对/连接/身份 | 全部 | 无 |
| nonce、签名 pin、交接接受 | 全部 | 只采集调用方信息 |
| 虚拟屏会话、display id、清理决策 | 全部 | 执行平台调用 |
| 包 allowlist、敏感动作、审批、审计 | 全部 | 无 |
| `ImageReader` / Surface / 帧旋转 | 消费策略 | 平台对象与像素 |
| 虚拟屏创建/释放 | 决策 | `DisplayManager` |
| App 启动/任务移动 | 决策 | `ActivityTaskManager` |
| 点击/滑动/按键注入 | 决策 | `InputManager` / `AccessibilityService` |
| Accessibility 节点和语义动作 | 决策 | 平台读取与执行 |
| overlay / 通知 / 用户接管 UI | 状态与策略 | 渲染与生命周期 |

Kotlin 不发起 ADB 连接、不保存 ADB 私钥、不判断 allowlist，也不提供任意 shell 方法。Go 不直接持有 Android `Context`、`Surface` 或 Binder；所有跨层调用必须经过窄 `AndroidUseHost` 和受限 AIDL。

### 7.4 验收标准

- 当前单机验收：Redmi / Android 14（MIUI/HyperOS 环境）。
- 配对一次后可重复启动；系统重启后状态可见，不能静默失效。
- 只允许经过 allowlist 和 Go 策略批准的目标 App 进入虚拟屏。
- 用户当前前台 App 不受影响；用户可以通过 live view 观察，并可随时停止。
- helper 断开、display 创建失败、App 崩溃、Surface 丢失、nonce 失效时，任务失败关闭并清理资源。
- 同一 App 的多任务串行化；不允许两个任务同时向同一 App 注入冲突输入。
- 关闭“高级手机控制”后，helper 和虚拟屏立即停止，旧句柄、旧 nonce 不可复用。
- Go 单元测试覆盖 ADB 握手、配对失败、nonce、Binder 拒绝和清理状态机；Kotlin instrumentation 覆盖虚拟屏创建/释放和 helper 中断。

### 7.5 额外风险

- `shell` 身份远强于普通 App；这层的漏洞影响面大。
- 虚拟屏 flags 和后台启动行为依赖 OEM；MIUI/HyperOS 可能有额外后台限制。
- 后台 App 的通知、音频、权限弹窗和 WebView 行为可能与前台不同。
- 不能把 shell helper 当作安全沙箱；它是用户明确授权的特权执行通道。

### 7.6 USB 后备

仅在无线调试入口缺失、配对不可用或用户明确选择时提供：

- App 在外部文件目录生成一次性 `usb-start.sh`，电脑执行 `adb shell sh <script>`。
- 脚本检查 `id -u` 必须为 2000，使用 `nohup setsid` 脱离 adb 会话启动同一个 helper。
- nonce 写入脚本但有有效期；helper 连接后脚本换成不带 nonce 的 stub。
- 手机重启后 USB 启动的 helper 不会自动恢复，必须重新执行命令。
- USB 启动不能成为静默回退，也不能把普通前台操作伪装成后台能力。

## 8. 安全与权限边界

AndroidUse 是独立能力，不继承 `android_build` 或文件工具的网络/执行授权。

- **能力门**：只有 Settings 中开启且用户已授权 Accessibility 后，工具才出现在 catalog。
- **每次动作**：Go 的策略层检查动作类型、目标 App、快照和敏感控件。
- **敏感动作**：支付、转账、发送消息、登录、验证码、删除、修改权限/设置默认需要用户确认或 `take_over`。
- **用户接管**：任何未完成动作取消；旧快照、旧输入句柄和旧截图凭证立即失效。
- **禁止自我提权**：Agent 不能操作 Golder 自身的权限页、模型设置、预算或 AndroidUse 开关。
- **不自动改权限**：不替用户点系统的无障碍、开发者选项、无线调试或安装确认。
- **脱敏**：截图和 UI 文本在进入模型上下文前做凭据/敏感字段过滤；审计只记录动作类型、摘要 hash 和结果码。
- **最小持久化**：截图默认只保留任务所需时间；会话落盘不包含原始截图或完整界面文本。
- **可停止**：用户停止、切走目标 App、锁屏、系统权限撤销、helper 断开都使会话失效。

Phase 2 额外规则：

- **独立能力门**：Phase 2 默认关闭，必须单独接受风险说明；不能继承 Phase 1 的“允许控制”开关。
- **Go 是唯一策略源**：Kotlin provider、helper 和 Accessibility 都不判断 allowlist、不决定重试、不决定是否清理。
- **受限 AIDL**：helper 只暴露语义方法，不提供任意 `sh -c`、任意文件访问或任意 Intent；模型永远看不到 shell。
- **本地 ADB**：Go 只连接本机 localhost / Wi-Fi 地址，不连接外部 ADB；配对身份和 nonce 不得写入日志。
- **交接防线**：shell UID、调用方 PID、APK 路径、签名 pin、一次性 nonce、Binder identity 全部通过后才接受 helper。
- **主屏保护**：目标 App 正在主屏使用时，Phase 2 不抢占；无法确认时按“不启动”处理。
- **一键停止**：停止 Phase 2 必须同时停止 helper、释放 display、清除旧 nonce/Binder，并恢复音频/剪贴板等副作用。

Phase 2 必须单独提示“这是 shell 身份，不是普通 App 权限”，并提供一键停止 helper 的入口。

## 9. 兼容性与已知限制

- `FLAG_SECURE`、DRM、银行/支付、部分 WebView 和自绘 UI 可能无法截图或读取节点。
- 无障碍节点可能缺失；Phase 1 可用 OCR/视觉模型兜底，但需要额外模型调用和隐私说明。
- 系统权限弹窗、锁屏、PIN、生物识别必须由用户处理。
- OEM 后台限制、电池优化、无线调试入口缺失会影响 Phase 2。
- 部分 OEM（包括 MIUI）会拒绝 AccessibilityService 的后台 `startActivity`；`open_app` 会验证前台包名并返回明确错误，不会把被拦截的启动报告为成功。用户可手动打开 App 后再 `observe`。
- 不能保证所有 App 都能自动化；失败时应报告具体限制，不伪造成功。
- 远程模型会看到选定的界面内容；provider 的数据策略仍需用户自行判断。

## 10. 测试策略

### Go 单元测试

- Fake `AndroidUseHost` 返回固定快照和动作结果。
- 快照过期、节点 ref 消失、动作不确定、超时、取消、敏感动作拒绝。
- 节点树截断、文本脱敏、截图引用生命周期。
- Phase 2：ADB mDNS/配对/连接状态机、SPAKE2 失败、证书错误、nonce 过期/重放、Binder UID/PID/签名拒绝。
- Phase 2：虚拟屏会话串行化、目标 App 在主屏占用、helper 断开、display 释放失败、清理幂等。

### Kotlin instrumentation

- Test-only App：按钮、输入框、列表、弹窗、不可读画布。
- 无障碍开启/关闭、目标切换、进程死亡、旋转、分屏。
- Phase 2：`ImageReader`/Surface 创建与释放、trusted flags 与兼容 flags、虚拟屏旋转、App 启动到指定 display、输入注入、helper 中断、重启恢复。

### 设备矩阵

- 当前单机验收：Redmi / Android 14（MIUI/HyperOS 环境）。
- 多设备/Pixel/AOSP 矩阵作为后续可选扩展。
- 真实 App 验收只使用测试账号和明确授权的 App。

## 11. 迁移与发布顺序

1. Phase 1 Go 接口 + fake host + Kotlin AccessibilityService。
2. Phase 1 UI、live view、`take_over` 和策略门。
3. 设备回归，默认关闭 AndroidUse。
4. Phase 2 Go ADB 身份/配对/连接、nonce 和 Binder 交接状态机。
5. Phase 2 Kotlin helper：受限 AIDL、`DisplayManager`、`ImageReader`/Surface、App 启动和输入注入。
6. Phase 2 Go 会话：显示生命周期、allowlist、主屏保护、串行化、清理和失败关闭。
7. Phase 2 风险 UI、一键停止、日志脱敏和设备验收。
8. 灰度开关；保留“只在前台操作”的降级路径。

Phase 1 的功能不应依赖 Phase 2 的 shell helper；Phase 2 不能改变 Phase 1 的安全承诺。

## 12. 非目标

- 不做 root 自动化。
- 不把任意 `adb shell` 暴露给模型。
- 不绕过系统权限或安全窗口。
- 不默认后台操作。
- 不追求 100% App 兼容。
- 不把 AndroidUse 变成安装器或文件管理器；这些继续使用现有专用工具。

## 13. 待定决策

- Phase 1 截图用 `AccessibilityService.takeScreenshot` 还是按需 `MediaProjection`（前者权限面更小，后者兼容性更广）。
- Phase 2 ADB 私钥的 Keystore 包装格式和轮换策略。
- Phase 2 在不同 OEM 上 trusted display flags 的降级矩阵。
- Phase 2 视觉/OCR 兜底的模型调用频率和隐私开关。
- 敏感动作的具体分类和用户确认 UI（Phase 1 已实现 overlay 的 Allow/Deny，仍可细化分类）。
- 截图/界面文本的保留时间与本地清理策略。

## 14. 相关文档

- [Android JNI 桥接](android-jni.md)
- [Android 回归测试](android-testing.md)
- [Android 原生架构](mobile-native-architecture.md)
- [Computer Use 方案](computer-use.md)
- [Mika 参考实现](https://github.com/wangzan101/Mika)
