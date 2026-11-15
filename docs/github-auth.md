# GitHub 认证：网络开关与 token 是两件事

`Allow Git network` 只是允许这次会话为 Git 使用网络，它不证明你是谁。私有仓库的 clone/fetch/push 还需要 GitHub 认证。

## 当前可用：fine-grained PAT（最简单）

这是目前已经接入的方式，不需要后端，也不需要注册 OAuth/GitHub App：

1. 打开 GitHub → Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate new token。
2. 选择要授权的仓库（或全部仓库），设置较短有效期。
3. 仓库权限至少给 **Contents: Read and write**；只读任务给 Read。需要后续 PR 能力时再加 **Pull requests: Read and write**；基础元数据需要 **Metadata: Read**。
4. 复制 `github_pat_…`，粘贴到 app 的 “GitHub token” 字段；勾选 “Allow Git network” 后打开工作区。

Android 端用 Android Keystore 的 AES-GCM 密钥加密后保存到 SharedPreferences；Keystore 私钥不出设备。token 不写入日志、工具输出或会话文件。清除锁屏/恢复备份可能使密钥失效，此时重新填入即可。

Go 侧通过 HTTPS Basic Auth 使用 token（用户名 `x-access-token`），与 go-git 的现有实现兼容。public 仓库不填 token 也能 clone；私有仓库和 push 必须填。

## 更优雅的长期方案：GitHub App + Device Flow

GitHub 官方现在建议 GitHub App 而非 OAuth App。它的优势正好适合 Golder：

- 细粒度权限，不申请宽泛的 `repo` scope；
- 用户安装时只选择允许 Golder 访问的仓库；
- user access token 默认 8 小时过期，配 refresh token（6 个月）自动续期；
- device flow 不需要 client secret，适合手机/CLI；
- 不需要 Golder 自建后端。

流程：

1. 注册一个 GitHub App（一次性）：
   - Permissions：Contents Read/write、Metadata Read；按需 Pull requests Read/write。
   - 勾选 **Enable device flow**。
   - 记录公开的 **Client ID**（不是 App ID，也不需要把 client secret 放进 app）。
2. app 请求 `POST https://github.com/login/device/code`（`client_id`，scope 可选 `offline_access`），显示 `user_code` 并打开 `https://github.com/login/device`。
3. 轮询 `POST https://github.com/login/oauth/access_token`，处理 `authorization_pending`、`slow_down`、`expired_token`。
4. 把 `ghu_…` access token 和 `ghr_…` refresh token 存入 Keystore；access token 到期前用 refresh token 换新。
5. Git HTTPS 使用 `x-access-token` + user access token。用户可在 GitHub 设置中随时撤销授权。

### 为什么不是 OAuth App

OAuth App 的 device flow 也能用，且同样不需要 client secret；但它的权限模型是 scope，通常要 `repo`（私有仓库的广泛读写），无法在授权时精确选择仓库。GitHub App 的权限和仓库选择更细，token 也更短命。

### 为什么不是 web OAuth + 回调

那需要可用的 callback 或 client secret，移动端还要处理浏览器回跳和 PKCE；device flow 只需要公开 client_id 和用户输入 8 位代码，复杂度更低。

## 现状与下一步

- 已实现：fine-grained PAT 输入、Android Keystore 加密保存、`gitToken` 传入 Go 的 HTTPS Git 认证、`Allow Git network` 会话开关。
- 未实现：GitHub App device flow、token 自动刷新、仓库选择 UI、逐次网络审批。

如果要做“Sign in with GitHub”，只需先注册上述 GitHub App 并提供公开 Client ID；客户端不需要、也不应该内置 client secret。
