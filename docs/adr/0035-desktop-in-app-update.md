# ADR-0035 菜单栏应用的自更新:签名清单发在 GitHub Releases,应用自己下载替换

状态:已采纳(2026-09-08,与用户确认)

## 背景

菜单栏应用一直靠人工升级。ADR-0008 选 Tauri 时假定「像服务端二进制一样从 GitHub
Releases 分发」,但从来没有实现过分发,更没有实现过升级 —— `cargo tauri build` 只写
`target/release/bundle`,装到哪儿、什么时候装,全靠人记。

后果是可观测的:2026-09-08 发现在跑的那份 bundle 比一个改了 `desktop/ui/` 的提交早
四个小时,**两周没人发现**,DeepSeek 来源卡一直没出现在菜单栏里。
[`make desktop-install`](../../Makefile) 把「构建→装→重启→验证」固化成一条命令解决了
一半,但它要求你手边有仓库、记得跑。

## 决策

**照搬同一台机器上另一个项目 OmniStats 的做法**,它用 Sparkle 解决同一个问题,已经
发到 v1.3.3 且在用。四个要点逐条对应:

| OmniStats(Sparkle) | 本项目(tauri-plugin-updater) |
|---|---|
| `SUPublicEDKey` 写进 Info.plist | `plugins.updater.pubkey` 写进 tauri.conf.json |
| `SPARKLE_ED_PRIVATE_KEY` 只存在于 CI secret | `TAURI_SIGNING_PRIVATE_KEY` 同 |
| `generate_appcast` 产出签名的 appcast.xml | `createUpdaterArtifacts` 产出 `.app.tar.gz` + `.sig`,CI 生成 latest.json |
| feed = `releases/latest/download/appcast.xml` | endpoint = `releases/latest/download/latest.json` |

三个从中继承的判断:

1. **清单是静态的 release 资产,不是 REST API 调用。** GitHub API 按 IP 限流,在共享
   网络上会以用户看不懂的方式开始失败;`releases/latest/download/...` 只是一个到 CDN
   的重定向。
2. **签名是 Ed25519,不是「有 TLS 就行」。** TLS 认证的是主机,不是产物。公钥编进
   应用,所以被篡改或截断的下载在**替换任何东西之前**就被拒绝。
3. **定时检查,不只手动。** 没人点的更新等于没人拿到的更新。6 小时一次,与
   OmniStats 的 `SUScheduledCheckInterval` 一致。

## 实测验证过的事(不是推断)

这套东西有两个说不准的地方,都实际跑过:

**① ad-hoc 签名的应用能不能自我替换。** 两个项目都是 `codesign --force --sign -`,
没有 Apple 开发者证书、没有公证。实验:构建 0.1.0 与 0.1.1 两个 bundle,本地服务
签名清单,让 0.1.0 的实例自己去拉。结果:

- bundle 在磁盘上从 0.1.0 变成 0.1.1,约 1 秒;
- 替换后的 bundle **没有** `com.apple.quarantine`(只有无害的 `com.apple.provenance`);
- 替换后的应用**实际启动成功**。

`spctl -a` 仍然拒绝它(ad-hoc 签名过不了 Gatekeeper 评估),但 Gatekeeper 只拦**带
隔离标记**的东西 —— updater 自己解包写文件,不经过浏览器那条会打标记的路径。
这就是为什么无证书也能用。

**② 安装替换的是 bundle,不是进程。** 拿真实的 v0.2.0 发布验证:磁盘上的 app 从
0.1.0 变成 0.2.0,而**进程 pid 一直没变**。也就是说 `download_and_install` 结束后,
跑着的仍是旧代码。菜单栏应用可能连续跑几周,不显式重启的话「更新装上了但永远不生效」。
所以定时路径与手动路径都在安装后 `app.restart()`,并先发一条通知 —— 菜单栏图标无声
消失再出现,读起来像崩溃。

**③ release 构建强制 HTTPS。** 把端点设成 `http://127.0.0.1` 后,应用在插件初始化
阶段直接 panic:「The configured updater endpoint must use a secure protocol」。
**这不是降级,是整个菜单栏应用起不来。** 所以端点写错的代价是应用打不开,而配置是
静态数据 —— 加了三条 `updater_config_tests` 把它挡在编译期之后、发布之前。
(本地要用 http 测,只能用 `--debug` bundle,dev 下才放行。)

顺带记一个坑:updater 拒绝在 `current_exe()` 路径含软链时工作。macOS 的 `/tmp` 是
指向 `/private/tmp` 的软链,所以测试实例必须放在 `/private/tmp` 下。真实安装位置
`/Applications` 没有这个问题。

## 后果

- 发布流程多一个 job:`release.yml` 的 `desktop`(macos-14,`needs: release`),
  从同一个 tag 构建、签名、生成 latest.json,挂到同一个 Release 上。
  两个 macOS 架构指向同一个 bundle —— 只发 `darwin-aarch64` 会让 Intel Mac 的
  updater 永远报「已是最新」,那比让它跑 Rosetta 更糟。
- 版本号从 tag 反推,同时写进 `Cargo.toml` 与 `tauri.conf.json`。两者不一致时,
  应用要么反复重装、要么永远不更新。
- **私钥丢了就再也更新不了任何已安装的副本** —— 它们信任的公钥是编进去的。
  私钥在本机 `~/.omnitoken/tauri-updater.key`(0600,仓库外),`.gitignore` 另加了
  `*.pem` / `*.key` 作为第二道。

## 局限

- 不是「热更新」。它下载整个 bundle(约 5.5MB)、替换、重启,没有 JS 级热补丁。
  真要热补丁就得把 `desktop/ui` 改成远程加载,那会让面板变成远程代码且离线即废,
  与 ADR-0008 的瘦客户端取向冲突,明确不做。
- ~~CI 那一半没被执行过~~ **已于 2026-09-08 随 v0.2.0 首次真实发布验证**:
  `desktop` job 8m37s 通过,`latest.json` / `OmniToken.app.tar.gz` / `.sig` 三个资产
  都挂上了 Release,本机装着的 0.1.0 实例从真实端点自更新到 0.2.0(约 20 秒)。
- 应用仍未公证。用 updater 升级不受影响(上面 ①),但**从浏览器下载 DMG 再拖进
  Applications** 的那条路会带隔离标记,首次打开需要右键→打开。
