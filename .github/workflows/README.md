# GitHub Actions

支撑三分支模型(`main` = 发布,`dev` = 集成,`feature/*` = 开发)。
完整协作流程见 [CONTRIBUTING.md](../../CONTRIBUTING.md)。

## 工作流

| 文件 | 触发 | 做什么 |
|------|------|--------|
| `ci.yml` | PR → `dev` | 跑 `make check`;绿了就开启 auto-merge。 |
| `release.yml` | 手动(`workflow_dispatch`) | 合 `dev` → `main`、算下一个 `vX.Y.Z`、打 tag、生成 changelog、创建 Release。 |
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

给 **`dev`** 和 **`main`** 都加规则:

- **Require a pull request before merging**
- **Require status checks to pass before merging** → 把 `ci.yml` 的 **`verify`** 这个 job
  设为 **required**(仅 `dev` 需要)。

  ⚠️ **没有 required check,auto-merge 会在 CI 跑完之前就合并**,门禁形同虚设。
  这是这套配置里最容易漏掉、后果最严重的一步。

- 建议同时开启 require branches to be up to date before merging。

> `verify` 这个 check 名要等 CI **至少跑过一次**之后才能在 UI 的下拉框里选到。
> 如果配置时找不到它,先开一个 PR 触发一次 CI,再回来补这项设置。

> 单人仓注意:分支保护默认也会挡住仓库 owner 的直推,这是预期行为 ——
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
