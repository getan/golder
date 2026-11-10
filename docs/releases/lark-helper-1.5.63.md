# golder-lark 1.5.63

独立 helper 版本；随后续 Golder 构建分发，不表示已经发布了新的 Golder tag。

- 解析 stdout/stderr 中的公共结构化 CLI 错误，保留数字 code、脱敏 message、错误类型、log/request ID、HTTP status 和 missing scopes。
- 返回 helper 版本、退出码、脱敏 argv 结构及捕获字节数；未知自由文本仍不直接透给模型，不新增原始凭据日志。
- `doc_search` 增加 `created_by_me`，明确 `mine` 的所有者语义及 Search v2 的 `search:docs:read` 要求。
- 固定的 `drive +search` 长尾调用按只读报告执行状态，不放松 exact-call 授权或其他长尾写入保护。
- 不自动登录、扩权、换 bot、追加 `--yes` 或重放失败写入；保留原有私有 HOME 和授权状态。

排查过程、适用范围与升级说明见 [CLI 错误诊断](../lark-cli-diagnostics.md)。
