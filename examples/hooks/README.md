# golder Hooks 示例

一组可运行的 hook 示例，每个例子一个文件夹，包含：

- `hook.sh` — hook 脚本本体
- `config.json` — 对应的 `hooks` 配置片段
- `README.md` — 原理与用法说明

Hooks 的完整说明（9 个 hook 点、输入 JSON schema、输出协议、matcher 规则、分层配置）见仓库根 [README 的 Hooks 章节](../../README.md#hooks)。

## 例子一览

| 目录 | 事件 | 作用 |
|------|------|------|
| [block-rm-rf](block-rm-rf/) | `PreToolUse` | 在 bash 执行前拦截 `rm -rf` |
| [inject-git-branch](inject-git-branch/) | `UserPromptSubmit` | 把当前 git 分支注入模型上下文 |
| [gofmt-after-write](gofmt-after-write/) | `PostToolUse` | 写/改 `.go` 文件后跑 `gofmt` |

## 快速开始

```bash
# 1. 赋予脚本可执行权限
chmod +x examples/hooks/*/hook.sh

# 2. 把某个例子的 config.json 内容合并进你的配置：
#    - 全局：~/.golder/config.json（对所有项目生效）
#    - 项目：./.golder/config.json（仅当项目受信任时加载）
#    命令路径示例用 $GOLDER_PROJECT_DIR 演示，实际改成绝对路径。
```

## 安全须知

- hook 以你**当前用户身份**执行，拥有你本人的全部权限——只启用你信任的脚本。
- 写入 hook stdin 的 payload **不含任何 API Key 或凭证**。
- 项目级（`./.golder/config.json`）hook 仅在项目**受信任**时加载（`--approve` 或信任存储记录），否则一律忽略，避免克隆仓库即执行任意命令。
