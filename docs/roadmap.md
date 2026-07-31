# 路线图与里程碑

开发模型:短迭代增量交付,每个里程碑以"端到端可用 + 真实数据验证"为完成标准
(design-first:功能动工前先更新 requirements/architecture/ADR)。

## M1 — 最小可用闭环 ✅(2026-07-26)

统一事件模型;Claude Code 解析器(+测试);采集层(增量扫描/offset 状态/repo 归一);
SSH 拉取(远端零安装);agent 推送 + 链式中继 + 配置文件体系;
服务端(幂等 ingest + SQLite + 聚合 API);内嵌总览面板(浅/深色,dataviz 规范校验);
provider 模型指纹。
**验证**:本机 46,419 条历史(3.4B tokens)导入对账;双通道去重实测;面板截图检查。

## M2 — 分析深度(进行中)

| 项 | 状态 | 备注 |
|---|---|---|
| Codex 解析器 | ✅ 完成 | 语义对照 ccusage Rust 适配器,3 个单测;实测导入 29,636 事件 |
| 成本折算(F7) | ✅ 完成 | internal/pricing,4 个单测;面板真实/等效分流展示 |
| repo 工作时长(F8) | ✅ 完成(v2) | ADR-0006:事件区间化 + 双指标(投入 union/代理 sum);3 个纯函数单测 |
| Live 实时页(F10) | ✅ 完成 | SSE 契约对齐 token-monitor hub;广播挂入库层 |
| 5 小时计费窗口(F11) | ✅ 完成 | 算法对齐 ccusage blocks.rs;跨设备=账户级口径;2 个单测 |
| 报表/明细页(F12/F13) | ✅ 完成 | subagent 并行开发;CSV 导出、事件级下钻含单事件成本 |
| Cache 分析页(F16,提前自 M3) | ✅ 完成 | 命中率/节省金额/TTL 结构;实测 30 天节省 $18.4K(等效) |
| 订阅 vs API key 探测(F9 后半) | ✅ 完成 | authprobe 四级探测(进程 env/shell env/settings/OAuth 凭据),override 保守静默,16 单测;仅本机路径精化,SSH 通道保持"推断" |

**M2 验收**:成本与 ccusage 同期对账误差为 0(同定价文件);工作时长抽查合理;
Live 页在两台以上设备同时使用时正确反映。

## M3 — 精确观测

| 项 | 状态 | 备注 |
|---|---|---|
| 本地代理(F14) | ✅ 完成 | 手写转发 + tee 流式解析,精确 TTFT/耗时,key 指纹多账号,5 单测含 race |
| 速度页(F15) | ⚠️ 口径已修正 | 见 ADR-0009:原「近似」口径是逐条比值再平均,分母含空闲,算出 Sonnet 5 = 0.7 tok/s 这类假数;已改为并集口径。Codex 仍不覆盖,但理由更正为 rollout 回放(70% 记录时间戳是刷盘时刻) |
| 权威配额(ADR-0007 → ADR-0011) | ✅ 完成 | Codex 解析 rollout 的 rate_limits;**Claude 已于 2026-07-29 改为从 statusLine 载荷捕获**,移除 OAuth 轮询与凭据读取(见 ADR-0011)。实时页权威优先、推断标注 |
| Cache 分析(F16) | ✅ 完成(提前) | 见 M2 |
| 长尾工具覆盖(F17) | 明确不做 | 见下方「明确不做」小节 |
| 发布工程 | ✅ 完成 | Makefile 交叉编译 + systemd/launchd 模板(deploy/);版本号由 `git describe` 派生并经 ldflags 注入;`release.yml` 手动触发即合 dev→main、打 tag、发布五平台产物与 SHA256SUMS。首个版本 v0.0.1 已发布并校验(校验和一致、`omnitoken version` 输出注入值) |
| 移动端适配 | ✅ 完成 | 九个页面在 320 / 390px 下横向溢出为 0,1440px 桌面无回归;标签条独立横向滚动,`pointer: coarse` 放大点击区 |

## 已完成的收尾项

