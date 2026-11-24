# ADR-0046: 心跳驱动的 Agent 自动更新、Hub 发行版代理缓存与子网强制策略

- **状态**: 已采纳
- **日期**: 2026-11-23
- **修订**: [ADR-0043](0043-self-update-and-hub-deploy.md)（仍禁止 Agent 后台轮询 GitHub；仅在显式 opt-in 后由 Hub 心跳触发并通过 Hub/中继子网下载）

## 背景

ADR-0043 提供了按需命令 `omnitoken self-update` 与 Hub 侧 CI 自动部署，但多设备实网运维中暴露出两个痛点：

1. **内网 / GFW 受限节点无法直连 GitHub Releases**：实测 `macmini`（`github.com` 读 EOF）和 `a100cc`（`release-assets.githubusercontent.com` TLS 握手超时）均无法直接运行 `omnitoken self-update`，而中心 Hub（阿里云节点）拉取 GitHub Releases 只需约 2 秒。
2. **多台远端节点手工 SSH 升级成本高，且存在误连公网风险**：一旦 Hub 升级到新版本，每台 Agent 需要逐一 SSH 升级；同时部分 Agent 历史上曾指向公网域名（如 `https://ingest.suool.net`），用户明确要求所有 Agent 的心跳、上报和自更新均只走内网 / 组网子网，不走公网域名或公网 IP。

## 决策

### 1. Hub 心跳响应通告目标版本与代理能力

`POST /api/v2/heartbeat` 响应在保留 `protocol_version` / `device_id` / `received_at` 的同时，新增：

- `hub_version`：Hub 当前运行版本（来自构建 ldflags，`dev` 不触发更新）；
- `release_proxy`：布尔值，标识 Hub 是否开启发行版代理（默认 `true`，可通过 `config.json` 中 `release_proxy_disabled: true` 关闭）。

`GET /api/v1/health` 同步附带 `release_proxy`。Agent 不再需要独立后台轮询 GitHub —— 正常 30s 子网心跳顺带获知 Hub 是否已升级。

### 2. Hub 认证发行版代理与本地校验缓存（`GET /api/v2/releases/{version}/{asset}`）

- **严格参数白名单**：`version` 必须匹配 `^v\d+\.\d+\.\d+$`；`asset` 仅允许 `SHA256SUMS` 与五平台官方产物（`omnitoken-{darwin,linux}-{amd64,arm64}`、`omnitoken-windows-amd64.exe`），拒绝路径穿越或任意文件读取。
- **鉴权与吊销即时生效**：携带 `X-OmniToken-Device-ID` 时严格校验 v2 `Authorization: Bearer <device_token>` 并经 `store.AuthenticateDevice` 鉴权（被吊销的设备立即 401）；未携带设备头时允许 `read_token` / `token` / `admin_token` / site-auth 会话（便于手工 `self-update`）。
- **先验后存 + Singleflight + 磁盘限容**：
  - 缓存落盘于 `<dir(db)>/release-cache/<version>/<asset>`，复现 Hub 既有数据目录写权限（无需改 systemd unit）；
  - 按 `version/asset` 加 singleflight 锁，车队并发下载时 Hub 仅回源上游（默认 `https://github.com/SuooL/OmniToken`，支持 `release_mirror_url` 覆盖）一次；
  - 下载任意二进制前先确保同版本 `SHA256SUMS` 已缓存，流式计算 SHA-256 并与 `SHA256SUMS` 常量时间比对，校验失败立即返回 `502` 且**绝不落盘缓存**；
  - 缓存保留最新 3 个 semver 版本目录，旧目录自动清理；
  - 单请求通过 `http.ResponseController.SetWriteDeadline` 将写超时扩至 5 分钟，不影响普通 API 的 60s 默认超时。

### 3. Relay 链式透传发行版下载

