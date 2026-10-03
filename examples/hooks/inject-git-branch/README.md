# inject-git-branch — UserPromptSubmit

每次提交 prompt 时，把当前 git 分支注入到模型上下文，让 Agent 知道你在哪个分支上工作。

## 原理

- 事件：`UserPromptSubmit`（工具无关，忽略 matcher，全部触发）。
- hook `exit 0` 并在 stdout 打印 JSON；golder 解析其中的 `additionalContext`，仅追加到**本轮**模型输入。
- 非 git 目录下输出 `no-git`，不会报错。

## 使用

```bash
chmod +x examples/hooks/inject-git-branch/hook.sh
```

把 `config.json` 的 `hooks` 段合并进 `~/.golder/config.json` 或受信任项目的 `./.golder/config.json`。

## 依赖

- `git`（可选；缺失时回退为 `no-git`）
