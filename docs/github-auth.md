# GitHub 认证：纯客户端 Fine-grained PAT

移动 UI 中 Git 网络默认开启；它只允许会话让 Git 访问网络，不是 GitHub 身份认证。
私有仓库 clone 和任何 push 还需要用户自己的 GitHub token。宿主仍可用
`allowNetworkGit=false` 关闭网络 Git。

## 决策：只用 Fine-grained PAT，不做 GitHub App/Broker

产品保持**纯客户端**：

- 不注册 GitHub App；
- 不实现 device flow；
- 不运行 token broker 或任何后端；
- 每个用户自己创建 fine-grained PAT，并填入 App。

原因是 GitHub App 的官方 Git 路径需要 installation access token（`ghs_…`），而生成它必须用 GitHub App 私钥签 JWT。私钥不能放进 APK，只能放到服务端；这会引入一个新的、持续可攻击的后端和密钥托管责任，不符合当前产品取舍。

## 用户如何配置

1. GitHub → Settings → Developer settings → Personal access tokens → **Fine-grained tokens** → Generate new token。
2. 选择允许访问的仓库，设置较短有效期。
3. 权限：
   - **Contents: Read and write**：clone/push；
   - **Metadata: Read**：基础必需；
   - 需要 PR 时再加 **Pull requests: Read and write**。
4. 复制 `github_pat_…`，粘贴到 App 的 **GitHub token** 字段。
5. 在 App 的 **Connection** 入口填模型 API key（不是 GitHub token），GitHub token
   单独填在 **GitHub token** 框；Git 网络默认开启，直接打开工作区。

public 仓库不填 token 也能 clone；私有仓库和 push 必须填。两者不是一回事：网络开关控制是否允许联网，token 控制以谁的身份访问 GitHub。

## 安全责任边界

App 负责：

- **绝不把 token 打包进 APK、源码或构建产物**；
- token 只来自用户输入；
- 用 Android Keystore 的 AES-GCM 密钥加密后保存；
- Keystore 密钥不可导出，SharedPreferences 只保存 IV 和密文；
- `allowBackup=false`，token 不进入系统云备份；
- 不把 token 写入日志、事件、工具输出或会话文件；
- 提供 “Forget GitHub token” 清除本地保存。

用户负责：

- 创建 fine-grained PAT，限制仓库、权限和有效期；
- 不把 token 发给别人；
- 丢失或泄露时在 GitHub 上撤销；
- 设备本身的安全（锁屏、恶意软件、root 等）。

App 不代管 token、不自动续期、不集中撤销，也不能保证被攻破的设备上的 token 安全。这是纯客户端模式的明确代价。Android Keystore 是否使用 TEE/StrongBox 取决于设备；root 或系统被攻破时，应用层的加密不能提供绝对保证。

## 多用户模式

- 每个用户安装同一个 APK，配置**自己的** fine-grained PAT；
- 不存在共享 token，也没有服务端保存所有用户的 token；
- 每个用户的 token 只能访问其自己选择的仓库；
- 用户撤销 token 或设置过期后，App 下一次请求会被 GitHub 拒绝；
- 某些组织禁止 PAT、要求 SSO 或只允许 GitHub App 时，纯 PAT 模式无法覆盖；这是已知边界。

这适合“每人本地运行自己的 Agent”的分发方式：没有中心账号系统，也没有中心密钥。

## 当前实现与验证

- Android UI 已有 GitHub token 输入框、Keystore 加密保存和清除按钮；
- Bridge 的 `gitToken` 通过 HTTPS Basic Auth 传给纯 Go Git 工具，用户名 `x-access-token`；
- Android 设备测试覆盖 Keystore 往返和 provider catalog，当前 JNI/Keystore 测试 6/6 通过；
- Git 网络默认开启，不再有 “Allow Git network” 复选框；host 仍可传
  `allowNetworkGit=false` 关闭。

## 不采用的方案（记录）

- **GitHub App + token broker**：需要服务端保管 App 私钥，新增持续攻击面和托管责任；不采用。
- **GitHub App + `ghu_` 纯客户端**：`ghu_` 主要用于 API，直接用于 Git clone/push 不是官方 Git 认证的主路径；不作为生产依赖。
- **OAuth App + device flow**：需要注册 OAuth App，权限通常是宽泛的 `repo` scope，且 Git 使用同样依赖非官方主路径；不采用。
- **把用户 token 内置 APK**：绝对不允许。

## 已知限制

- 每个用户需要手动创建和轮换 PAT；
- 组织可能限制 PAT 或要求 SSO；
- App 还没有单独的 Git 代理设置，GitHub 网络不可达时 token 也无效；
- 逐次网络审批仍未实现，目前是打开会话前的会话级开关。