- **statusline 接入(F18)** ✅:`omnitoken statusline` 子命令,本会话 + 跨设备今日 +
  权威配额,10ms 渲染、缓存兜底、颜色分级;7 个 hermetic 单测
- **5 小时窗口口径修正** ✅:按计费通道分卡(Claude 订阅 / Codex 订阅 / API 计费),
  订阅窗口用权威 `resets_at` 边界而非日志推断;API 计费用滚动 5h 且仅在有用量时出现;
  Codex 无权威数据时保留占位卡;进度条按消耗强度分级着色
- **配额主键塌缩修复** ✅(见 ADR-0007):分模型周配额此前被静默丢弃

## 缺口补齐(2026-07-27 全部完成)

对照初版页面规格核查出的 GAP-1..5 已全部实现:窗口外推回退修复、活动热力图、
独立设备页、独立模型页、设置页(定价覆盖热重载)。面板现为九页。
同轮修复:pricing.Table 并发写 map 崩溃隐患(高危)、定价热swap 的指针竞态。

## 明确不做:长尾工具覆盖(F17)

只采集 Claude Code 与 Codex。其它工具不做。

2026-07-28 复查本机(对比已采集 7.6B tokens / 52,171 事件):唯一有可解析用量的是
`.qwen`(`usageMetadata`),但最近记录停在 2026-05;`.gemini` 近 30 天有 250 次变更,
查下去全是插件与配置,根本不落用量日志;`.qoderworkcn` 130M/188 文件,抽样 40 个
会话文件提取到 0 处 token 计数;`.kiro`/`.copilot`/`.cursor`/`.aider`/`.cline`/
`.codebuddy` 均无用量字段。占比 <1%,且唯一有数据的工具已停用。

将来若要重开:Gemini CLI 与 Qwen Code 共用 `usageMetadata: {promptTokenCount,
candidatesTokenCount}`,新增 `internal/parser/gemini` 一个包即可覆盖两者。

**不要包装 ccusage。** 参考项目是用来对照解析语义的(见 `docs/references.md` 的
原则),不作为运行时被调用。它是 Node CLI,而 agent 在每台被监控机器上本地解析后
才推送事件,包装意味着每台机器(含 headless 服务器)都要装 Node,与单二进制定位
冲突;且它输出聚合值,无法参与 event_id 幂等去重。

## 明确不做:多币种

面板一律美元:定价输入、成本计算、显示,全链路单一币种。

曾经存在一个「显示币种 + 手填汇率」的设置项,但它**从未接上换算** ——
`usd()` 把 `$` 写死,填了别的币种也只是存进数据库,九个页面照样显示美元。
已整体移除,不留不生效的开关。

不做的理由:

- **内置定价表(LiteLLM)全部按美元计价**,国产模型也是折算后的美元。
  想让它们显示为「原生人民币」,要么按汇率反算(那只是换算,不是原价,
  与真实账单有汇率误差),要么自行维护一份厂商原价表(会过期)。
- **聚合视图必须单一币种**。今日总成本、趋势、设备分布都要跨模型求和,
  混合币种无法直接相加,终究要折算 —— 混合显示只能作用在模型维度的行上。
- 成本本就不入库(`events` 只存 token,成本查询时算),单一币种没有历史包袱。

将来若真要做,正确形态是给定价表引入 per-model 币种 + 原价,而不是在显示层
乘一个汇率。

## M4 — 桌面端(v1 完成)

决策见 [ADR-0008](adr/0008-desktop-client.md)。v1 之后的现代化重构见 M6。

