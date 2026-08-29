# BENCHMARK.md — 同类下载工具实证对标（联网调研）

> 调研日期：2026-08-29，方式：公开网页检索（官方站点/官方手册/GitHub/权威评测）。
> 所有第三方数据均标注来源链接；本工具一列的"实测状态"以第 4 轮门禁证据（`TEST_REPORT.md`）为准。
> 遵守 R-2 边界：**不做任何"比 X 快 N%"声明**（无同环境实测对比）。

## 一、对标对象一览（定位与引擎）

| 工具 | 定位 | 引擎/技术栈 | 协议 | 开源 | 平台 |
|---|---|---|---|---|---|
| **IDM** 6.43.x | 商业共享软件（30 天试用，Tonec FZE） | 闭源（Windows） | HTTP/FTP | ⛔ | Windows |
| **aria2** 1.37 | 轻量命令行多协议下载引擎 | C++ | HTTP(S)/FTP/SFTP/BT/Metalink | ✅ | 全平台（CLI） |
| **FDM** 6.x | 免费图形化下载器 | 闭源 | HTTP/FTP/BT/磁力 | ⛔ | Win/macOS/Linux/Android/iOS |
| **Motrix** | aria2 图形壳（Electron+Vue） | 复用 aria2 | HTTP/FTP/BT/磁力 | ✅ | Win/macOS/Linux；v1 自 2023 起基本停更，Turbo 2.0 重写 |
| **Gopeed** | 开源现代下载器 | **Go** + Flutter | HTTP(S)/BT/磁力/ed2k | ✅ | 全平台含移动端/Web，支持扩展 |
| **AB Download Manager** 1.6.x | 开源 IDM 替代品 | JVM（Compose UI） | HTTP/FTP | ✅ | Windows/macOS + Android 伴侣 |
| **本工具** | 回环受限的多线程 HTTP 下载器（交付约束 H-3） | **Go 1.26** 标准库 | HTTP/HTTPS（仅 127.0.0.0/8） | ✅（交付物） | Windows/Linux（静态单二进制） |

## 二、核心机制对比

| 机制 | IDM | aria2 | FDM / Motrix / Gopeed / AB | **本工具（实测状态）** |
|---|---|---|---|---|
| 分段策略 | **动态文件分段**：下载中动态重分配分段，连接复用（官方宣称加速至 5x） | chunked 静态分片：默认 `--split=5`；`--min-split-size=20M`（可 1M~1024M）才触发再分 | 均为多线程分片下载（粒度策略未公开/依赖引擎） | **计划期分片 + 运行时尾段窃取**：快 worker 完成即取消最慢在途请求、接管其剩余区间（≥2MiB 才分裂），缺口自动补投——与 IDM"动态分段"同一思想，机制独立实现 |
| 单任务连接数上限 | 最多 **32**（Options→Connection 可调） | 默认每服务器 **1**，`-x` 可至 **16**（硬编码，更多需改源码重编译） | 多线程（具体上限未公开） | 默认 `min(max(⌈size/8MiB⌉,3),6)`，**上限 6**（刻意保守，配合内存红线 H-1/H-2） |
| 断点续传 | ✅ | ✅ `--continue`（HTTP/FTP） | ✅ | ✅ **字节级**：每分片已写前缀 500ms 周期原子持久化，强杀最多损失一个周期（进程级实测：56% 处 kill -9 → 续传 sha256 一致） |
| 多任务并发 | 默认 4 文件 | 默认 `-j=5` | 均支持队列 | 调度器有优先级队列（`scheduler`），但 CLI 目前单 URL 入口，多任务队列未接线 |
| 完整性校验 | —（无内置校验声明） | ✅ `--checksum=sha-256=` | 部分支持 | ✅ 流式 sha256/sha1/md5（64KiB 固定缓冲），完成后自动校验 |
| 失败重试 | 自动重连 | ✅ `--retry-wait`/`--max-tries` | ✅ | ✅ 指数退避+抖动（1s→30s 饱和）；分类：429/5xx/断连/超时可重试，其余 4xx 与"服务器忽略 Range"不可重试 |
| 限速 | ✅ | ✅ `--max-overall-download-limit` | ✅ | ⛔ 客户端未实现（testserver 有限速但那是测试设施） |
| 代理 / Cookie / 自定义 Header | ✅ | ✅ `--all-proxy` / `--load-cookies` / `--header` | ✅ | ⛔ 未实现 |
| 浏览器集成 / GUI | ✅ 浏览器接管 | CLI（配 AriaNg 等） | ✅ | ⛔ 纯 CLI（设计定位：库+CLI，安全边界为回环） |
| 部署形态 | 安装包 | 单二进制（C++，动态/静态视构建） | 安装包 / Electron | **静态单二进制、零第三方依赖**（`go list -m all` 仅 module 自身） |

## 三、本工具的差异化（与差距）

