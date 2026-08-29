# SPIKE_LOG.md — G-A PoC 实测记录（2026-08-29，Windows 11 / go1.26.2 / CGO_ENABLED=0）

> 依据 [GUI_TASK.md](../GUI_TASK.md) G-A 门禁执行。所有数字来自 `scripts/measure_gui.ps1`
> 真实采样（ PrivateBytes = PrivateMemorySize64；CPU 按采样间隔归一为单核百分比）。

## Spike-1：Gio 千行列表（720×480 窗口）

| 指标 | 实测 | 门禁 |
|---|---|---|
| Private Bytes | **103.0 MB**（7 样本恒定） | ≤50 MB ⛔ |
| Working Set | 114.8 MB | — |
| 空闲 CPU | **0%/core**（无重绘风暴） | ≈0 ✅ |
| 二进制 | 6.69 MB | ≤20 MB ✅ |
| Go 堆 / Go Sys | 18.3 / 30.9 MB | — |

实现要点（如实记录）：空闲期无 FrameEvent，窗口内检查时间无法触发关闭——**自动关闭必须由
独立 goroutine `time.AfterFunc → w.Perform(system.ActionClose)` 驱动**（这同时印证了
"一切事件驱动"的 Gio 编程模型，进度刷新也必须走外部事件注入）。

## Spike-1b：Gio 空窗口地板（无任何控件）

| 指标 | 实测 |
|---|---|
| Private Bytes | **68.8 MB** |
| Working Set | 87.2 MB |
| 空闲 CPU | 0%/core |
| Go 堆 / Go Sys | 2.2 / 11.6 MB |

**结论**：Gio 的 Direct3D 管线 + 驱动侧分配的**运行时地板 ≈ 69MB Private**（Go 自身仅 12MB），
与业务内容无关、不可通过应用层优化消除。列表内容另增 ~34MB（字体整形缓存 + 文本管线）。

## Spike-2：systray × Gio 共存 —— 未执行

Gio 在 Spike-1/1b 已被内存门禁否决，Spike-2 的前提（Gio 路线继续）不成立；
且若回退 walk，托盘用 `walk.NotifyIcon` 原生实现，`getlantern/systray` 亦不再需要。

## Spike-3：lxn/walk 对照组（1000 行 ListBox + manifest）

| 指标 | 实测 | 门禁 |
|---|---|---|
| Private Bytes | **21.5 MB**（恒定） | ≤50 MB ✅ |
| Working Set | 32.4 MB | — |
| 空闲 CPU | 0%/core | ≈0 ✅ |
| 二进制 | 5.07 MB | ≤20 MB ✅ |
| Go 堆 / Go Sys | 1.5 / 7.1 MB | — |

实现摩擦（如实记录，印证任务书风险清单）：
1. 无 Common Controls 6 manifest → `TTM_ADDTOOL failed` 直接崩溃；必须用 `rsrc`
   生成 `.syso` 嵌入 manifest（已产出可复用流程：`spike/spike3/spike3.manifest` + rsrc）。
2. 声明式 `MainWindow.Create()` 必须传 `AssignTo: &mw`，否则拿到的指针是零值 → `Run()` 空指针
   （首次 spike 的测试代码 bug，非 walk 缺陷，但说明报错不友好）。

## G-A 门禁裁决

**⛔ 不通过（Gio 路线）**：空窗口地板 68.8MB > 50MB 门禁，超标发生在框架运行时层，
应用层无法优化。按任务书 §G-A："不达标则本任务书终止，回退 walk 方案重走选型（不许带病推进）"。

walk 对照组全指标达标（Private 21.5MB / CPU 0% / 5.07MB），摩擦点已有已验证解法。

## Spike-4：Fyne v2.8.1 千行虚拟列表（第 7 轮修订准则"AI 为第一用户"后补测）

| 指标 | 实测 | 门禁（修订：120MB/2%/45MB） |
|---|---|---|
| Private Bytes | **163 MB**（恒定） | ≤120 MB ⛔ |
| Working Set | 161.1 MB | — |
| 空闲 CPU | 0%/core | ≤2% ✅ |
| 二进制（cgo/MinGW 13.2） | 24.4 MB | ≤45 MB ✅ |
| Go 堆 / Go Sys | 53.9 / 68.6 MB | — |

