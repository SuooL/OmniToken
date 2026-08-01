# ADR-0023 Windows 进程表:读 PEB 拿命令行,读不到就不报

状态:已采纳(2026-08-01,与用户确认)

## 背景

ADR-0012 定下「agent 读本机进程表」,并把 Windows 实现推迟了,理由是引入
`golang.org/x/sys` 是一笔真实的依赖。当时 `procs_windows.go` 写成:

```go
func listProcs() ([]procInfo, error) { return nil, nil }
```

返回**空**而不是错误,注释里的论证是「少一份实时数据不该让整趟采集失败」。
那句话是对的,但这个实现不是它 —— 空列表不是「没有数据」,是**一句陈述**。

ADR-0012 之所以给进程状态套一层 `ProcReport` 信封,唯一目的就是让「报了、没有
会话」可表达,从而与「根本没报」区分开。Windows 这条路径把两者合并了:agent
照常发出一份 `Sessions: []` 的完整报告,Hub 记下「这台机器可观测」,面板于是
打出「没有会话开着」——

实测(2026-08-01,mypc):该机 449 个进程,其中 2 个是真实的 Claude Code 会话,
同一时刻正在产生事件(今日 145 个请求、6.2M tokens,最后一条事件 2 分钟前),
而面板说它没有会话开着,桌面端摘要把它算进「3/3 台设备可观测」。

用户看到的是「明明有记录和会话,bar 显示没有活跃」。数据一条没丢,丢的是
ADR-0012 建那层信封时唯一想守住的区分。

## 决策

### 1. Windows 实现走原生 API:Toolhelp32 枚举 + 读 PEB 拿命令行

ADR-0012 点名的 Toolhelp32 单独不够。它给的是映像名(`szExeFile`),而
`codex app-server` 与交互式 `codex` 是同一个 `codex.exe`,ADR-0012 明确要求把
前者排除在会话之外。**只有命令行能分开它们**,所以每个进程还要
`NtQueryInformationProcess` → PEB → `ProcessParameters` → `CommandLine`,三次
`ReadProcessMemory`。

依赖的实际成本比 ADR-0012 预计的低:`golang.org/x/sys` 早已在依赖树里
(modernc.org/sqlite 间接引入),这次只是从 indirect 提升为 direct,`go.mod`
的 require 块之外没有任何变化。N1(单二进制、无 CGO、可交叉编译)不受影响。

### 2. 不走 WMI

先试的是 PowerShell / wmic,因为不写平台代码。实测在 mypc 上:

| 方式 | 耗时 |
|---|---|
| `Get-CimInstance Win32_Process`(449 进程,进程内计时) | **4.5 s** |
| `wmic process get ProcessId,CreationDate,CommandLine` | ~5 s |
| 采集间隔 | **5 s** |

一次采样要占满一个采集周期,还不算每次新起一个 PowerShell 的开销。
原生路径是每进程几次系统调用。

### 3. 命令行读不到就丢掉这个进程,不用映像名兜底

这是本决策里唯一会「少报」的地方,而它换来的是不会「多报」。

退回映像名意味着把 `codex.exe` 当成会话,而它可能是 `codex.exe app-server` ——
app-server 的寿命比任何一次会话都长,一旦误报就**永久**钉在面板上。
ADR-0012 立项的动机正是替掉「codex 一直在跑」这种无信息量的信号。

漏报的代价则是自限的:5 秒后的下一轮采样就会纠正。

实际被丢掉的是系统进程和其他用户的进程 —— agent 本来也无权读,而它们不可能是
用户自己起的 CLI。32 位进程同样会被丢掉(PEB 布局不同),两个 agent CLI 都不以
32 位分发。

### 4. 观测不了就不报,而不是报空

对应第 1 节里那个 bug 的根:

- `reportProcs` 拿不到进程表就**不发**这条载荷(原本就是如此,现在有测试钉住);
- 心跳里的 `process_state` 改成**可缺省**。原来它是必填,`LiveProcesses` 出错
  会让整个心跳失败 —— 一台读不到进程表的机器于是被报成「离线」,而它的事件还在
  源源不断地到达。心跳回答的是「这台机器还在不在」,不该被一个附带载荷否决。

两条合起来才让 `has_procs=false` → 面板「无进程数据」这条既有链路真正生效。

### 5. 匹配规则的两处补充,不分平台

读到命令行之后,ADR-0012 的匹配规则本身有两个洞是 Windows 暴露出来的,但修在
共享代码里,因为论证与平台无关:

- **路径分隔符**:`tokenIsBinary` 只按 `/` 切,于是
  `C:\...\claude-code\bin\claude.exe` 整条被当作一个文件名,匹配不上任何东西。
  现在两种分隔符都切 —— 同一条 MSYS 命令行里两种都有。
- **引号感知的分词**:Windows 上有意思的路径都在 `C:\Program Files\` 下,因而
  总是带引号。按空白切会把一个带引号的路径切成两段,把真正的第二个参数挤出
  `commandRuns` 只看前两个 token 的窗口。

以及一条新规则:**shell 包装不算会话**。Windows 上每个 npm 装的 CLI 都由
shim 启动(`sh.exe /c/Users/x/AppData/Roaming/npm/claude`),MSYS sh 作为父进程
一直活着,于是一次会话在进程表里出现两次。丢掉 shim 不丢会话 —— 它启动的那个
二进制就在同一张表里(mypc 上实测,两者启动时间相差 1 秒)。

这条在 Unix 上同样成立:一个开着的 Claude Code 会话会为每次工具调用 spawn 一个
shell,每个都在路径里带着 `.claude`。

## 影响

- `internal/collect/procs_windows.go` 从 12 行的占位变成真实实现;
- `internal/collect/procs.go` 的分词与匹配对两个平台同时收紧;
- `model.Heartbeat.ProcessState` 由必填变为可缺省,服务端早已容忍 `nil`,无需改动;
- Windows 机器首次进入「可观测」集合,`observable` 计数不再把只发空报告的机器算进去。

## 未决

进程表读取的频率仍与事件扫描共用 `collect.interval_seconds`。原生路径下这不成
问题(毫秒级),但如果将来某个平台的实现重新变贵,该拆成独立间隔 —— live 数据的
TTL 是 90 秒,采样频率不必等于扫描频率。
