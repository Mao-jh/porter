# GUI_TASK.md — 第 7 轮目标任务书：Gio 主窗口 + systray 托盘

> **【已归档/被替代】**：用户最终裁决"抛弃 GUI，采用 TUI（AI 为第一用户）"——
> 本任务书停止执行，选型证据（SPIKE_LOG 四方实测）保留。后继见
> [TUI_TASK.md](TUI_TASK.md)（Bubble Tea 栈）。

> **性质**：任务设计文档。执行者按本任务书逐门禁推进，不得跳过 G-A PoC。
> **对应差距**：[BENCHMARK.md](BENCHMARK.md) §3.2 #7（无 GUI）。
>
> **【第 7 轮修订：AI 为第一用户】**（2026-08-29 用户裁决）：选型第一准则改为 **AI 可开发性**
> （训练语料量、API 稳定性、编译期可自纠性、运行时错误的 lore 依赖度），占用退为第二准则
> （仍设上限）。据此重排候选：
> | 候选 | AI 可开发性 | 占用实测（本机） | 状态 |
> |---|---|---|---|
> | **Fyne v2.8.1** | ★★★（语料最多、API 十年稳定、报错清晰） | G-A2 实测 **163MB** | ⛔ 超门禁出局 |
> | **Wails v2.10** | ★★★（前端=Web 即 AI 最强区；热重载；可截图验证） | G-A2 实测进程 **55.7MB**（系统级归属 273MB） | **✅ 选定** |
> | Gio v0.10 | ★★☆（版本漂移严重，AI 自信输出旧 API） | 空窗口地板 68.8MB | ⛔ 出局（门禁+裁决） |
> | walk | ★☆☆（语料少且老、运行时 GUI 崩溃、lore 依赖） | **21.5MB（最优）** | 降为占用备胎 |
>
> Fyne 需要 cgo（本机 MinGW 13.2 就绪）：**仅 gui module 允许 CGO_ENABLED=1**，
> 核心 CLI 二进制保持 CGO_ENABLED=0 零依赖，审计边界不变。
> 内存门禁按新准则修订：Private ≤ **120MB**、空闲 CPU ≤ 2%、二进制 ≤ 45MB。

## 〇、目标与非目标

**目标**：为下载器提供 Windows GUI 前端。性能与占用优先，硬指标：
| 指标 | 要求 | 测量方式 |
|---|---|---|
| GUI 二进制体积 | ≤ 20 MB | `ls -l` + `file`（`-trimpath -ldflags="-s -w"`） |
| 空载内存（窗口打开、无任务） | ≤ 50 MB | PowerShell `Get-Process` 1 分钟采样脚本 |
| 空载 CPU | ≈ 0%（≤1%） | 同上（验证 Gio 无重绘风暴） |
| 下载中 GUI 进程 CPU | ≤ 5%（单核） | 1GB 下载期间采样 |
| 关闭到托盘后内存 | ≤ 40 MB | 托盘模式采样 |

**非目标（本轮不做）**：H-3 回环解除、代理、FTP/BT/磁力、浏览器集成、安装包签名、多语言、macOS/Linux 适配（Gio 天然跨平台，但本轮只验 Windows）。

## 一、架构与依赖边界（关键决策）

```
cmd/downloader-gui/main.go   ← GUI 入口（独立二进制）
gui/                         ← GUI 包：模型层（纯 Go 可测）+ Gio 渲染层 + systray 集成
gui/go.mod                   ← ★ 独立 Go module（module downloader/gui，require downloader + replace ../）
core（现有包）                ← 零改动：不 import gio/systray，`go list -m all` 保持仅 downloader
```

**必须遵守**：
1. **核心模块零第三方依赖的审计边界不破**：Gio/systray 只允许出现在 `gui/` 独立 module 中（`replace downloader => ../` 指向核心）。执行前后各跑一次根目录 `go list -m all` 留证（应仍只有 `downloader`）。
2. **依赖方向单向**：`gui → core 公开 API`，严禁反向。若需要 core 新增导出，走"契约增补"流程（见 §四 API 决策），在 DESIGN.md 附录记录，禁止修改既有签名。
3. **线程模型（PoC 必须先验证）**：三个并发域——Gio 事件循环（主 goroutine）、下载引擎 goroutine、systray 回调 goroutine。**一切跨域通信走 channel**；systray 回调与引擎进度绝不允许直接触碰 Gio 组件，统一汇入 `uiUpdates chan uiMsg`，由 Gio 事件循环消费。进度更新必须**合并去重**（只保留最新），防止重绘风暴。