| 项 | 状态 | 备注 |
|---|---|---|
| 前端源码提到顶层 `web/` | ✅ 完成 | `web` 包自带 embed(go:embed 无法引用父目录),`serve` 自带面板不变 |
| 前端传输层收敛 | ✅ 完成 | 12 处 `fetch` 与 1 处 `EventSource` 收敛到 `web/api.js`;桌面端只需替换 get/put/stream 三个方法体 |
| Mac 菜单栏应用(F24) | ✅ v1 完成 | Tauri 瘦客户端:托盘图标 + 点击弹出面板 + 经 Rust 侧取真实数据。完整面板仍走浏览器 |
| 服务端地址设置(F24) | ✅ 完成 | 面板内设置视图;地址存 `app_config_dir()/settings.json`,归一化与校验在 Rust 侧(6 单测);先落盘再探 `/api/v1/health`,连不上也不丢用户刚填的地址。只存地址不存 token —— 读接口本就免鉴权 |
| 燃烧速率(F24) | ✅ 完成 | 新增 `GET /api/v1/live`(SSE `snapshot` 的单次 GET 版,复用同一个 `livePayload`),面板轮询它拿配额 + 燃烧速率;2 单测。至此 v1 精简视图三项齐全 |
| 托盘配额表盘(F24) | ✅ 完成 | 菜单栏图标即最紧配额的仪表弧,按 0/25/50/75/100 五档切换,连不上服务端显示虚线环;Rust 侧 60s 轮询独立于面板。图标由 `desktop/icons-src/gen-tray-icons.py` 生成(36px = 托盘 18pt 槽位的 Retina 1:1),6 单测 |
| 应用/DMG 图标(F24) | ✅ 完成 | 蓝色 squircle + 白色仪表弧,与托盘同一形象。master 由 `desktop/icons-src/gen-app-icon.py` 生成,整套资产经 `cargo tauri icon` 派生;不收 iOS/Android 那两套(本项目只做 macOS/Windows) |
| Windows 端 | 未排期 | Tauri 跨平台,但托盘行为与视觉需单独验收 |

**前置**(2026-07-28 实测):

| 项 | 状态 |
|---|---|
| Rust(rustup / stable) | ✅ 已装 |
| Xcode 命令行工具 | ✅ `/Library/Developer/CommandLineTools` |
| WebKit 框架 | ✅ 系统自带 |
| `x86_64-apple-darwin` target | ❌ 缺,出通用二进制时需 `rustup target add`(当前只做 arm64) |
| Tauri CLI | ✅ 已装(2.11.4) |

> `~/.cargo/bin` 写在 `~/.profile` 里。非交互 zsh 只读 `.zshrc` / `.zprofile`,
> 因此某些工具环境下 `command -v cargo` 会给出假阴性 —— 判断是否已装应直接看
> `~/.cargo/bin/`,不要只信 PATH 查找。

## M5 — 重心转向实时(2026-07-29 起)

用户复盘:实际更需要的实时速度/用量实现不足,而配额这类厂商本就提供的功能反成重心。
已在 requirements.md 重定优先级(F10/F15 升 P0,F11/F19 降 P2)。现有统计分析不删减。

| 项 | 状态 | 备注 |
|---|---|---|
| 生成速度口径修正(ADR-0009) | ✅ 完成 | 见上;并集口径 + 实时速度 + 跨分片 turn 起点 |
| 面板视觉重做 | ✅ 完成 | Quiet Instrument 设计系统:system sans 正文 + tabular/mono 数字、平面分析卡、Live/Speed 局部玻璃层、sticky mobile nav、active-scroll 状态、`focus-visible` 与 reduced-motion。九路由在 1440×1000 / 390×844 实测无横向溢出 |
| 实时会话地面真值(F25) | ✅ 完成 | ADR-0012:agent 读本机进程表,`live_sessions` 表按 (device,pid) 覆盖写入,TTL 90s;Live 页区分「开着但空闲」「已关闭」「无进程数据」。实测本机识别出 1 个 claude 会话,并排除了 `codex app-server` / `codex mcp-server` 这类常驻服务进程 |
| 其余八页逐页重做 | 大体完成 | 速度页整页重做;模型页与设备页的图表统一到 ECharts(ADR-0010),去掉约 150 行手绘 SVG 与它们各自的 tooltip 实现;卡片标题不再被图例挤成两行。九个页面在 999 / 1440 / 1728px 逐页体检:横向溢出为 0,无破版。热力图保留手绘 SVG —— 日历格子不是 ECharts calendar 擅长的形状,且它本就没有坐标轴可省。剩下的是主观视觉细节,无已知缺陷 |
| 速度页重做为实时曲线 | ✅ 完成 | 近 60 分钟滚动曲线(空闲画断点不画 0)+ 并集口径按模型 + 覆盖率;删掉 ADR-0009 判定为错的逐条比值口径 —— 真机对照 claude-opus-4-8:旧均值 137.4、旧求和 31.0、新口径 68.3,旧通道两个方向同时错 |
| `-rescan` 回填入口 | ✅ 完成 | ADR-0009 承诺的全量重扫一直没有入口,gen_ms 只有 493/24,516 条。补上后实测回填至 93.5%,事件数与 token 数不变 |

