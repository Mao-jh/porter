# REPORT.md — 高性能下载工具 对标分析与设计目标

> **第 6 轮迭代交付（第 5 轮联网对标 → 差距落地）**。
> 第 5 轮：以官方来源联网调研 IDM/aria2/FDM/Motrix/Gopeed/AB Download Manager，
> 对标分析由"主指令常识"升级为实证数据（[BENCHMARK.md](BENCHMARK.md)）。
> **第 6 轮**：落地差距清单中的 4 项——全局限速 `-limit`、请求头透传 `-H`、
> 多 URL 并发队列（R-3 模式真实控制并发任务数）、显式分片上限放宽至 16（`-n`，
> 对齐 aria2 -x）。全部有单测/-race/进程级 e2e 证据（TEST_REPORT §8）。
> **裁决升级：A → B** —— 执行环境迁移至 Windows 原生（go1.26.2 可用），
> 第 2 轮降级 A 的唯一原因（工具链不可导入）已消除：`go build` / `go vet` /
> `go test -race` / Windows `.exe` 运行时验证**全部真实执行并通过**。

## 一、真实性声明（CHECKLIST 逐项打勾）

- [x] **R-1 裁决**：**B**（自 A 升级）—— Windows 原生工具链就绪，G-3/G-4 门禁全过，证据见 `TEST_REPORT.md`
- [x] exe 是否真实编译验证：**是** —— `go build -trimpath -ldflags="-s -w"`，`file` → `PE32+ executable for MS Windows 6.01 (console), x86-64, 8 sections`
- [x] exe 是否在 Windows 真实运行：**是** —— 下载 64MiB + sha256 与源文件一致；进程强杀续传后 sha256 一致；CLI 冒烟 4 例退出码正确（原始输出见 TEST_REPORT §4/§5）
- [x] 工具链导入清单：见 `BUILD.md` §三（已回填 go1.26.2 windows/amd64，本机预装）
- [x] 外网探测：本轮不适用（Windows 宿主非断网沙盒）；全部网络交互限定 127.0.0.0/8（H-3），由 `network.validateURL` + DialContext 双重校验，测试覆盖
- [x] 对标数据 N/A 项：见 §二 表格「实测值」列（结构性保证 vs 运行时实测已区分标注）
- [x] 测试运行 OS：Windows 11 x64（原生）；另有 Linux 交叉编译产物（静态 ELF，`file` 验证）

## 二、G-0 对标分析【第 5 轮：联网实证修订】

> 第 4 轮及之前的对标基于"主指令已知事实 + 公开常识"；本轮用联网检索取得
> 各竞品**官方来源**的真实数据（IDM 官网/官方支持页、aria2 官方手册 1.37、
> FDM 官方渠道条目、Motrix/Gopeed/AB Download Manager 官方仓库），
> 完整矩阵、逐项来源链接与差距清单见 **[BENCHMARK.md](BENCHMARK.md)**。

### 2.1 关键实证结论（摘要）
| 维度 | IDM（实证） | aria2 1.37（官方手册） | 本工具 |
|---|---|---|---|
| 分段 | 动态文件分段、连接复用（宣称至 5x） | chunked：`split=5` 默认、`min-split-size=20M` 才再分 | 计划期分片 + 运行时尾段窃取（IDM 同类思想，机制独立实现） |
| 连接上限 | 32 | 每服务器 1（`-x` 至 16 硬上限） | 6（刻意保守，配合 H-1/H-2） |
| 续传 | ✅ | `--continue`（HTTP/FTP） | ✅ 字节级（500ms 持久化，强杀 56% 续传实测通过） |
| 协议 | HTTP/FTP | HTTP(S)/FTP/SFTP/BT/Metalink | HTTP/HTTPS，**仅回环**（H-3 交付约束） |
| 授权 | 共享软件（30 天试用） | 开源 (GPLv2) | 交付物（零第三方依赖静态二进制） |

