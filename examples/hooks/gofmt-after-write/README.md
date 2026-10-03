# gofmt-after-write — PostToolUse

在 `write`/`edit` 工具写入 `.go` 文件后，自动跑 `gofmt -w` 格式化。

## 原理

- 事件：`PostToolUse`，`matcher: "write|edit"`（任一工具命中）。
- 工具执行**后**触发；golder 把工具入参与返回以 JSON 写入 stdin。
- hook 读出写入路径（`.tool_input.path` 或 `.tool_input.file_path`，不同工具键名不同，做了兜底），命中 `.go` 就格式化。属于观察型副作用，始终 `exit 0`。
- `timeout: 30` 覆盖默认 60s 超时。

## 使用

```bash
chmod +x examples/hooks/gofmt-after-write/hook.sh
```

把 `config.json` 的 `hooks` 段合并进 `~/.golder/config.json` 或受信任项目的 `./.golder/config.json`。

## 依赖

- `jq`
- `gofmt`（缺失时静默跳过）