## M6 — 菜单栏现代化重构(2026-07-30 起)

M4 交付的是 v1,M5 之后与主面板脱节:托盘**没有任何菜单**(连退出都没有,只能强杀)、
面板与托盘各跑一条轮询(滞后 15–60s)、弹窗漏掉了 M5 定为 P0 的生成速度、令牌还是
M5 之前的浅色一份。决策见 [ADR-0014](adr/0014-menubar-realtime-and-interaction.md)
(修订 ADR-0008 第 4 条与其 SSE 推迟结论)。服务端零改动。

| 项 | 状态 | 备注 |
|---|---|---|
| ADR-0014 + F24 范围重定 | ✅ 完成 | 修订 ADR-0008 第 4 条:弹窗从「精简视图」扩为实时视图;SSE 桥不再推迟 |
| 设计令牌单一来源(F24) | ✅ 完成 | 抽 `web/tokens.css` + `web/format-core.js` 为唯一来源,`desktop/ui/` 用副本,`make desktop-sync-check` 以 `cmp` 卡漂移(已实测:改副本即失败)。不引打包器 —— 维持 ADR-0002/0010 的零构建链。顺带把 `web` 的 `go:embed` 从点名 `style.css` 改为 `*.css`:点名会让新增样式表静默 404,而 404 的 `text/plain` 正文被浏览器拒绝当样式表用,整个面板会**无令牌**渲染却每个文件看着都在 |
| 弹窗视觉重做(F24) | ✅ 完成 | 原生 `windowEffects` 毛玻璃取代 CSS 卡片;高度按内容自适应,取代固定 420px + 内滚;不引图表库;动效遵循 `prefers-reduced-motion`,meter 补 `role`/`aria-valuenow` |
| 弹窗主角改为撞墙预测(F11/F24) | ✅ 完成 | 见 ADR-0014 第 7 条(修订本 ADR 自己的第 6 条)。环形表盘只是把托盘图标的「大概多满」又说一遍,主位应该给「会不会在重置前用完」——`windows[]` 里的 F11 外推此前一眼都没被看过。时间轴 + 100% 天花板,点线/实点/虚线分别表示路径未知、权威读数、外推;越顶转红并标触顶时刻。弹窗去掉 75/90 色阶(严重度由几何承担),天花板安全时用灰 |
| 托盘右键菜单(F24) | ✅ 完成 | `show_menu_on_left_click(false)`,左键仍切弹窗。补齐**退出**、打开完整面板、立即刷新、设置、开机自启、配额预警、全局快捷键 ⌥⌘O。三档「菜单栏数字」用 CheckMenuItem 当单选,勾选态每次从设置回灌 —— 点击会自己翻转,不回灌就会出现两个勾 |
| SSE 实时桥(F24) | ✅ 完成 | Rust 侧一条 `/api/v1/stream`,同一快照喂 webview + 托盘图标 + 托盘数字 + 预警,取代 v1 的两条轮询。断流退回轮询 `/api/v1/live` 并在界面标注降级;不可达时 1s→30s 退避。**90s 静默看门狗**:对端停发但不关闭 socket 时,TCP 会一直保持连接,弹窗会顶着「实时」显示一个冻住的数字 —— 实测撞到过,守时按字节计(心跳算活着),不按事件计 |
| 菜单栏数字(F24) | ✅ 完成 | `set_title`:关闭 / 配额% / 生成速度,默认关闭(菜单栏是稀缺空间)。无读数显示 `—` 而非上一次的数字;速度 `0/s` 表示空闲,与「无数据」区分。Windows 不支持该 API |
| 生成速度进弹窗(F15/F24) | ✅ 完成 | 载荷里的 `speed` 此前完全没渲染 —— P0 指标只在浏览器里可见,与 M5 的优先级正好相反。一并补上 `processes` / `devices` 摘要与 10 分钟生成活动条(用 `speed.spans`,不自己攒时序:载荷给的是「何时在生成」,攒出来的曲线只能说明弹窗开了多久) |
| 配额预警通知(F24) | ✅ 完成 | 跨 75%/90% 各推一次,按 `(source, scope, resets_at, threshold)` 去重;`resets_at` 变化即重新武装。一次只报最高那一档 —— 打开时已经 92% 不该同时弹 75 和 90。权限延迟到第一条真要发的预警才申请,推送在独立任务上跑(权限弹窗不返回,不能卡住流) |
| 设置结构演进(F24) | ✅ 完成 | `Settings` 每个字段都 `#[serde(default)]`:`load()` 对任何解析失败都静默回落默认值,少一个默认就会让老 `settings.json`(只有 `server` 一个键)解析失败、用户填过的地址被悄悄重置。4 条回归单测覆盖老格式/空对象/未知字段/往返 |

