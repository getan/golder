# Android JNI 桥接

`cmd/golder-mobile` 把移动会话边界编译成 `libgolder_mobile.so`，通过 JNI 供 Kotlin 调用。服务层在 `internal/mobilebridge`，不依赖 JVM，因此主机测试和 Android 设备测试可以分开进行。当前走的是 **in-process JNI** 路线，不是把 Go 可执行文件作为子进程启动；两条路线的取舍见文末。

## 当前状态

- Go 共享库可用 NDK 构建，JNI 和 C ABI 符号都已导出。
- 设备上的 C ABI loader 已实际加载该库，打开一个文件型会话、读取快照并关闭。
- Kotlin/JVM 的 JNI 调用已由 `JniSessionTest` 验证：版本、打开、启动、轮询、流式文本和 `run_end` 都通过；模型是设备内回环的假 OpenAI SSE 服务，不依赖真实凭据。
- 当前 Android 后端开放文件工具（`read`、`ls`、`apply_patch`，以及可用的 `grep`/`find`）和结构化 `git` 工具。Git 使用纯 Go 实现，本地操作限定在工作区；`clone`/`fetch`/`push` 必须拿到宿主网络授权，否则失败关闭。`NoShell` 明确不暴露 `bash`/`write_stdin`，提示词也会说明 shell 不可用。

## 构建与设备 ABI 测试

```bash
scripts/android-jni.sh

# 没有设备时只构建并检查符号；CI 可要求必须跑设备
REQUIRE_DEVICE=1 scripts/android-jni.sh
```

App 级 JNI 测试：

```bash
scripts/android-app-test.sh
REQUIRE_DEVICE=1 scripts/android-app-test.sh
```

该脚本用 `adb push` + `pm install` 安装 app 和测试 APK，绕过 MIUI 每次
`adb install` 的“USB安装提示”。测试 APK 运行 `JniSessionTest`。

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
    external fun catalog(): String
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
  "model": "gpt-6.1-sol",
  "provider": "openai",
  "thinkingLevel": "high",
  "permissionMode": "auto",
  "baseUrl": "",
  "protocol": "",
  "apiKey": "…",
  "systemPrompt": "",
  "contextWindow": 128000,
  "stateDir": "/data/user/0/com.getan.golder.mobile/files/state",
  "sessionId": "mobile-…",
  "gitAuthorName": "Your Name",
  "gitAuthorEmail": "you@example.com",
  "gitToken": "",
  "allowNetworkGit": true,
  "debugLog": ""
}
```

密钥应由 Android Keystore 保护，并在调用前才读出；不要写日志、事件或错误。`open` 返回 0 表示失败，错误文本从 `lastError()` 读取。`poll` 返回事件 JSON 数组，空队列是 `[]`；`snapshot` 返回 `{running, runId, seq, pending, messages}`。

`catalog()` 在打开会话前返回 provider/model/reasoning 目录：
`{providers:[{name, defaultModel, models, reasoning}], defaultProvider, defaultModel}`。
移动 UI 用它生成一个组合选择器；当前 `models` 含注册表默认模型，其他模型 id 通过
UI 的 “Custom model…” 输入。它是静态目录，不触发网络。

移动 UI 会把 provider/model/reasoning/Base URL 保存到普通 SharedPreferences，
把模型 API key 和 GitHub token 分别用 Android Keystore 加密后保存；重启后恢复。
Connection 卡片只显示 `API key ••••••`，不把密钥明文放回界面。

`debugLog` 可选，只在宿主调试时设置；它记录循环事件类型和终止错误，不记录提示词、工具参数/结果或密钥。

`stateDir` 可选，设置后会话保存在 `<stateDir>/sessions/<sessionId>.jsonl`；同一 `sessionId` 重新打开会恢复历史。锁文件防止同一会话被两个进程同时写入。当前采用整文件原子重写，后续可换成增量追加。

`gitAuthor*` 决定提交身份；为空时读取仓库配置，最后才使用本地 fallback。`gitToken` 只用于 HTTPS 远端，不会出现在工具输出或错误里。`allowNetworkGit` 是**会话级**宿主能力：传 false 时 `clone`/`fetch`/`push` 明确失败关闭，本地 `commit`/`branch`/`status`/`log` 可用。Android UI 在打开会话时固定传 true，即 Git 网络默认开启，不再提供复选框；宿主仍可显式传 false 关闭。逐次网络授权的审批卡片仍未实现。

GitHub 认证方案见 [GitHub 认证](github-auth.md)。当前决定只使用用户自备的 fine-grained PAT，不实现 GitHub App/device flow/token broker；UI 已支持 PAT 并用 Android Keystore 加密保存。

## 尚未解决的边界

- 目前是**文件型 + 本地 Git 后端**，网络 Git 由会话级能力控制（移动 UI 默认开启）；还不是逐次审批的完整 `auto` 权限链，没有 shell 或浏览器。不能把这个后端说成已经支持完整任务自动化。
- 会话持久化和重启恢复已有基础实现；后台执行限制、结果未知时的写操作保护和多会话列表仍待实现。

## JNI 还是子进程

当前实现是 **in-process JNI**：Go 编成 Android `c-shared` 库，Kotlin
`System.loadLibrary` 后在同一个 app 进程里调用 `open/poll/start`。它没有独立
Go 进程，也没有 stdin/stdout 或 JSON-RPC 的 IPC。

另一条路线是把 Go 编成独立可执行文件，由 Kotlin 启动子进程，用 JSON 行协议
通信。两条路线的取舍：

- JNI：延迟低、没有 IPC 序列化和子进程生命周期，APK 只需一个 `.so`；但 Go
  panic 会影响 app 进程，JNI 线程/引用和 cgo ABI 更敏感，崩溃隔离弱。
- 子进程：崩溃隔离、可以单独替换/调试可执行文件；但 Android 对打包二进制的
  执行限制、IPC 协议、进程回收和双份内存都更复杂，启动也更重。

当前选择 JNI，是因为移动工作区需要频繁的工具事件和低延迟轮询；文件/网络权限
仍由 Go 侧 gate 控制，JNI 不是权限边界。

## 工具链与 UI

JDK 21 使用 Homebrew 的标准安装，Gradle 8.14.6 固定在
`~/Library/Android/gradle/gradle-8.14.6`；Android SDK 继续位于
`~/Library/Android/sdk`。Kotlin/Compose 工程在 `mobile/android`，通过同一套
JSON 契约接入。
