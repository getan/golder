# Android JNI 桥接

`cmd/golder-mobile` 把移动会话边界编译成 `libgolder_mobile.so`，通过 JNI 供 Kotlin 调用。服务层在 `internal/mobilebridge`，不依赖 JVM，因此主机测试和 Android 设备测试可以分开进行。

## 当前状态

- Go 共享库可用 NDK 构建，JNI 和 C ABI 符号都已导出。
- 设备上的 C ABI loader 已实际加载该库，打开一个文件型会话、读取快照并关闭。
- **尚未由 Kotlin/JVM 调用 JNI 方法**；需要 Android UI 工程后做第一次真实 JNI 调用。
- 当前 Android 后端只开放文件工具：`read`、`ls`、`apply_patch`，以及可用的 `grep`/`find`。`NoShell` 明确不暴露 `bash`/`write_stdin`，提示词也会说明 shell 不可用。

## 构建与设备 ABI 测试

```bash
scripts/android-jni.sh

# 没有设备时只构建并检查符号；CI 可要求必须跑设备
REQUIRE_DEVICE=1 scripts/android-jni.sh
```

脚本会定位 NDK、设置 `CGO_ENABLED=1 GOOS=android GOARCH=arm64`、构建
`buildmode=c-shared`，检查 `Java_com_getan_golder_mobile_GolderNative_open`
符号，然后用 NDK clang 编译 `internal/mobilebridge/testdata/abi_loader.c`，
推到设备上加载并运行。loader 不发起模型请求，因此不需要网络或凭据。

## Kotlin 侧形状

```kotlin
package com.getan.golder.mobile

object GolderNative {
    init { System.loadLibrary("golder_mobile") }

    external fun version(): String
    external fun lastError(): String
    external fun open(configJson: String): Int
    external fun start(handle: Int, prompt: String): Int
    external fun poll(handle: Int, max: Int): String?
    external fun snapshot(handle: Int): String?
    external fun cancel(handle: Int)
    external fun close(handle: Int)
}
```

`open` 接收 JSON：

```json
{
  "workspace": "/data/user/0/com.getan.golder/files/workspace",
  "model": "gpt-4o-mini",
  "provider": "openrouter",
  "baseUrl": "",
  "protocol": "",
  "apiKey": "…",
  "systemPrompt": "",
  "contextWindow": 128000
}
```

密钥应由 Android Keystore 保护，并在调用前才读出；不要写日志、事件或错误。`open` 返回 0 表示失败，错误文本从 `lastError()` 读取。`poll` 返回事件 JSON 数组，空队列是 `[]`；`snapshot` 返回 `{running, runId, seq, pending, messages}`。

## 尚未解决的边界

- 目前是**文件型后端**，不是完整的 `auto` 权限链：没有 shell、浏览器、Git、commit/push 或网络工具。不能把这个后端说成已经支持完整任务自动化。
- JNI 方法需要 Kotlin 工程和 JVM 才能验证；C ABI 加载成功不等于 JNI 调用已验证。
- 会话持久化、后台恢复和结果未知时的写操作保护仍待实现。

## 为什么先做 JNI 而不是 UI

当前开发机没有 JDK/Gradle，无法构建 Kotlin/Compose 工程；NDK 已安装，因此先把可独立验证的 ABI 和加载路径做实。Android UI 工程需要先补齐 JDK/Gradle 工具链，再用同一套 JSON 契约接入。
