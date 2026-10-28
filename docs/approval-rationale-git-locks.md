# 审批理由丢失与 Git tab 锁竞争：调查记录

## 审批卡片为什么只剩“已授权：联网”

这不是 LLM 必然没有给解释，而是展示链有一条确定的丢失路径：

1. `internal/judge/llm.go` 将评审返回的 rationale 放进 `Verdict.Reasons`。
2. `consent.go` 的 `consentRationale` 原来在存在额外 capability needs 时，直接用
   “已授权：…”覆盖了评审理由。auto 批准和 ask 确认后的通知都经过这里。
3. `note.go` 的 FormatNote 又把以“已授权：”/“granted:”开头的 rationale 当作
   重复能力清单隐藏。最后只留下结构化的 Granted 后缀。

因此，一次同时请求 sandbox escape 和 network 的调用可以显示 via llm，却没有
LLM 的自然语言解释。修改只调整理由保留和去重，不改变风险等级、授权能力或执行包络：

- 优先保留第一个非空 reviewer reason；没有 reason 才生成能力清单兜底。
- Granted 仍独立展示；仅去掉与清单完全一致的重复文字，不按前缀删除完整解释。
- 中文/英文、auto/ask，以及以“已授权：”开头的真正解释都有回归用例。

源码修复需重新构建并运行新版本后才影响当前终端里的审批卡片。

## Git tab 偶尔报锁，不能一概认为正常

### 正常的竞争边界

Git 写操作需要互斥。终端/Agent 正在 add、commit、reset 或切分支时，另一个写操作
遇到 index.lock 是合理的保护机制。不能擅自删除锁，也不能不分操作地重试有副作用命令。

但只点击一个文件查看 diff，本来不应为了刷新用户 index 而和另一个读取者争锁。

### 当前固定依赖中的可复现路径

核对的是 `github.com/getan/tuios-mini v0.10.14`，不是修改 Go module cache：

```text
internal/app/git_status.go: ActivateGitEntry
  → reviewLoadCmd → review-diff
  → internal/session/verb_review.go: review.IndexOptions
  → internal/review/stage.go: IndexTree
  → git write-tree（使用工作区的真实 index）
```

`review.gitEnv` 虽然已设置 GIT_OPTIONAL_LOCKS=0，但 write-tree 的锁不是 optional。
它还可能把 cache-tree 扩展写回真实 index。快速先后打开两个 diff（旧请求只按
generation 丢弃结果，没有因此取消后端 Git 命令），或者同时有后台刷新/外部写者，
都有竞争窗口。该版本 `internal/gitstatus.Read` 的 status 子进程也没有禁用 optional locks。

这解释了“平时可用，少数点击偶发”的一种具体原因。不过，没有用户那次完整的错误
路径，不能把所有 Git 锁提示都认定为它：真实 index.lock、临时 index 的锁以及 ref 锁
需要区分。

### 隔离实验

在自建临时仓库中，使用 Git 2.54.0 (Apple Git-157) 观察到：

| 操作 | 结果 |
|---|---|
| `git --no-optional-locks write-tree` | 仍会修改真实 index 的 cache-tree 数据 |
| 自建 fixture 的 index.lock 存在时运行同一命令 | 失败，错误明确提到 index.lock |
| 复制 index，改用 GIT_INDEX_FILE 指向副本后 write-tree | 原锁存在时仍成功 |
| 实验后的真实 index 与锁 marker | 未被副本操作改动 |

实验只创建、清理自己拥有的临时 fixture，没有删除用户仓库的锁文件。

### 本轮修复和依赖边界

Golder 自己的 Agent 状态栏 `internal/cli/tui/gitinfo.go` 也使用普通 git status。
本轮给这个后台读取加上 `--no-optional-locks`，并测试它不会刷新共享 index，
即使环境中显式设置 GIT_OPTIONAL_LOCKS=1。

Git tab 的完整修复已在 tuios-mini `v0.10.15` 发布，提交
`854638ee8e586bad5cbcfcc96a086e8c4ee4b717`，golder 的 go.mod/go.sum 已更新到该 tag。
IndexTree 在独立 index 副本上 write-tree，保留 staged/intent-to-add/split-index
等语义；副本放在原 index 旁以解析 shared index，写入时仅对副本禁用 split index。
后台 status 同样禁用 optional locks。没有改写 module cache 中的源文件。

回归覆盖原锁存在、四个 worker 的并发读取、首次提交、linked worktree、split/sparse
和 v4 index、intent-to-add、相对路径、冲突/损坏/取消时的清理。真实 index 的字节和
mtime、锁 marker 与工作文件保持不变，只清理自己拥有的临时副本及其锁。

`internal/workspace/git_preview_test.go` 还通过 golder 的真实嵌入式 daemon 调用
review-diff，核对 staged/unstaged 内容并发读取。旧 `v0.10.14` pin 的负对照明确报
index.lock；修复实现通过。实际 commit/stage/discard 仍遵守 Git 的锁，不自动重放。
已经运行的 workspace daemon 不会热加载依赖，需使用新版本重新启动才能生效。
