# Golder Android

原生 Kotlin/Compose 前端，通过 JNI 调用 `libgolder_mobile.so`。当前后端是文件型会话加结构化 Git：文件工具、可用的 `grep`/`find`，以及纯 Go 的 `git` 工具。没有 shell 或完整 `auto` 权限链；远程 Git 需要宿主网络授权。

## 构建

需要：

- JDK 21（Homebrew `openjdk@21`）
- Gradle 8.14.6（固定安装在 `~/Library/Android/gradle/gradle-8.14.6`，脚本会自动发现）
- Android SDK：platform 34、build-tools 35.0.0、NDK

```bash
scripts/build-android-app.sh            # 构建并安装到已连接设备
scripts/build-android-app.sh --no-install
scripts/android-app-test.sh             # 构建、安装并跑 JNI 设备测试
```

安装使用 `adb push` + `pm install`，而不是 `adb install`：在 MIUI/HyperOS
上后者每次都会弹“USB安装提示”，前者在已启用 USB 调试的情况下直接安装。

脚本会先用 NDK 构建 `libgolder_mobile.so`，放进 `app/src/main/jniLibs/arm64-v8a/`，再执行 Gradle 构建。首次构建需要下载 Gradle/Android 依赖。

## 当前界面

- 工作区、模型、Base URL、API key 输入；密钥仅保存在内存中。
- 打开工作区、发送任务、停止。
- 流式文本、thinking、工具开始/结束和运行状态。
- 会话按工作区保存在 app 私有目录，重新打开会恢复历史并回放文本。
- 本地 Git：status/diff/log/add/commit/branch/checkout/remote；clone/fetch/push 在网络授权前失败关闭。
- “Allow Git network” 是打开会话前的复选框；它是会话级授权，不是逐次审批。
- `scripts/android-app-test.sh` 用设备内回环假模型验证完整 JNI 路径，不需要真实凭据。

## 边界

- 还没有逐次审批卡片、多会话列表、后台执行恢复或 iOS。
- JNI 版本在启动时调用并写入 logcat（`GolderJNI`），用于确认 Kotlin→Go 路径。