## M7 — 多设备汇集(2026-07-30 起)

接 macmini 做多设备实测时暴露出两个产品级缺陷,都不是本机配置的怪癖。决策见
[ADR-0016](adr/0016-read-endpoint-auth.md) 与 [ADR-0015](adr/0015-device-attribution.md)。
拓扑仍由 ADR-0003 的内网/overlay、SSH 隧道、认证 relay 与 SSH 拉取覆盖;传输语义已
升级为一个权威 Hub + device-scoped v2 protocol,而不是在每台机器部署可分叉的独立
后端/数据库。

| 项 | 状态 | 备注 |
|---|---|---|
| 读接口鉴权(ADR-0016) | ✅ 完成 | 默认 listen 改为 `127.0.0.1:8787`;非 loopback 必须同时具备 legacy ingest、read、admin 三类 credential,缺一即拒绝启动。`?access_token=` 只在 SSE 上接受 |
| 面板与桌面端 scoped token | ✅ 完成 | Web 分离 read/admin draft 与持久化边界;桌面端只存 read token。401 时设置页仍可进入并修复 credential |
| v2 device registry + enrollment | ✅ 完成 | stable UUID、per-device SHA-256 credential、admin-only enrollment 与 device revoke API、rename 不换 identity、revocation 同时阻断 ingest/heartbeat;agent config 原子 `0600` 写入且 secret 不进 argv/output |
| acknowledged transactional ingest | ✅ 完成 | 16 MiB strict envelope、device binding、batch receipt/idempotent replay、事务 apply、精确 ACK 四元组。非法 batch 不 mutation,并发相同 batch 只产生一份 receipt/通知 |
| durable outbox + cursor ledger | ✅ 完成 | agent SQLite WAL FIFO;sequence allocation 与 insert 同事务;ACK limit+1 严格解码;scan 固定 in-flight byte boundary/逐批 delivery key,小容量与重启下不会反复卡首批,quota enqueue 失败不推进 offset |
| heartbeat liveness | ✅ 完成 | 独立 heartbeat worker 不受 scan/outbox 上传阻塞；server receive time 决定 online/stale/offline，未来客户端时钟不能伪造在线。Devices/Live 合并 registry、legacy usage 与 backlog；SSE 每 30s 重算状态，Web 另用 Hub age + monotonic elapsed 本地降级，revoked offline 不会被改善 |
| transport/relay hardening | ✅ 完成 | remote plaintext HTTP 默认拒绝并需显式 opt-in;relay 逐跳独立 header credential且保留最终 device Authorization,route allowlist/body limits/timeouts;Hub/SSH scheduler具备 timeout、elapsed cadence 与 jitter |
| 设备归属(ADR-0015) | ✅ 完成 | 实测:SSH 拉取 macmini 的 46,303 条事件里 **42,784 条(92%)已存在于本机名下** —— 根因不是 id 冲突,是两台机器日志同步(539/543 个 codex rollout 文件连 UUID 都一样)。只计一次是对的(混合库 0 重复 event_id),错的是归属退化成「先扫到的胜」。改为自报优先的单向覆盖:`observed → self` 可改,反向不可,`self` 之间先到者胜(**启发式**,ADR 里写明)。计数列一行不碰 |
| 采集起点 `since` | ✅ 完成 | `ssh_hosts[].since`,早于该时刻的不入库。接一台老机器不该把可能是副本的多年历史当成它的工作补进来。畸形日期在配置加载时直接报错 —— 静默退化成「无窗口」会导入用户明确要跳过的历史,而那不可撤销 |
| agent 采集起点(补 ADR-0015) | ✅ 完成 | `ssh_hosts[].since` 有,agent 没有 —— 而 agent 更危险:推送是**自报**,优先级高于旁观推断,新装一台家目录是同步副本的机器会以自报权限认领另一台的历史。`agent.json` 补 `since`,并改掉源码里那句错误注释(「agent 只读本机日志,所以找到的都是本机的活」正是同步家目录打破的假设) |
| macmini 实接 | ✅ 完成 | agent + SSH 反向隧道(落地端口 47871,避开 macOS 临时端口区间),服务端维持 `127.0.0.1:8787` 零暴露。两端 launchd 常驻。实测:两台设备均 active、均为进程上报方(SSH 拉取给不了进程真值),0 重复 event_id |
| macOS 常驻 runbook | ✅ 完成 | launchd 两个 job + 验证清单 + 完整卸载步骤;`-L`/`-R` 给出选择规则而不是并列摆出 |