### 3.1 对齐或独有
1. **运行时动态分段（工作窃取）**：与 IDM 的核心加速思想同类，且实现路径不同（HTTP Range 重协商 + 上下文取消 + 区间补投），有单测与进程内 e2e 覆盖。
2. **字节级续传粒度**：aria2 的 `--continue` 基于控制文件；本工具按"分片内已写前缀"持久化（500ms），粒度不劣于常见实现，且有强杀实测证据。
3. **三层完整性防线**：Range 必带头 + 200 全量拒绝 + 响应体限长校验 → 分片覆盖守卫（Done≠End-Start 硬失败）→ 流式哈希。同类工具的公开文档未见等价的"覆盖守卫"层。
4. **可审计的安全边界**：所有出站 socket 强制回环（URL 校验 + DialContext 双层校验，含域名解析断言），这是交付约束 H-3，也是本工具与所有同类工具的根本差异——**它不是通用下载器，不可直接用于公网**（差距见 3.2 第 1 条）。
5. **零依赖静态产物**：Windows PE 与 Linux ELF（`CGO_ENABLED=0`），供应链面最小。

### 3.2 差距清单（诚实，按价值排序）
| # | 差距 | 同类参照 | 状态（第 6 轮更新） |
|---|---|---|---|
| 1 | 仅回环，无法用于真实网络 | 全部同类 | ⛔ 交付约束保留；解除需产品决策（安全开关 + 灰度） |
| 2 | 连接上限 6 vs IDM 32 / aria2 16 | IDM/aria2 | ✅ 显式 `-n` 放宽至 **16**（对齐 aria2 -x=16）；自动决策仍封顶 6（保内存红线） |
| 3 | 无客户端限速 | 全部同类 | ✅ `-limit` 全局限速（平滑令牌配额，跨任务跨分片共享；80MiB@12MiB/s 实测 7s ≥ 理论 6.7s） |
| 4 | 无代理/Cookie/Header/认证 | aria2 最全 | ◐ `-H` 透传头已实现（Cookie/Authorization 可用）；代理需决策（与 H-3 交互） |
| 5 | 多任务队列未接线到 CLI | aria2 `-j`、IDM 4 任务 | ✅ 多 URL 队列已接线：并发任务数由 R-3 模式决定（default ⌈cpus×0.6⌉ / max cpus） |
| 6 | 协议面窄（无 SFTP/BT/磁力） | aria2 六协议 | ◐ **第 13 轮扩展 FTP(S)/HLS/Metalink4/file**（HLS 为主列表选流+AES-128，复用分片引擎；Metalink 含候选 failover+哈希期望值校验）。SFTP 需 x/crypto、BT 需完整协议栈——零依赖（B-1/H-4）约束下的诚实取舍 |
| 7 | 无浏览器集成/GUI | IDM/FDM/AB | ✅ **TUI 已实现**（Bubble Tea，`tui/` module + `downloader-tui.exe`；AI 第一用户准则下实测 Private 13.3MB 为全候选最优）。GUI 仍无（选型证据 [gui/SPIKE_LOG.md](gui/SPIKE_LOG.md)） |
| 8 | HTTP/2 已随 net/http 自动启用但未调优、HTTP/3 未支持 | — | ⛔ 长尾优化项 |

### 3.3 性能声明边界（R-2）
- ✅ 可声明：机制层对标（动态分段思想同类、字节级续传、三层校验），均有测试证据。
- ⛔ 不可声明：任何与 IDM/aria2/FDM 的速度或内存对比数字——无同环境实测；如需，后续在受控环境对同一文件同链路跑三方对比并出 CSV。

## 四、来源

- IDM 官网（动态分段、32 连接、5x 宣称、30 天试用）：[internetdownloadmanager.com](https://www.internetdownloadmanager.com/)、[动态分段与性能支持页](https://www.internetdownloadmanager.com/support/segmentation.html)
- aria2 官方手册 1.37（`--split=5`、`--min-split-size=20M`、`--max-connection-per-server=1`、`--max-concurrent-downloads=5`、协议列表）：[aria2c(1) manual](https://aria2.github.io/manual/en/html/aria2c.html)；16 连接硬上限的源码级说明：[ntsd.dev](https://ntsd.dev/aria2-max-connections-per-server/)、[ArchWiki](https://wiki.archlinux.org/title/Aria2)
- FDM（多线程、BT、平台覆盖）：[Chrome 商店页](https://chromewebstore.google.com/detail/free-download-manager-for/foghpgkpmafaojbgpdnaelhaidlpmigi)、[Softonic 条目](https://free-download-manager.en.softonic.com/)、[Google Play](https://play.google.com/store/apps/details?id=org.freedownloadmanager.fdm)
- Motrix（aria2 引擎、停更状态）：[motrix.app/about](https://www.motrix.app/about)、[GitHub agalwood/Motrix](https://github.com/agalwood/Motrix)
- Gopeed（Go+Flutter、协议与扩展）：[gopeed.com](https://gopeed.com/)、[GitHub GopeedLab/gopeed](https://github.com/GopeedLab/gopeed)
- AB Download Manager（开源 IDM 替代、1.6.4）：[GitHub releases](https://github.com/amir1376/ab-download-manager/releases)、[Neowin 报道](https://www.neowin.net/software/ab-download-manager-164/)