Agent 中继（`relay_listen`）新增 `GET /api/v2/releases/{version}/{asset}` 转发路由：校验 `X-OmniToken-Relay-Token` 并按跳替换，原样透传下游 `Authorization` 与 `X-OmniToken-Device-ID`，单请求写超时扩至 5 分钟。从而处于内网隔离段、经 LAN 中继接入的节点（如 `a100cc → pathzl → Hub`）也能零公网完成自更新。

### 4. Agent 显式 Opt-in 自动更新与安全自重启

- `agent.json` 新增：
  - `auto_update`（默认 `false`，CLI `-auto-update` / 环境变量 `OMNITOKEN_AUTO_UPDATE`）：显式开启后，v2 Agent 仅在自身与 `hub_version` 均为合法 semver 且 `agent < hub` 时触发异步更新（绝不自动降级，绝不更新 `dev` 构建）。
  - `auto_update_source`（默认 `"hub"`，可选 `"github"`）：`"hub"` 模式下只从 `<server>/api/v2/releases/<ver>` 下载，携带 `Authorization`、`X-OmniToken-Device-ID` 与 `X-OmniToken-Relay-Token`，绝不直连公网 GitHub。
- **防惊群、退避与可观测性**：
  - 下载前加 5–25s 随机抖动；同进程单飞防重入；
  - 失败后按 `5m → 15m → 1h` 退避（若 Hub 升级到更新版本则立即重试），并将错误摘要写入下一次心跳的 `warnings`，直接浮现于 Web 设备抽屉；
  - 复用 `internal/selfupdate.Run` 执行二次端到端 SHA-256 校验、安装前 `<new> version` 冒烟测试与原子替换。
- **跨平台自重启**：
  - Unix（Linux / macOS）：先优雅关闭本地 relay 与 durable outbox，再以 `syscall.Exec` 原地替换进程映像（保持 PID 与 `systemd`/`launchd` 监管上下文；失败时以退出码 `75` 交由 supervisor 拉起）；
  - Windows：`selfupdate.Run` 将旧二进制暂存为 `omnitoken.exe.old.<pid>` 并原子换入新二进制后，关闭 relay/outbox 并以退出码 `75`（`EX_TEMPFAIL`）退出，触发计划任务 `RestartOnFailure`（或 Windows Service recovery）拉起新版本；新进程启动时自动清理同目录残留的 `*.old.<pid>` 文件。
  - **只读部署注意**：如 Hub 宿主机上的本机 agent（`ProtectSystem=strict` 且由 `deploy-hub` 统一更新 `/usr/local/bin/omnitoken`），必须保持 `auto_update: false`。

### 5. `subnet_only` 子网强制策略与手工代理更新

- `agent.json` 新增 `subnet_only`（默认 `false`，CLI `-subnet-only` / 环境变量 `OMNITOKEN_SUBNET_ONLY`）：开启后，启动与 enrollment 阶段强制校验 `server`（及可选 `resolve_ip`）必须是 loopback、RFC1918/ULA 私网、链路本地或 RFC6598 CGNAT（`100.64.0.0/10`，覆盖 Tailscale / EasyTier）字面 IP（或 `localhost`），拒绝所有公网域名与公网 IP，并禁止与 `auto_update_source: "github"` 组合。
- `omnitoken self-update` 新增 `-agent-config <path>` 参数，支持手工按 `agent.json` 的子网地址与设备/中继凭据通过 Hub 代理拉取并校验升级。

## 后果

- **正面**：Hub 发布新版本并完成部署后，所有已开启 `auto_update: true` 的内网/子网 Agent 在下一轮心跳窗口（30s + 5–25s 抖动）内自动完成校验升级与重启，不再依赖 GitHub 连通性，也无需人工逐台 SSH。
- **约束**：Windows 节点开启 `auto_update: true` 时需确保计划任务配置了非零退出自动重启（如 `RestartOnFailure`）；Hub 数据目录会额外占用最多 3 个版本 × 已请求平台数的磁盘空间（约 50–150 MiB）。