## 二、功能范围（按里程碑）

### G-A PoC（强制先行，约 0.5 天）—— 两个 spike，产出实测数据后才允许继续
- **Spike-1**：Gio 空窗口 + 1000 行 `widget.List`，滚动 + 静置各 1 分钟，实测内存/CPU。
- **Spike-2**：systray 与 Gio 共存：托盘菜单点击 → channel → Gio 窗口弹通知文案；验证 Windows 消息循环线程亲和无死锁（`systray.Register` vs `systray.Run` 的取舍要在 spike 里定死并记录理由）。
- **门禁**：空载内存 ≤ 50MB 且静置 CPU ≈ 0。**不达标则本任务书终止，回退 walk 方案重走选型**（不许带病推进）。

> **执行结果（2026-08-29）**：⛔ **Gio 路线内存门禁不通过**——空窗口地板 Private 68.8MB（>50MB，
> 框架运行时层、不可优化），千行列表 103MB；空闲 CPU 0% 达标。walk 对照组全指标达标
> （Private 21.5MB / CPU 0% / 二进制 5.07MB）。完整证据与摩擦记录见
> [gui/SPIKE_LOG.md](gui/SPIKE_LOG.md)。
>
> **最终状态（用户裁决）：轮次暂停**。gui/ module、spike 源码、manifest 流程、采样脚本
> （scripts/measure_gui.ps1）与实测数据全部保留作为选型证据；重启时从 SPIKE_LOG §选型决策点
> 的三个选项继续，无需重做 PoC。
>
> **【G-A2 裁决（同日，AI 为第一用户准则）】**：Fyne ⛔（Private 163MB 超 120MB 门禁，
> 全场最高）；Wails v2 ✅（进程 55.7MB 过门禁、CPU 1.33%、二进制 4.27MB；AI ★★★——
> 前端为 Web 即 AI 最强区，可截图验证、热重载）。**选定：Wails v2 主窗口 +
> getlantern/systray 托盘**（Wails v2 无内置托盘）。完整四方对比与必踩坑
> （`-tags desktop,production`、WebView2 子进程内存归因方法）见
> [gui/SPIKE_LOG.md](gui/SPIKE_LOG.md) §G-A2 裁决。代价如实声明：系统级归属占用 ~273MB
> （WebView2 多进程架构）。托盘/窗口菜单与引擎的接线方案沿用 §一 的线程模型
> （channel 汇聚，禁止跨域直调）。
>
> **下一里程碑：G-B MVP（Wails 栈）**，验收口径不变。

### G-B 最小可用 MVP（约 1 天）
- 工具栏：添加任务（URL 输入 + 输出目录，默认目录取自设置）、开始。
- 任务列表：URL / 输出 / 大小 / 进度条 / 速度 / 状态（含分片覆盖守卫、sha256 校验结果透出）。
- 单任务全生命周期：排队 → 下载 → 完成/失败 → 打开所在文件夹（`explorer /select`）。
- 进度数据源：`persist.Store` 轮询（引擎已有 500ms 原子落盘，GUI 端 500ms 拉取 + 合并）。

### G-C 多任务与控制（约 1 天）
- 多任务并发（复用 `RunMulti` 语义）；列表操作：暂停（ctx 取消）/继续（重新入队，续传自动生效）/删除（Abort + 状态清理）。
- 全局限速设置框：映射 engine `SetGlobalLimit` 语义；显示当前活动连接数（`scheduler.Slots()`）。
- 启动时从 `persist.Store` 恢复历史任务列表（done/running 状态如实展示）。

### G-D 托盘集成（约 0.5 天）
- 关闭窗口 → 隐藏到托盘（进程常驻）；托盘菜单：显示主窗 / 暂停全部 / 退出（退出 = 取消全部 ctx + 等待 flush 完成 + `sf` 状态落盘）。
- 托盘 tooltip 实时显示「运行中 n / 总 m」；任务完成/失败弹 Windows 气泡通知。
- 图标：`go:embed` 多分辨率 .ico（16/32/48/256），DPI 缩放验证 100%/150%/200%。

