# 贡献指南

欢迎参与 OmniToken。

## 环境

只需要 Go(版本见 `go.mod`)。纯 Go 无 CGO,不需要额外的系统依赖。

```sh
git clone https://github.com/SuooL/OmniToken.git
cd OmniToken
make check      # 跑一遍,确认环境就绪
```

## 开发约定

分支模型、PR 流程、验收标准、提交信息格式,以及项目的正确性铁律,全部在
**[CLAUDE.md](CLAUDE.md)**。规则只维护在那一处 —— 它同时被 Claude Code 自动加载,
人和 AI 共用同一份,不会出现两套说法。

开 PR 前请通读它,尤其是 event_id 幂等那一节:它决定了哪些改动需要额外小心。

## 发版

由维护者手动触发 GitHub Actions 的 **Release** 工作流,选 `major` 或 `minor`。
它会把 `dev` 合进 `main`、算出下一个 `vX.Y.Z`、打 tag、生成 changelog、创建 Release。

## 报告问题

开 issue 时请附上:你在跑哪个子命令(`serve` / `agent`)、`omnitoken` 版本、
相关的日志片段。**贴之前记得抹掉 token、主机名、API key 这类信息。**

## 隐私底线

只采集用量元数据、永不读取对话内容、API key 只存哈希指纹。
任何会碰到对话正文或明文凭据的改动都不会被接受。
