# GitHub 认证：网络开关与 token 是两件事

`Allow Git network` 只是允许这次会话为 Git 使用网络，它不证明你是谁。私有仓库的 clone/fetch/push 还需要 GitHub 认证。

## 当前决定：个人版 PAT，多用户生产版 GitHub App + broker

对个人手机 Agent 和早期验证，我们采用 **fine-grained PAT**。原因是 PAT 不需要注册 GitHub App、不需要自建后端、不需要处理 refresh token 轮换，用户可以直接限定仓库和权限；在大陆网络环境下，也少一层 GitHub 网页授权流程。

如果产品要面向很多用户，则不能只依赖 PAT：计划采用 **GitHub App + token broker**，让每个用户安装 App、选择仓库，由 broker 签发 1 小时 installation token。PAT 继续保留为企业限制、自托管或完全无后端场景的 fallback。两种模式共用现有的 `gitToken`/HTTPS BasicAuth 路径。

GitHub App 不是从应用商店安装的手机 App，而是注册在 GitHub 账号/组织下的服务集成：用户在 GitHub 网页上“安装”它并选择仓库，不需要安装 APK。大陆用户访问 GitHub 网页仍可能需要自己的网络条件，这与是否使用 GitHub App 无关。将来如果 Golder 面向多用户分发、需要更细的仓库安装授权和短期 token，再考虑 GitHub App。

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
5. Git HTTPS 的**官方支持路径**是 installation access token（`ghs_…`）：它绑定一次安装和选定仓库，1 小时过期，使用 `x-access-token` 作为用户名、token 作为密码。生成它需要 GitHub App 私钥，私钥不能放进手机，所以这条路径需要一个小型 token broker 后端。

GitHub App 的 user access token（`ghu_…`）主要面向 REST API。社区有人报告它能直接用于 Git clone，但它不是官方 Git 认证文档的主路径；客户端-only 方案必须先实测，不能直接当作生产保证。

### 面向多用户的现实边界

- 每个用户/组织安装同一个 **public GitHub App**，并选择授权仓库；不同安装之间互相隔离。
- 每个安装只能拿到被安装时允许的仓库；token 不能越过用户权限、分支保护或组织 SAML 策略。
- 如果做客户端-only device flow，则依赖 `ghu_` 的 Git 支持（需实测）；如果要用官方保证的 `ghs_`，就需要后端保管私钥并签发 1 小时 installation token。
- GitHub App 注册/安装/Device Flow 本身免费；可能产生费用的是你自己的 broker 托管。

### 多用户推荐架构：GitHub App + token broker

如果 Golder 要给很多用户使用，推荐把 GitHub App 和手机客户端分开：

```text
用户手机                           Golder broker                    GitHub
   │                                   │                              │
   │ 1. device flow 授权               │                              │
   ├───────────────────────────────────┼─────────────────────────────▶│
   │                                   │                              │
   │ 2. 用户在 GitHub 安装 App          │                              │
   │    并选择仓库                      │                              │
   ├───────────────────────────────────┼─────────────────────────────▶│
   │                                   │ 3. 用 App 私钥签 JWT          │
   │                                   ├─────────────────────────────▶│
   │                                   │ 4. installation token (ghs_) │
   │                                   ◀─────────────────────────────┤
   │ 5. 短时 token（绑定 installation）  │                              │
   ◀───────────────────────────────────┤                              │
   │ 6. git clone/push                  │                              │
   ├───────────────────────────────────┼─────────────────────────────▶│
```

- App 私钥只在 broker，绝不进手机；后端只在签发 token 时参与，不代理 Git 流量。
- broker 把“用户身份 → 安装 → 允许的仓库”映射起来，签发时用 `repository_ids` 再收窄到单个仓库。
- `ghs_` token 1 小时过期；手机在过期前通过 broker 换新。GitHub App 本身免费，成本主要是 broker 托管。
- 不要为所有用户共用一个 owner 的 PAT，也不要把同一个 installation token 分发给多个用户。

如果坚持**完全没有后端**，可以先做设备流拿到 `ghu_`，实测它能否对目标私有仓库执行 Git clone/push；能通过再用于 beta，但生产上仍应把它当作“待验证的边界”，因为官方 Git 认证文档主路径是 installation token。

### 为什么不是 OAuth App

OAuth App 的 device flow 也能用，且同样不需要 client secret；但它的权限模型是 scope，通常要 `repo`（私有仓库的广泛读写），无法在授权时精确选择仓库。GitHub App 的权限和仓库选择更细，token 也更短命。

### 为什么不是 web OAuth + 回调

那需要可用的 callback 或 client secret，移动端还要处理浏览器回跳和 PKCE；device flow 只需要公开 client_id 和用户输入 8 位代码，复杂度更低。

## 现状与下一步

- 已实现：fine-grained PAT 输入、Android Keystore 加密保存、`gitToken` 传入 Go 的 HTTPS Git 认证、`Allow Git network` 会话开关。
- 未实现：GitHub App device flow、token 自动刷新、仓库选择 UI、逐次网络审批。

如果要做“Sign in with GitHub”，只需先注册上述 GitHub App 并提供公开 Client ID；客户端不需要、也不应该内置 client secret。
