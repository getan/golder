# Android 回归测试：没有手机时也能跑哪几层

Android 回归分三层，前两层不依赖手机。完整设备行为仍需要设备或模拟器。

```bash
# 无手机：主机测试 + Android 交叉编译/类型检查
scripts/android-regression.sh

# 有设备或模拟器：同一脚本自动追加设备测试
scripts/android-regression.sh

# CI：没有设备时直接失败，而不是静默跳过
REQUIRE_DEVICE=1 scripts/android-regression.sh

# 指定设备
ANDROID_SERIAL=emulator-5554 scripts/android-regression.sh
```

## 第 1 层：主机测试，始终可运行

`internal/mobileagent` 的测试用脚本化模型验证移动边界本身：流式文本、工具调用经过宿主 gate、取消、忙碌拒绝、JSON 事件和快照。它不依赖 Android、JNI 或网络，因此在 macOS/Linux 上都能跑。

这一层能防住协议和状态机回归，但测不到 Android 系统调用、App 沙箱或真实文件系统。

## 第 2 层：Android 交叉编译，始终可运行

脚本以 `GOOS=android GOARCH=arm64 CGO_ENABLED=0` 对移动核心和 Android 工具集执行 `go vet`，并编译测试二进制。它保证代码在 Android 目标上仍能编译、类型检查通过；不执行任何设备代码。

## 第 3 层：设备或模拟器

脚本检出 `adb` 和一个处于 `device` 状态的设备，推送测试二进制并运行。当前设备测试包括：

- 移动边界的全部主机测试；
- `TestAndroidToolsetRoundTrip`：真实 Android 工具集通过移动边界读取文件、用 `apply_patch` 写入并读回，同时确认宿主 gate 收到两次调用。

没有设备时脚本默认跳过这一层并返回成功，适合本地快速回归。CI 应使用 `REQUIRE_DEVICE=1`，避免“测试通过”其实什么都没跑。

模拟器是手机的替代品，不是完全等价物：它能覆盖大部分文件、取消和工具行为，但 JNI 装载、系统 DNS/CGO、厂商后台限制、真实凭据和发布渠道仍应至少在实体设备上验证一次。

JNI 共享库有独立的构建与 ABI loader：

```bash
scripts/android-jni.sh
REQUIRE_DEVICE=1 scripts/android-jni.sh
```

它验证 .so 能在设备上加载、C ABI 可调用、文件型会话可以打开并读快照；
Kotlin/JVM 的 JNI 调用由下面的 app 级测试覆盖。

App 级 JNI 测试：

```bash
scripts/android-app-test.sh
REQUIRE_DEVICE=1 scripts/android-app-test.sh
```

该脚本构建 app 和 androidTest APK，用 `pm install` 安装，再运行
`JniSessionTest`。测试内起一个回环 OpenAI SSE 假服务，验证 Kotlin→JNI→Go→
Provider→Agent 循环的完整流式回合；不需要真实模型或 API key。

设备侧 Go 测试还覆盖完整 Android 后端和会话持久化：完成一轮后关闭，再用
同一 session id 打开，验证历史恢复。

`internal/mobilegit` 的主机和 Android 设备测试覆盖本地 init/add/commit/status/
log/branch、路径逃逸、网络授权失败关闭，以及通过嵌入 file transport 的
push→clone 往返；不会使用真实远程或凭据。

## 还应补上的无设备测试

- 模拟器镜像纳入 CI（Linux KVM runner 或 Firebase Test Lab），并固定 Android API 版本；
- JNI 桥接的 Kotlin 单元测试/Robolectric 测试（需要 Android Gradle 工程）；
- 真实 Provider 的流式响应和取消，用本地假服务器而不是公网模型；
- 会话持久化与进程重启恢复的故障注入。

在这些补齐之前，“无手机回归”只覆盖第 1、2 层；它不会把设备专属能力误报为已验证。
