# AndroidUse：Android 设备操作层设计

> 状态：设计提案，尚未实现。
>
> 目标：为 Golder Android 增加类似桌面 BrowserUse / ComputerUse 的基本能力，让 Agent 能观察和操作其他 Android App。
>
> 参考：Mika 的公开设计（AccessibilityService、后台虚拟屏、无线调试 helper）。本文只借鉴架构，不复制其代码。

## 1. 结论

AndroidUse 采用 **Go 核心 + Kotlin 适配层**：

- **Go** 负责工具协议、观察结果建模、动作选择、状态机、重试、审批、审计和会话。
- **Kotlin** 负责无法在 Go 中可靠实现的 Android 平台能力：AccessibilityService、截图、虚拟屏、Binder/JNI 生命周期。
- **Phase 1** 只做前台/可见屏幕上的 Accessibility 操作，不需要无线调试，风险和实现复杂度较低。
- **Phase 2** 增加 Mika 式后台虚拟屏：用户显式开启“高级手机控制”，通过无线调试配对启动 shell helper。

不追求纯 Go。`AccessibilityService` 是 Android framework 的生命周期组件，`DisplayManager`、`MediaProjection` 和输入注入都需要 Java/Kotlin 平台对象；Go 直接通过 JNI 调这些 API 会不稳定，也会把平台细节泄漏进 Agent 核心。

## 2. 参考案例：Mika

Mika 的能力链路可以概括为：

1. `AccessibilityService` 读取界面树，执行点击、输入和滚动。
2. 通过无线调试启动内置 helper，以 `shell` 身份创建 `mika-agent` 虚拟屏。
3. App 在虚拟屏中运行，用户当前屏幕不受影响。
4. UI 树不可用时，使用 OCR 或视觉模型读取截图。
5. 登录、验证码、支付等敏感操作交还用户。

值得借鉴的是“观察、动作、用户接管”的交互模型；不应直接照搬其权限范围或把所有用户都引导到 shell helper。

## 3. 总体架构

```text
┌─────────────────────────────────────────────────────────┐
│ Go Agent                                                │
│  internal/androiduse                                    │
│  observe / act / wait / take_over                       │
└───────────────────────┬─────────────────────────────────┘
                        │ AndroidUseHost interface
                        │ JNI / Binder / local bridge
┌───────────────────────▼─────────────────────────────────┐
│ Kotlin Android host                                     │
│  GolderAccessibilityService   Phase 1                   │
│  AndroidUseForegroundService  live view / lifecycle      │
│  ShellHelper (optional)       Phase 2 virtual display   │
└───────────────────────┬─────────────────────────────────┘
                        │
        ┌───────────────┼────────────────┐
        ▼               ▼                ▼
 Accessibility    Screenshot       Virtual display
 UI tree/actions   PNG frame        background apps
```

Golder 继续使用现有的 in-process JNI 路线。Kotlin 已经把 Go 会话加载到 App 进程；AndroidUse 再增加一个 host 接口，不让 Go 直接持有 Android `Context` 或 framework 对象。

## 4. Go / Kotlin 边界

| 层 | 语言 | 职责 |
|---|---|---|
| Tool contract | Go | `android_use` 参数、结果、错误码、快照引用 |
| Agent loop | Go | 观察后决策、动作序列、重试、超时、取消 |
| Policy | Go | 权限门、敏感动作分类、用户确认、审计 |
| Session | Go | 运行记录、截图引用、历史与恢复 |
| Bridge | Go/JNI | 向 Kotlin 发起请求并等待结构化结果 |
| Accessibility | Kotlin | 节点树、`performAction`、`dispatchGesture` |
| Capture | Kotlin | 截图、图像尺寸、帧序号、生命周期 |
| Virtual display | Kotlin | `DisplayManager`、Surface、App 启动与清理 |
| Shell helper | Kotlin | 仅在 Phase 2、用户授权后使用无线调试身份 |

Kotlin 不做业务规划，也不决定哪些 App 能操作；它只执行经过 Go 策略层批准的动作，并返回可验证的结果。

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
- Android 12、13、14 至少各一台设备/模拟器完成基本回归。

## 7. Phase 2：后台虚拟屏

### 7.1 触发条件

Phase 2 是独立的“高级手机控制”能力，默认关闭，必须由用户明确开启：

- 用户阅读并接受额外风险说明。
- 用户通过无线调试配对一次。
- App 启动内置 shell helper，并确认 helper 状态。
- helper 不可用、被系统停止或权限撤销时，AndroidUse 失败关闭，不回退到静默的前台操作。

### 7.2 机制

1. Kotlin 通过无线调试建立本机连接，启动受限制的 helper。
2. helper 以 Android `shell` 身份创建虚拟 display 和 Surface。
3. 目标 App 启动到该 display；屏幕内容通过 Surface/截图回传。
4. AccessibilityService 负责读节点和执行语义动作；坐标手势在目标 display 上注入。
5. 任务结束后关闭目标 App、释放 display、清理 Surface 和临时截图。

不要求 root。无线调试不可用或 OEM 删除了入口时，可以不提供 Phase 2，或提供 USB 启动作为明确的高级后备；不能把普通前台操作伪装成后台能力。

### 7.3 Phase 2 验收标准

- 配对一次后可重复启动；重启后状态可见，不能静默失效。
- 只允许经过白名单/策略批准的目标 App 进入虚拟屏。
- 用户当前前台 App 不受影响；用户可通过 live view 观察。
- helper 断开、display 创建失败、App 崩溃、Surface 丢失时，任务失败关闭并清理资源。
- 同一 App 的多任务串行化；不允许两个任务同时向同一 App 注入冲突输入。
- 关闭“高级手机控制”后，helper 和虚拟屏立即停止，旧句柄不可复用。

### 7.4 额外风险

- `shell` 身份远强于普通 App；这层的漏洞影响面大。
- 虚拟屏依赖 OEM 实现，兼容性和行为差异大。
- 后台启动 App、通知、音频、权限弹窗都可能触发系统限制。
- 不能把 shell helper 当作安全沙箱；它是受用户授权的特权执行通道。

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

### Kotlin instrumentation

- Test-only App：按钮、输入框、列表、弹窗、不可读画布。
- 无障碍开启/关闭、目标切换、进程死亡、旋转、分屏。
- Phase 2：虚拟屏创建/释放、App 启动、helper 中断、重启恢复。

### 设备矩阵

- Pixel/AOSP Android 12、14、15。
- 至少一台国内 OEM ROM，覆盖后台限制和无线调试差异。
- 真实 App 验收只使用测试账号和明确授权的 App。

## 11. 迁移与发布顺序

1. Phase 1 Go 接口 + fake host + Kotlin AccessibilityService。
2. Phase 1 UI、live view、`take_over` 和策略门。
3. 设备回归，默认关闭 AndroidUse。
4. Phase 2 helper、虚拟屏、白名单和资源清理。
5. 灰度开关；保留“只在前台操作”的降级路径。

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
- Phase 2 是否同时支持 USB 启动 helper，还是首版只支持无线调试。
- 虚拟屏输入由 Accessibility 手势统一注入，还是由 shell helper 提供更底层的 display 输入。
- 敏感动作的具体分类和用户确认 UI。
- 截图/界面文本的保留时间与本地清理策略。

## 14. 相关文档

- [Android JNI 桥接](android-jni.md)
- [Android 回归测试](android-testing.md)
- [Android 原生架构](mobile-native-architecture.md)
- [Computer Use 方案](computer-use.md)