## M8 — Telemetry Studio A2(2026-07-30)

用户再次把产品重心收敛到“实际用了多少、现在/近期多快、由谁贡献”，并明确要求 Web
与菜单栏以图形表达为主。实现合同见
[ADR-0017](adr/0017-additive-speed-contributions.md)，完整设计与执行记录见
[Telemetry Studio A2 计划](superpowers/plans/2026-07-30-telemetry-studio-a2.md)。

| 项 | 状态 | 备注 |
|---|---|---|
| 可加和速度口径 | ✅ 完成 | 总吞吐与来源/设备/模型/会话贡献统一使用全局活跃时间并集作分母，保证 `aggregate_tps = Σ contribution_tps`；自身活跃速度作为不可加和的 `native_tps` 下钻。窗口边界按重叠时长分摊 token |
| 有界遥测 API | ✅ 完成 | `GET /api/v1/telemetry?range=1h\|5h\|24h` 返回今日完整模型构成、滚动 5h Claude/Codex/Other 用量、独立来源速度桶、覆盖率与未测来源；Codex 用量可见但不伪造速度 |
| Web 九页 A2 | ✅ 完成 | 总览、实时、速度、报表、明细、设备、模型、缓存、设置全部接入统一图表生命周期与状态模型；来源速度使用独立零基线，今日模型不再只显示前三名 |
| 菜单栏 A2 | ✅ 完成 | 420px 宽、可滚动自适应弹窗；主位改为近 10m 已测总吞吐，增加 Claude/Codex 5h、60m 来源 lanes、1h 峰值/活跃占比、今日全部模型和可加和贡献者。M6 的撞墙预测主位由此被取代 |
| 可靠性与安全 | ✅ 完成 | telemetry last-good 按 endpoint 隔离，401 清除旧数值，并发刷新拒绝旧快照覆盖；图表实例清理脱离 DOM 的 canvas/observer；未知、空值、陈旧状态分开表达 |
| 本机交付 | ✅ 完成 | `make check`、`make desktop-check`、Tauri app bundle、签名校验与真实数据库九路由验收通过；本机 Hub 与 `/Applications/OmniToken.app` 已升级并重启 |

## 工程事项(持续)

- 单测:每个解析器必须有基于真实样本结构的用例;去重/offset 协议有回归测试
- 发布:goreleaser 交叉编译产物(待 M2 收尾时加入)
- 文档:docs/ 与代码同 PR 更新;对外 README 保持可独立上手