### G-E 验收（约 0.5 天）
见 §五 门禁清单；全部证据回填 TEST_REPORT 新章节。

## 三、文件清单（预期产出）

```
gui/go.mod gui/go.sum            ← 独立 module：require gioui.org（执行时最新稳定版）、github.com/getlantern/systray
gui/model.go model_test.go       ← 纯 Go 状态机：任务表模型、进度合并节流、排序；-race 全覆盖（不依赖 Gio）
gui/window.go                    ← Gio 布局与事件循环（薄渲染层，逻辑尽量下沉到 model）
gui/tray.go                      ← systray 集成（菜单事件 → channel）
gui/assets/icon.ico              ← go:embed
cmd/downloader-gui/main.go       ← 装配：app.Main + tray + model
scripts/measure_gui.ps1          ← 内存/CPU 采样（真实证据脚本）
e2e/run_gui_selftest.sh          ← GUI 冒烟：downloader-gui --selftest 模式（无头驱动一次真实下载后自动退出并输出结果）
```

## 四、core API 决策（预先声明，防执行时跑偏）

- **首选（阶段 A/B）**：零 core 改动。GUI 直接调用 `cli.RunMulti(ctx, opt)`（每批任务一个 goroutine）+ `persist.Open(stateDir)` 轮询状态。暂停 = 取消该批 ctx（引擎已有的取消路径，进度已持久化）；继续 = 重新调用（续传自动生效）。
- **逃生舱（仅当轮询被实测证明不足）**：允许向 cli 包**增补**导出（不改既有签名），候选签名：
  ```go
  func NewRunner(opt *Options) (*Runner, error)        // 共享 Transport/Store
  func (r *Runner) Start(url, output string) (id string, cancel context.CancelFunc)
  func (r *Runner) Events() <-chan TaskEvent           // 状态/进度事件（合并节流在 core 侧）
  ```
  增补必须同步 DESIGN.md 契约附录与单测；`Options` 字段冻结不变。
- **禁止**：GUI 直接操作 `.part`/state.json 文件；一切状态经由 persist.Store 公开 API。

## 五、验收门禁清单（执行者逐项打勾，证据真实）

- [ ] G-A PoC 实测数据表（内存/CPU，spike 记录原文）
- [ ] 根目录 `go list -m all` 前后对比（仍仅 `downloader`）；`gui/` module 独立可构建
- [ ] 旧门禁不回退：`./run_tests.sh` 全绿（CLI 路径零感知 GUI）
- [ ] `go vet ./gui/... ./cmd/downloader-gui/...` + `go test -race ./gui/...`（model 层）全绿
- [ ] 性能五指标实测表（§〇）+ 采样脚本原始输出
- [ ] 1GB 真实下载走 GUI 完整生命周期：进度、暂停/继续（续传生效）、sha256 校验通过、打开文件夹
- [ ] 强杀 GUI 进程 → 重启 → 任务列表与续传状态恢复（persist 语义不因 GUI 改变）
- [ ] DPI 100%/150%/200% 截图 + 托盘图标/气泡验收（人工项如实标注"人工"）
- [ ] TEST_REPORT 新增章节（命令 + 原始输出）；REPORT.md 差距 #7 状态更新

## 六、风险登记

| 风险 | 缓解 |
|---|---|
| systray 与 Gio 消息循环线程冲突（Windows 消息泵亲和） | G-A Spike-2 专项验证；备选：energye/systray fork 或自绘 NotifyIcon（syscall） |
| 进度高频刷新 → Gio 重绘风暴 → CPU 飙升 | model 层合并节流（单测强制：1000 更新/秒 → 渲染 ≤ 10 次/秒） |
| Gio 版本 API 迭动 | go.mod 锁定执行时最新稳定版并记录；升级属显式任务 |
| Windows Defender 对新 exe 误报 | 不在本轮范围，报告如实标注 |
| gio/systray 依赖进入核心审计边界 | 强制独立 module（§一）；违反即返工 |

## 七、估时与顺序

G-A(0.5d，含门禁裁决) → G-B(1d) → G-C(1d) → G-D(0.5d) → G-E(0.5d)，合计 **3~4 天**。
顺序不可调换：G-A 门禁不通过则整体终止（回退 walk 重选型），G-C 依赖 G-B 的列表模型。
