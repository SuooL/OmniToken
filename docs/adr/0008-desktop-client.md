# ADR-0008 桌面端形态:Tauri 菜单栏瘦客户端

状态:已采纳(2026-07-28)。第 4 条(菜单栏 v1 的精简视图清单)与本文对 SSE 桥接的
推迟结论,经 [ADR-0014](0014-menubar-realtime-and-interaction.md) 修订(2026-07-30):
实时改由 Rust 侧一条 SSE 长连接分发,弹窗补入生成速度。瘦客户端、Tauri 外壳、
网络走 Rust 侧三条不变。

## 背景

面板目前只有 web 一种形态,要看用量得开浏览器输地址。需求是 Mac 菜单栏常驻、
点击展开内容,Windows 端后续跟上。参考实现是 token-monitor
(`~/git/references/token-monitor`):Electron 应用,`src/electron/tray.js` 用
`Tray` + `BrowserWindow` 做菜单栏,前后端经 Electron IPC 通信。

**现状核实(2026-07-28)**:前后端在架构层面**已经分离**——

- 服务端零模板渲染,`//go:embed web` + `http.FileServerFS` 纯静态托管;
- 前端是零构建 SPA(原生 `<script>`,无模块、无打包器,项目无任何 Node 依赖),
  全部数据经 `/api/v1/*`(12 个 GET 端点 + `/api/v1/stream` SSE)获取。

耦合的只是**打包方式**,不是架构。因此本 ADR 的重点是桌面端,而非"分离"本身。

## 决策

1. **壳用 Tauri**(Rust + 系统 WebView),不用 Electron。
2. **瘦客户端**:只连接已运行的 `omnitoken serve`(本机或远端),自身不采集、
   不内嵌服务端。
3. **前端源码提到顶层 `web/`**,web 与桌面端共用同一份;Go 侧继续 `go:embed`,
   `omnitoken serve` 仍自带完整面板,单二进制定位不变。
4. **菜单栏 v1 是精简视图**(配额、燃烧速率、今日用量),不是九页面板的搬运。
   完整面板仍走浏览器。
5. **网络请求走 Rust 侧**,不给服务端加 CORS。
6. 先 macOS,Windows 后续。

## 为什么不是 Electron

| | Electron | Tauri |
|---|---|---|
| 应用体积 | 约 150MB | 约 10MB |
| WebView | 自带 Chromium | 系统的(macOS 为 WKWebView) |

服务端本体只有 11MB。为一个"随时瞟一眼配额"的常驻程序附带一整个浏览器,
比例失衡。Tauri 同样能复用现有 HTML/CSS/JS,菜单栏支持也成熟。

代价是项目引入第三套工具链(Go + JS + Rust)。这是本决策最主要的负债,
接受它的前提是桌面端不承担采集职责——见下。

## 为什么是瘦客户端

采集职责已由 `serve`(本机扫描 + SSH 拉取)与 `agent`(推送)覆盖。桌面端再采集
会成为**第二个写入者**,与 event_id 幂等(ADR-0004)和 offset 仅在上报成功后推进
这两条铁律产生新的交互面,而收益只是省掉一条 `omnitoken agent` 命令。

菜单栏的核心价值是随时可见,不是多一个采集通道。

## 为什么不给服务端加 CORS

Tauri 的 webview 从 `tauri://localhost` 加载,与 `http://<server>:8787` 跨源,
直接 `fetch` 会被 CORS 拦下。但**不应靠给服务端加 CORS 头解决**:

除 `POST /api/v1/ingest` 与设置写入外,**所有读接口都是免鉴权的**
(`server.go:60-71`)。一旦返回 `Access-Control-Allow-Origin: *`,任何用户访问过
的网页都能读取其用量数据,只要能连到该服务端。

改为在 Tauri 的 Rust 侧发起 HTTP、经 IPC 把结果交给 webview。Rust 侧不受
同源策略约束,服务端一行不用改,也不新增暴露面。

## 后果

**正面**

- 应用体积与服务端同量级,不与"单二进制、轻量"的定位冲突。
- 前端一份代码,web 与桌面端共用;`serve` 仍自带面板,现有部署方式不受影响。
- 服务端无需为桌面端做任何改动。

**负面**

- 引入 Rust 工具链。开发机已装(rustup / stable),Tauri 另需 CLI 与
  `x86_64-apple-darwin` target 才能出通用二进制 —— 见 roadmap M4 的前置清单。
- 前端需要一层传输抽象:现有 12 处 `fetch` 直调 `/api/v1/*`,web 走 `fetch`、
  桌面端走 Tauri IPC,需收敛到统一的 api 层。
- **SSE 是最难桥接的一环**:`live.js` 用 `EventSource("/api/v1/stream")`,
  Rust 侧要流式转发到 webview。这也是 v1 只做精简视图、SSE 可先用轮询替代的原因。
- 桌面端需要一个已运行的服务端才有数据,首次启动要引导用户填地址与 token。

**待办(不在本 ADR 范围)**

- 服务端地址与 token 的本地存储(Tauri 有安全存储能力,不落明文)。
- Windows 端:Tauri 跨平台,但托盘行为与视觉需单独验收。
