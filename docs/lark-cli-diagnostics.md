# Lark CLI 错误诊断与搜索权限

适用：`golder-lark` **1.5.63 起**。这次只提升业务 CLI 的安全诊断，不改变身份、授权或沙箱规则，也不会自动重放失败调用。

## 2026-11-10 文档搜索排查结论

本次核验的用户身份有效，但已授予的 scope 有 `drive:drive.search:readonly`，没有 `search:docs:read`。

适配器调用的是 `lark-cli drive +search`，即 **Search v2** 的 `/open-apis/search/v2/doc_wiki/search`。官方 CLI v1.0.99 为这个命令声明的 scope 是 **`search:docs:read`**，两项权限不能互相替代。因此当前授权存在一个已确认的权限缺口；“用户认证通过”不代表每个业务 API 的权限都已齐全。

旧 helper 丢弃了 stderr，无法从旧的两个 `CLI_ERROR` 还原当时准确的服务端 code、request ID 或排除额外网络问题。不要把推断出的示例错误码当成已经采集到的原始错误。

处理顺序：

1. 操作者检查飞书开放平台应用是否已开通 `search:docs:read`，并完成租户要求的发布/管理员审批。
2. 再显式通过 GUI 的用户授权入口或 `/mcp setup lark` 更新用户授权，并核对实际 granted scope；应用开通和用户授予是两个独立步骤。
3. 权限补齐后再执行一次限定范围的只读查询。没有必要重置登录、删除状态或换成 bot。

参考：[官方 v1.0.99 的 `DriveSearch` 定义](https://github.com/larksuite/cli/blob/v1.0.99/shortcuts/drive/drive_search.go)。

## 日志中的三种现象不是同一件事

- `doc_search` 的 `CLI_ERROR`：旧版结果不足以区分上游权限、参数或网络错误；新版本解析 stdout **和 stderr** 的结构化公共错误。
- `cli_help` 的 `COMMAND_BLOCKED`：现在 help 接受任意 Lark/Project 业务或 raw 命令路径；只有 `auth/config/install` 仍被本地策略拒绝。raw 执行使用独立的 `cli_raw`。
- 长尾调用的执行状态：`cli_call` 只将固定的 `lark / drive +search` 和 `project / url decode` 认定为已知只读；其他业务调用和全部 `cli_raw` 调用仍按可能写入处理。所有调用都保留 exact-call host policy 授权检查，管理命令和本地文件不走长尾入口。

此外，`mine=true` 表示“我拥有的文档”，不是“我创建的文档”。“我创建的文档”在新版本使用：

```json
{"options":{"created_by_me":true,"page_size":20}}
```

这是限定一页的筛选查询，不是整个云盘的完整清单。空 `query` 可以按过滤条件浏览；适配器使用独立 argv，不经过 shell 分词，空串不会被省略。旧的扁平日志可能把 `"--query", ""` 显示得像缺少值，新诊断会明确标为 `--query=<empty>`。

## 新的安全诊断

失败结果增加 `diagnostics`：

- 实际 helper 版本、退出码（进程取消/启动失败时不虚构退出码）。
- `argv_shape`：业务命令、参数名、显式空串和固定的 `--as=user`；普通参数值、查询文本、正文和 JSON payload 均隐藏。
- 错误来源：`stdout_json`、`stderr_json`、已知参数错误、未结构化 stderr 被隐藏，或 runner/输出预算错误。
- 上游提供时保留公共 error code、错误类型、脱敏 message、log/request ID、HTTP status、missing scopes。
- `required_scopes` 是适配命令的预期权限，不是假称服务器已经确认 missing scopes。
- 捕获的 stdout/stderr 字节数。这是预算内已捕获的长度，不冒充完整原始日志的长度。

**不把完整 raw stderr 直接交给模型，也不默认保存原始日志。** 只提取公共错误字段；脱敏已知应用密钥、token/header、设备码、JWT、私钥和 URL。错误 message 有长度上限。无法安全识别的自由文本 stderr 只报告其存在与退出码；常见 `unknown flag` / `flag needs an argument` 只返回安全的参数名。

授权/setup 的设备码、验证链接和原始输出仍保持在操作者私有通道；`--debug`、`--verbose`、凭据/配置/文件参数不会因为调试而向模型开放。诊断不自动重新登录，不自动追加 `--yes`，也不重试结果不明的写入。

## 升级与验证

更新 Golder 和独立 helper，确认实际运行 helper 为 1.5.63 或更新版本；仅更新 GUI/TUI 而继续使用旧 helper，业务结果中不会出现新诊断字段。正常更新保留来源私有 HOME，不需要清除登录状态。

回归覆盖结构化 stderr、数字错误码、前置 notice、空参数、所有权/创建者筛选、敏感信息隐藏、确认/身份/输出上限拒绝以及中断后不重放。真实账号只核验了授权状态，没有自动开通权限、重新授权、读取文档正文或写入飞书资源。
