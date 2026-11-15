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

- 主界面只显示会话：用户消息在右侧、agent 在左侧，各用不同颜色气泡；工具调用
  默认折叠，只显示真实命令（如 `git clone …`、`read …`）和一行结果摘要。
- `todo` 工具卡片默认展开并用高亮边框显示；只保留最新一份任务清单，完成项
  划线、进行项加粗。
- 顶部状态栏显示当前项目、provider · model · reasoning；设置按钮打开集中配置页。
- 设置页包含 `Provider · Model · Reasoning`、`Connection`（Base URL + API key）
  和 GitHub token；密钥分别由 Android Keystore 加密保存。
- 左滑项目抽屉列出项目并支持新建；每个项目是独立 workspace 根目录，切换后
  自动打开。Android 系统返回手势占用最左边缘，因此从内容区左滑或点汉堡按钮
  打开抽屉。
- Provider/model/reasoning/Base URL 会保存；模型 API key 用 Android Keystore
  加密保存，重启后恢复。Connection 卡片只显示 `API key ••••••`，GitHub token
  也显示掩码。
- 发送/停止任务；项目目录固定在 app 私有目录，不再需要填写路径。
- 流式文本、thinking、工具开始/结束和运行状态；工具结果按命令名展示。
- 会话按工作区保存在 app 私有目录，重新打开会恢复历史并回放文本。
- 本地 Git：status/diff/log/add/commit/branch/checkout/remote；移动 UI 默认开启
  Git 网络（clone/fetch/push），不再提供复选框。它是会话级能力，不是逐次审批。
- clone/fetch 遇到临时 TCP 中断会重试一次；push 不自动重试，避免远端状态未知。
- 当前 Git 工具只把 **workspace 根目录**当作仓库：要提交/推送，必须直接 clone
  到空的 workspace 根目录，或在根目录 init；clone 到子目录后 `add`/`commit`
  会报 “not a Git repository”。
- `scripts/android-app-test.sh` 用设备内回环假模型验证完整 JNI 路径，不需要真实凭据；
  当前设备测试 6/6 通过，包含 provider catalog 和模型 API key 的 Keystore 往返。

## 边界

- 还没有逐次审批卡片、多会话列表、后台执行恢复或 iOS。
- JNI 版本在启动时调用并写入 logcat（`GolderJNI`），用于确认 Kotlin→Go 路径。
