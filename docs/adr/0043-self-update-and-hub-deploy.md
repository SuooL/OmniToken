# ADR-0043 Hub 与 Agent 按需自更新(`omnitoken self-update`)与 GitHub Actions 自动部署

状态:已采纳(2026-11-22)

## 背景

macOS 菜单栏客户端已通过 `tauri-plugin-updater` 实现版本检查与签名自更新(ADR-0035)，但
`omnitoken` 二进制(承载 Hub `serve`、内嵌 Web 面板以及各设备 `agent`)仍依赖手工下载或重跑
`install.sh --no-enroll`。

用户明确提出两条边界:
1. 为 Hub 与 Agent 增加 `omnitoken self-update` 子命令，支持手动一键升级与可选服务重启；
2. 发新版本时由 **GitHub Actions 事件驱动自动部署线上 Hub**，同步更新 Hub 与内嵌 Web；
3. **不做后台定时拉取(polling)** —— 不在 `serve` 或 `agent` 进程内引入定时轮询升级，避免不可控的后台自我变更与额外外网依赖。

## 决定

### 1. `omnitoken self-update` 子命令(`internal/selfupdate`)

- **仅按需执行**:由运维/用户手工执行，或由 GitHub Actions 在发布后触发；常驻的 `serve`/`agent` 绝不后台轮询升级。
- **零新依赖**:纯 Go 标准库实现，保持单二进制无 CGO(ADR-0002)。
- **版本解析零 API 限流**:
  - 指定 `-version vX.Y.Z` 时直接从 `releases/download/<tag>` 拉取；
  - 未指定时通过 `GET https://github.com/SuooL/OmniToken/releases/latest` 的 `302 Location` 提取 `/releases/tag/vX.Y.Z`，不碰限流的 GitHub REST API(同 ADR-0035)；
  - 支持 `-base-url` 覆盖下载源(适配内网镜像或测试)。
- **强制 SHA-256 校验与同目录原子替换**:
  - 下载 `SHA256SUMS`(上限 64 KiB)与对应 `GOOS/GOARCH` 资产(上限 128 MiB)到目标二进制同目录的临时文件；
  - 严格比对 SHA-256，校验失败立即删临时文件并报错退出，绝不触碰现役二进制；
  - Unix 沿用权限并原子 `os.Rename` 覆盖；Windows 先将运行中的 `.exe` 重命名为 `.exe.old` 再替换，失败自动回退。
- **版本比较与 `-check`**:
  - 语义化版本比对，目标版本不高于当前版本且未传 `-allow-downgrade` 时直接退出 0(无下载)；`dev` / 非 semver 开发构建允许升级；
  - `-check` 仅检查并输出当前/最新版本，不下载不写入。
- **可选服务重启(`-restart` / `-restart-service`)**:
  - 传 `-restart` 自动探测当前活跃的 `omnitoken-server` / `omnitoken-agent`(Linux systemd)或 `com.omnitoken.agent`(macOS launchd)并重启；
  - 传 `-restart-service a,b` 仅重启显式指定的服务。

### 2. GitHub Actions 发版后自动部署线上 Hub

- **事件驱动、零轮询**:
  - 在 `.github/workflows/release.yml` 增加 `deploy-hub` job(`needs: release`)，每次发版发布五平台二进制与 `SHA256SUMS` 后自动触发；
  - 新增独立 `.github/workflows/deploy-hub.yml`(`workflow_dispatch`)，支持按需重跑或显式指定版本回滚。
- **安全门控与无 Secret 降级**:
  - 依赖仓库 secrets `HUB_SSH_HOST`、`HUB_SSH_USER`、`HUB_SSH_KEY`(可选 `HUB_SSH_PORT`、`HUB_SSH_KNOWN_HOSTS`、`HUB_SERVICES`、`HUB_HEALTH_URL`)；
  - 未配置上述 secrets 时安全跳过并输出说明，不阻断 `release` 与 `desktop` job。
- **首次引导兼容与自动回滚**:
  - 部署前将 `/usr/local/bin/omnitoken` 备份至 `/var/backups/omnitoken/omnitoken-<tag>-<ts>`；
  - 若目标机现役二进制已支持 `self-update`，直接调用 `omnitoken self-update -version <tag> -allow-downgrade`；若尚为未含该子命令的旧版本，自动回落为 `curl` + `sha256sum` 校验并原子 `install`；
  - 重启 `omnitoken-server`(及运行中的 `omnitoken-agent`)后轮询本机 `/api/v1/health`(默认 `http://127.0.0.1:8787/api/v1/health`)并验证 `omnitoken version`；
  - 健康检查失败自动恢复备份二进制并重启服务，job 返回失败。
- **Agent 边界**:
  - 内网/桌面 Agent 无公网 SSH 入口，不由 CI 主动推送；升级只需在设备执行 `omnitoken self-update -restart`(首次从旧版迁移仍可用 `install.sh --no-enroll`)。

## 影响

- 不修改任何事件采集、解析、入库与去重逻辑。
- 发版后 Hub 与内嵌 Web 可由 GitHub Actions 自动上线并带健康检查回滚，macOS 菜单栏继续走 `tauri-plugin-updater` 自更新，各端 Agent 通过 `omnitoken self-update -restart` 单命令升级。
