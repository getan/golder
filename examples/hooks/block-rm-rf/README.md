# block-rm-rf — PreToolUse

拦截危险的 `rm -rf` 命令，在 `bash` 工具执行前阻断。

## 原理

- 事件：`PreToolUse`，`matcher: "bash"`（只对 bash 工具生效）。
- golder 把工具入参以单行 JSON 写入 hook 的 stdin；hook 用 `jq` 读出 `.tool_input.command`。
- 命中 `rm -rf` 变体时 `exit 2` 阻断，stderr 作为阻断原因返回给 Agent；否则 `exit 0` 放行。

## 使用

```bash
chmod +x examples/hooks/block-rm-rf/hook.sh
```

把 `config.json` 的 `hooks` 段合并进 `~/.golder/config.json`（全局）或受信任项目的 `./.golder/config.json`（项目级）。
命令路径这里用 `$GOLDER_PROJECT_DIR` 演示；实际使用时改成脚本的绝对路径或你放置 hook 的位置。

## 依赖

- `jq`
