# GitHub Actions

支撑三分支模型(`main` = 发布,`dev` = 集成,`feature/*` = 开发)。
完整协作流程见 [CONTRIBUTING.md](../../CONTRIBUTING.md)。

## 工作流

| 文件 | 触发 | 做什么 |
|------|------|--------|
| `ci.yml` | PR → `dev` | 跑 `make check`;绿了就开启 auto-merge。 |
| `release.yml` | 手动(`workflow_dispatch`) | 合 `dev` → `main`、算下一个 `vX.Y.Z`、交叉编译五平台二进制并注入该版本号、打 tag、生成 changelog、发布带产物与 `SHA256SUMS` 的 Release。 |
| `prune-branches.yml` | 每周定时 + 手动 | 兜底删除已合入 `dev` 的 `feature/*` 远程分支。 |

`ci.yml` 调用的是 `make check` —— 和贡献者本地跑的**完全同一条命令**。
不要把构建/测试/覆盖率命令复制进工作流:两份副本一定会漂移,
到时候「本地绿、CI 红」就会变成常态。覆盖率阈值只存在于 `scripts/coverage-gate.sh`。

## 必须配置的仓库设置

auto-merge 和合并门禁**只有在仓库配置到位时才真正生效**。在 **Settings** 下:

### 1. General → Pull Requests

- 开启 **Allow auto-merge** —— `ci.yml` 的 auto-merge 步骤依赖它。
- 开启 **Automatically delete head branches** —— PR 合并后自动删除 feature 分支。

### 2. Branches → Branch protection rules

两条分支的规则**不一样**,不要照抄。

#### `dev` —— 人和 AI 都往这里提交,需要完整门禁

- **Require a pull request before merging**(approval 数设 **0**:单人仓无法自我批准,
  设 1 会让 auto-merge 永远卡住)
- **Require status checks to pass before merging** → 把 `ci.yml` 的 **`verify`** 设为 **required**

  ⚠️ **没有 required check,auto-merge 会在 CI 跑完之前就合并**,门禁形同虚设。
  这是这套配置里最容易漏掉、后果最严重的一步。

- 建议同时开启 require branches to be up to date before merging。

> `verify` 这个 check 名要等 CI **至少跑过一次**之后才能在 UI 的下拉框里选到。
> 如果配置时找不到它,先开一个 PR 触发一次 CI,再回来补这项设置。

#### `main` —— 只禁强推与删除,**不要**要求 PR

- 关闭 Require a pull request before merging
- 保留 **禁止 force push**、**禁止删除分支**

⚠️ **给 `main` 加 "Require a pull request" 会让发版直接失败。**
`release.yml` 是按设计直接 push `main` 的(合并 dev → 打 tag → 发布),
开了这条规则后 push 会被拒:

```
remote: error: GH006: Protected branch update failed for refs/heads/main.
remote: - Changes must be made through a pull request.
```

这不是漏配,是两条规则本身冲突。`main` 的唯一写入者就是发版工作流,
而它自身已经是门禁(手动触发 + 构建必须先通过),再套一层 PR 要求并不增加安全性。
真正要防的是历史被改写和分支被删,这两条保留着。

> 单人仓注意:`dev` 的保护同样会挡住仓库 owner 的直推,这是预期行为 ——
> 所有改动都该走 PR。

## Secrets

不需要手动配置任何 secret。`GITHUB_TOKEN` 由 Actions 自动提供,
`ci.yml`(auto-merge)、`release.yml`、`prune-branches.yml` 都用它。

## 分支自动清理

两层:

1. 仓库设置 **Automatically delete head branches** —— PR 一合并就立刻删。
2. `prune-branches.yml` —— 每周兜底,删掉漏网的已合并 `feature/*`。

第二层用 `gh pr list --state merged` 而不是 git 的 ancestry 判断,
因为 `ci.yml` 用 `--squash` 合并会生成全新 commit,
原分支的提交在 `dev` 上根本不可达,ancestry 检查会认为它「没合并过」而永远不清理。
