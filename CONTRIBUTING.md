# 贡献指南

欢迎参与 OmniToken。本文件是**协作流程的唯一权威**:分支怎么开、PR 提给谁、什么算做完。
写代码时的项目约定(架构分层、正确性铁律、解析器注意事项)见 [AGENTS.md](AGENTS.md) ——
那份文件同时也是给 AI 编码助手读的。

## 环境

只需要 Go(版本见 `go.mod`)。纯 Go 无 CGO,不需要额外的系统依赖。

```sh
git clone https://github.com/SuooL/OmniToken.git
cd OmniToken
make check      # 跑一遍,确认环境就绪
```

调试时也可以只跑其中一段:`make vet`、`make test`、`make cover`(测试 + 覆盖率门禁)、`make build`。

## 分支模型

三条分支,职责固定:

| 分支 | 角色 |
|------|------|
| `main` | 发布分支。只在里程碑时由 `dev` 合入,打 tag + GitHub Release。 |
| `dev` | 集成分支。所有功能都合到这里。**PR 的目标始终是 `dev`。** |
| `feature/<描述>` | 功能 / 修复 / 热修。从最新的 `dev` 切出。 |

```
feature/xxx  →  PR  →  dev  →  CI 绿  →  自动合并 + 删分支
                                      →  [里程碑] 手动发版:dev → main(tag + Release)
```

**不要直接推 `dev` 或 `main`。** 两条分支都开了保护,所有改动都要走 PR。
热修也用 `feature/*`,没有单独的 `hotfix/` 或 `release/` 分支。

## 提一个 PR

```sh
git switch dev && git pull
git switch -c feature/你的改动

# ... 写代码 ...

make check                  # 必须绿,这和 CI 跑的是同一条命令
git push -u origin feature/你的改动
gh pr create --base dev     # 或在 GitHub 网页上开
```

CI 会在 PR 上跑 `make check`。绿了之后自动合并、自动删除分支。

## 什么算做完

- [ ] 实现了 issue / PR 描述里说的行为
- [ ] **`make check` 通过** —— 包含 `go vet`、全部测试、覆盖率门禁、构建
- [ ] 新行为有测试覆盖
- [ ] PR 目标是 `dev`
- [ ] 只改了这次改动需要的文件

### 关于覆盖率门禁

`make check` 只对**生成 event_id 的三个包**(`parser/codex`、`parser/claudecode`、`agent`)
强制覆盖率下限。原因是 event_id 是去重的唯一依据 —— 它出错会让已入库的历史数据重复计数,
**改回代码也修复不了数据**。

其余包不卡覆盖率。改 HTTP handler、网页面板、配置解析不会因为覆盖率数字被拦下来。

阈值只定义在 [`scripts/coverage-gate.sh`](scripts/coverage-gate.sh) 一处。
如果你的改动确实需要调整某个下限,在 PR 里说明理由。

## 代码风格

跟着 Go 的常规来:`gofmt`(编辑器一般会自动做)、`go vet` 干净。
没有额外的 lint 硬性要求 —— 与其堆规则,不如把精力放在 [AGENTS.md](AGENTS.md) 讲的那几条正确性铁律上。

## 提交信息

用 `类型: 描述` 的形式,类型取 `feat` / `fix` / `docs` / `refactor` / `test` / `chore`。
发版时的 changelog 直接由提交信息生成,所以写清楚「改了什么」比写「怎么改的」有用。

## 发版

由维护者手动触发 GitHub Actions 的 **Release** 工作流,选 `major` 或 `minor`。
它会把 `dev` 合进 `main`、算出下一个 `vX.Y.Z`、打 tag、生成 changelog、创建 Release。

## 报告问题

开 issue 时请附上:你在跑哪个子命令(`serve` / `agent`)、`omnitoken` 版本、
相关的日志片段。**贴之前记得抹掉 token、主机名、API key 这类信息。**

## 隐私底线

这个项目只采集用量元数据,**永不读取对话内容**,API key 只存哈希指纹。
任何会碰到对话正文或明文凭据的改动都不会被接受。
