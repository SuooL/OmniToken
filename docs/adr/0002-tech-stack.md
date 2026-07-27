# ADR-0002 技术栈:Go 无 CGO 单二进制 + SQLite + 内嵌前端

状态:已采纳(2026-07-26)

## 背景

设备多为 headless 服务器(N1/N2);候选:Go / TypeScript(Node) / Python。
对比项目 token-monitor 因 Electron 形态与服务器环境错配而不可用,是直接反例。

## 决策

- Go,`modernc.org/sqlite`(纯 Go 驱动,**无 CGO**)→ 任意平台交叉编译单文件分发;
- 服务端与 agent 同一二进制两子命令(`serve`/`agent`),部署心智最小;
- 前端 vanilla JS + SVG,`go:embed` 打进二进制,零运行时依赖、零构建链;
- 存储 SQLite 单文件(N5 规模下绰绰有余),WAL + 单写连接。

## 后果

- 服务器部署 = scp 一个文件 + systemd unit;备份 = 拷一个 .db 文件;
- 放弃了 npm 图表生态,图表手写 SVG(遵循 dataviz 规范,已通过校验);
- 若未来事件量超出 SQLite 舒适区(亿级),再以 ADR 讨论换 DuckDB/ClickHouse。