### 2.2 「超越」声明的有效边界（R-2，联网实证后依旧成立）
- ✅ 合法：机制层对标——运行时动态分段思想与 IDM 同类（[BENCHMARK §3.1](BENCHMARK.md)）；字节级续传粒度；三层完整性防线。
- ⛔ 不宣称：任何与 IDM/aria2/FDM 的速度/内存对比数字——**无同环境实测**；对标矩阵中竞品数据均来自官方文档，本工具数据来自第 4 轮门禁实测证据。

### 2.3 联网调研暴露的主要差距（详见 BENCHMARK §3.2）
连接上限偏保守（6 vs 32/16）、无客户端限速、无代理/Cookie/Header、多任务队列未接线 CLI、
仅回环（交付约束而非缺陷）——已按价值排序，供后续迭代决策。

## 三、第 4 轮完成清单

### 架构与功能（自第 2 轮以来的实质变化）
1. **HEAD/Range 探测**（`network.Probe`）：读取 Content-Length 与 Accept-Ranges，决定「并行分片」或「流式单连接」——修复了第 2 轮 `NewPlan(0)` 导致多分片为死代码的缺陷。
2. **范围队列 + 工作窃取引擎**（`cli.downloader`）：分片为范围任务入队；worker 完成后窃取慢连接最大剩余尾段（≥2MiB 才分裂）；受害者取消后前缀入账、缺口自动补投。重叠区双写同一服务器内容，幂等无害。
3. **字节级断点续传**：`persist.State` 增加 `Shards[]` 每分片进度；`OpenSparse` 不再截断 `.part`；在途前缀纳入快照；恢复时同 URL+同大小+未完成才续传，否则全新开始。
4. **重试分类**：`network.Retryable`（429/5xx/传输错误可重试；其余 4xx、上下文取消、Range 被忽略不可重试）接入退避循环。
5. **数据完整性防线（三层）**：Range 请求必带 `Range` 头 + 200 全量拒绝 + 响应体限长校验 → 分片覆盖守卫（Done≠End-Start 硬失败）→ 完成后流式 sha256 校验。
6. **`cmd/testserver`**：独立可执行测试服务端（`-dir/-name/-size/-limit` 限速），支撑进程级 e2e 与 USAGE.md 的承诺闭环。

### 真实缺陷修复（13 项，均由真实门禁暴露）
见 `TEST_REPORT.md` §2.1 表格。要点：
- **数据正确性**：分片拆分间隙（S-2 违例）、200 全量错位、`cancel()` 后检查 ctx 导致任务静默丢失（文件空洞却报成功）。
- **并发安全**：`retry.Config` 数据竞争（-race 检出）。
- **可用性**：flag 包位置参数截断导致全部 flag 失效。
- **续传基础**：`OpenSparse` 截断 `.part`。
- **一致性**：DESIGN §3.1 分片公式内部矛盾 → 修订为 `min(max(⌈size/8MiB⌉,3),6)`。

### 门禁结论（G-0~G-4）
> ✅ **G-0/G-1/G-2**（对标/设计/审查）维持通过。
> ✅ **G-3**（编译）：`go build ./...`、`go vet ./...` exit=0。
> ✅ **G-4**（运行时）：`go test -race` 全 ok（两轮）；exe e2e 全量下载、强杀续传、故障重试、CLI 冒烟全过。
> ⚠️ 遗留（诚实声明）：pprof 内存曲线、≥1GiB e2e、≥1h 泄漏、异构服务器兼容——见 TEST_REPORT §7。

## 四、引用说明
- 运行时证据与缺陷修复明细：`TEST_REPORT.md`（第 4 轮，全部真实输出）。
- 架构/接口契约：`DESIGN.md`（§3.1 已随第 4 轮修订同步）。
- 构建/工具链：`BUILD.md`（Windows 原生流程已回填）。
- CLI 用法：`USAGE.md`。