比 Gio（103MB）还高 60%，为全场最高。附实现发现：Fyne v2.8 强制 `fyne.Do` 线程模型迁移
警告（跨 goroutine 调 UI 即打日志）——对 AI 是又一个必踩的新版坑点。

## Spike-5：Wails v2 + WebView2 千行 DOM 列表（无 npm，go:embed 纯 go build）

| 指标 | 实测 | 门禁（修订） |
|---|---|---|
| GUI 进程 Private | **55.7 MB** | ≤120 MB ✅ |
| 归属系统级总占用（+6 个 WebView2 子进程，按命令行归因） | **273.5 MB** | 诚实披露 ⚠️ |
| 空闲 CPU | 1.33%/core（WebView2 合成器空闲活动） | ≤2% ✅ |
| 二进制 | 4.27 MB | ≤45 MB ✅ |
| Go 堆 / Go Sys | 0.7 / 6.3 MB | — |

**必踩坑（对 AI 关键 lore）**：plain `go build` 不带 `-tags desktop,production` 时 Wails
拒绝启动，弹标题为 "Error" 的对话框提示 "Wails applications will not build without the
correct build tags. Please use 'wails build'..."——窗口标题被换成 Error，极易误判为
WebView2 缺失（本 spike 因此走了 WebView2Loader.dll 的弯路，该 DLL 是无关变量）。
另：Wails 的内存大头在 6 个独立 msedgewebview2.exe 子进程，单进程采样会严重低估，
须按命令行（user-data-folder 含 exe 名）归因汇总。

## 第 7 轮修订后 G-A2 门禁裁决（AI 为第一用户）

| 框架 | AI 可开发性 | 进程 Private | 系统级 Private | 空闲 CPU | 二进制 | 裁决 |
|---|---|---|---|---|---|---|
| walk | ★☆☆ | **21.5 MB** | 21.5 | 0% | 5.07 MB | 占用最优但 AI 最差（备用） |
| Gio | ★★☆ | 103（地板 68.8） | 同 | 0% | 6.69 MB | ⛔ 出局（门禁+用户裁决） |
| Fyne | ★★★ | 163 | 163 | 0% | 24.4 MB | ⛔ 超 120MB 门禁 |
| **Wails v2** | ★★★（前端=Web 为 AI 最强区；热重载；可截图验证） | **55.7** | 273.5 ⚠️ | 1.33% | 4.27 MB | **✅ 唯一过门禁的 AI 友好方案** |

**裁决**：AI 优先准则下选 **Wails v2**（进程口径过门禁、AI 三星）。代价如实声明：
系统级真实占用 ~273MB（WebView2 多进程架构）与 1.33% 空闲 CPU。若按"系统级占用 ≤120MB"
口径则四者皆无解，唯一达标者是 walk（AI 代价自担）——该口径切换属产品决策，不在本轮。
托盘：Wails v2 无内置托盘，沿用 `getlantern/systray`（纯 Go）组合方案不变。

| 选项 | 内容 | 代价 |
|---|---|---|
| A. 切换 walk | 主窗口 + `walk.NotifyIcon` 托盘一体（不再需要 systray 库）；全指标达标 | 锁定 Windows；上游停更需自维护（fork 先例存在）；manifest/rsrc 流程固化进 build.sh |
| B. 修订门禁保留 Gio | 若 50MB 阈值可放宽至 ~75MB（空窗口地板 + 余量），Gio 的跨平台与纯 Go 自绘优势保留 | 占用约为 walk 的 3~5 倍，"占用优先"目标实质放弃 |
| C. 暂停 GUI 轮次 | CLI 已可用，GUI 需求重评 | — |


## 原决策点存档（第 7 轮前，已被上方 G-A2 裁决取代）

| 选项 | 内容 | 代价 |
|---|---|---|
| A. 切换 walk | 主窗口 + `walk.NotifyIcon` 托盘一体（不再需要 systray 库）；全指标达标 | 锁定 Windows；上游停更需自维护（fork 先例存在）；manifest/rsrc 流程固化进 build.sh |
| B. 修订门禁保留 Gio | 若 50MB 阈值可放宽至 ~75MB（空窗口地板 + 余量），Gio 的跨平台与纯 Go 自绘优势保留 | 占用约为 walk 的 3~5 倍，"占用优先"目标实质放弃 |
| C. 暂停 GUI 轮次 | CLI 已可用，GUI 需求重评 | — |
