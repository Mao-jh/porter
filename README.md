<div align="center">

# Porter

**AI 的专职搬运工 —— 多线程下载器（CLI / TUI / MCP 三形态）**

`porter` / `porter-tui` / `porter-mcp` 三个二进制共用一套零第三方依赖的核心引擎：
把下载能力装进终端、装进脚本、装进任何 AI 客户端。

`go install` 即用 · 零第三方依赖核心 · 字节级断点续传 · MCP 插件化给 AI 客户端

[![CI](https://github.com/Mao-jh/porter/actions/workflows/ci.yml/badge.svg)](https://github.com/Mao-jh/porter/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go)](go.mod)

</div>

---

一个对标 IDM / aria2 的多线程下载器。核心引擎只有 Go 标准库（`go list -m all` 仅自身），
三种形态共用同一引擎：**CLI**（脚本/管道）、**TUI**（终端交互界面）、**MCP Server**
（让 ZCode / Claude / Cursor 等 AI 客户端把下载当工具调用）。

## ✨ 特性

- **真并行分片**：HEAD/Range 探测 → 计划期分片（`min(max(⌈size/8MiB⌉,3),6)`）+ 运行时
  **工作窃取**（快连接完成后接管慢连接尾段，缺口自动补投）
- **多协议**：HTTP(S) / FTP(S) / **HLS m3u8**（RFC 8216：主列表选流 + AES-128 解密，
  明文流复用分片并行与字节级续传——aria2 都没有的能力）/ **Metalink4**（RFC 5854：
  候选 failover + 元数据哈希强制比对）/ `file://` 本地复制
- **字节级断点续传**：每分片已写前缀 500ms 周期原子持久化，`kill -9` 最多损失一个周期；
  重启同命令自动续传
- **三层完整性防线**：Range 强制 + 200 全量拒绝 + 响应体限长校验 → 分片覆盖守卫 →
  流式 sha256/sha1/md5 校验；Metalink 元数据哈希直接做**期望值比对**，不符删产物
- **全局限速**：`-limit` 平滑令牌配额，跨任务跨分片严格共享
- **代理与凭据**：`-proxy` HTTP(S)/SOCKS5 代理出口（显式配置即显式允许出站）；
  `-load-cookies` Netscape cookie.txt（curl/wget/aria2 通用格式，按域匹配注入）
- **自动化友好**：`-i urls.txt` 批量任务 + `-j N` 并发上限（对标 aria2 -i/-j）；
  `-summary` 每秒进度摘要（含速率与 ETA，周期性帧仅活跃任务、结束汇总全部）；`porter tasks`
  任务列表；无 `-o` 时按 Content-Disposition 自动命名；`-o -` 流式输出到 stdout（对标 curl `-o -`）
- **HTTP/2**：显式强制启用（自定义 DialContext 下默认关闭自动协商），h2 多路复用
  ——6 分片可共享一条连接（对 h2 服务端）
- **早期失败**：下载前磁盘空间预检（跨平台；续传按 `.part` 折算；查询失败降级警告）
- **故障自愈**：429/5xx/断连/超时指数退避（1s→30s 饱和，±20% 抖动）；尊重 `Retry-After`；
  其余 4xx 不重试
- **链接发现**（`find` / `ls` / `bookmarks` / `extract` / `torrent`）：
  - `find <url>` 抓取 HTTP 页面提取可下载链接（相对 URL 绝对化、去重、`-ext` 过滤、
    `-probe` 探测大小、`-depth` 递归）——对标 yt-dlp 的链接收集，输出可直接喂 `-i`；
  - `ls <ftp-url>` FTP 目录列取（MLSD 优先，回退 LIST 解析，`-r` 递归全站清单）；
  - `bookmarks <html>` 解析浏览器书签导出（Netscape 格式，Firefox/Chrome 通用）；
  - `extract` 从任意文本/日志提取 URL；
  - `torrent <file.torrent|magnet:...>` 解析种子元数据（bencode 纯标准库实现）：
    名称/文件清单/**info_hash（SHA1 原始字节，可作磁力校验）**/tracker/**WebSeed 直链**
    （WebSeed 可直接 `porter <url>` 下载——HTTP 种子接力）；
    能力边界：不实现 BT 对等协议（零依赖取舍，见 BENCHMARK）。
- **抗劣化下载**（弱网 / 冷源 / 跨境 / 限速）：
  - `-mirror u1,u2,...` 镜像 failover：主源失败（断连/超时/5xx/慢速/停滞）按序切镜像，
    切换保持**字节级续传**（镜像内容一致时从断点继续，实测哈希一致）；
  - `-min-rate bps` 慢速保护：30s 滑动窗口平均速率低于阈值 → 判坏源切镜像/重试
    （服务端限速的主动应对，而非傻等）；
  - `-stall Ns` 停滞超时：N 秒无任何进度推进 → 判坏源（弱网挂死自救）；
  - `-retry-forever` 无限退避重试（1s→30s 饱和 ±20% 抖动，覆盖探测失败），
    直到成功或 Ctrl-C（进度已持久化，重启续传）——跨境/冷源场景挂机即走。
- **下载后处理**（`info` / `transcode` / `organize` / `scrub`）：
  - `info <file>` 纯 Go 容器解析（零依赖）预览：MP4/MKV/MP3/FLAC/WAV/JPEG/PNG 的
    时长、分辨率、编码、采样率、ID3 标题（无第三方依赖，秒出）；
  - `transcode <file> -to mp3|mp4|...` 调用系统 ffmpeg 转码（无 ffmpeg 明确报错并给
    安装指引——编码不在零依赖范围内，诚实降级）；
  - `organize <dir>` 按媒体类型归类（video/audio/image/archive/docs）+ 同名防冲突 +
    `-dedupe` 哈希去重（重复移入 .dupes/）；**只移动不删除**，`-dry-run` 先看计划；
  - `scrub <dir>` 文件级去广告：`.url/.lnk/.crdownload/.part` 残留与广告说明页
    （promo/advert/推广 等命名）移入 `.trash/`（播放器内嵌广告不在下载器职责内）。
- **MCP 插件**：5 个工具（start/status/cancel/list/probe），任何 MCP 客户端即插即用
- **合规内建**：零遥测零上报；默认 UA 自标识；跨主机凭据剥离；HLS 直播流与 DRM 方法拒绝；
  [SECURITY.md](SECURITY.md) / [COMPLIANCE.md](COMPLIANCE.md) 声明边界，
  `scripts/compliance.sh` 与 CI 持续断言

实测数据（Windows 11 / go1.26，采样脚本在 `scripts/measure_gui.ps1`）：
进程内存 **13.3MB**（TUI）、空闲 CPU **0%**、64MiB 下载 sha256 逐字节一致、
限速 12MiB/s 聚合误差 < 5%、48MiB 强杀续传 sha256 一致。

## 📦 安装

```bash
# 源码安装（需要 Go 1.22+）
go install github.com/Mao-jh/porter/cmd/porter@latest
go install github.com/Mao-jh/porter/tui/cmd/porter-tui@latest
go install github.com/Mao-jh/porter/mcp/cmd/porter-mcp@latest
```

或到 [Releases](https://github.com/Mao-jh/porter/releases) 下载预编译二进制
（Windows / Linux，静态、无动态依赖）。

### 接入 AI 客户端（MCP · Porter）

```jsonc
// ZCode / Claude Desktop / Cursor 的 mcp.json
{
  "mcpServers": {
    "porter": {
      "command": "porter-mcp",
      "args": ["-state-root", ".porter-mcp"]
    }
  }
}
```

之后 AI 客户端即拥有 5 个工具：`download_start`（异步启动）→ `download_status`（轮询进度）
→ `download_cancel` → `list_tasks`（含历史恢复）→ `download_probe`（探测 size/ranged/name）。
MCP 服务端同样支持 `-proxy` / `-load-cookies`（见下方命令行参数；`-allow-remote` 仍为
直连公网目标的产品开关，代理为另一条受控出站通道）。

**MCP 工具参数**（JSON 对象；除 `url` 外均可选）：

| 工具 | 参数 | 说明 |
|---|---|---|
| `download_start` | `url`（必填）、`output_dir`、`limit_bps` | 异步启动下载，立即返回 `task_id`/`output`/`state`；同 URL 重复调用从断点续传；`output_dir` 需已存在（与 CLI 一致，不自动创建） |
| `download_status` | `task_id`（缺省返回全部） | 查询任务状态与进度（`done_bytes`/`size_bytes`/`speed_bps`/`state`） |
| `download_cancel` | `task_id`（必填） | 取消运行中任务（进度已落盘，可续传） |
| `list_tasks` | — | 等价于 `download_status` 不带参数 |
| `download_probe` | `url`（必填） | 探测 `size_bytes`/`ranged`/`url`，不下载 |

示例调用：`download_start {"url":"http://127.0.0.1:8080/file.bin","output_dir":"/tmp/dl","limit_bps":10485760}`。

## ⚡ 快速试用（5 分钟）

一个脚本跑完全部核心能力（本地服务端 + 固定端口，自动清理），适合新环境/CI 验收：

```bash
./scripts/demo.sh            # 12 项检查：下载/sha256/probe/meta/tasks/批量/限速/强杀续传/MCP
./scripts/demo.sh 54322      # 指定端口（默认 54321）
```

跨平台：Windows 直接用 `bin/` 预编译产物；Linux/macOS 自动现场构建（需 Go 工具链，
MCP 段需 python）。退出码 0 = 全部通过。

手工起服务端（**端口可固定**，不再随机）：

```bash
./bin/testserver.exe -addr 127.0.0.1:54321 -size 16777216 -name big.bin
# url=http://127.0.0.1:54321/file/big.bin   ← 端口确定，可直接复制到下面命令
```

MCP 单测冒烟：`python scripts/mcp_smoke.py mcp/porter-mcp.exe <url> <out_dir>`

## 🚀 使用

### CLI
```bash
porter http://127.0.0.1:8080/file/big.bin            # 自动分片 + sha256 校验
porter url1 url2 -o outdir/ -limit 10485760          # 多任务 + 10MiB/s 全局限速
porter -i urls.txt -j 4 -summary                     # 批量任务 + 并发上限 + 进度摘要
porter url -H "Cookie: session=abc" -n 16            # 透传头 + 16 分片
porter url -proxy socks5://127.0.0.1:1080            # 代理出口（http/https/socks5）
porter url -load-cookies cookies.txt                 # Netscape cookie.txt 按域注入
porter url -mirror u1,u2 -min-rate 102400 -stall 30 -retry-forever   # 抗劣化：镜像/慢速/停滞/无限重试
porter tasks                                         # 列出持久化任务与历史
porter rm "out.bin" / porter clean                   # 删除任务 / 清理完成记录
porter probe http://127.0.0.1:8080/file/big.bin      # 只探测不下载（wget --spider 对标）
porter meta http://127.0.0.1:8080/file/big.bin       # 查看响应头（curl -I 对标）
porter http://127.0.0.1:8080/hls/movie.bin.m3u8      # HLS 播放列表（URL 需带完整文件名 <名称>.m3u8，.m3u8 结尾自动识别；AES-128 自动解密）
porter retry                                         # 续传重跑未完成任务（done 跳过）
porter url -o - | sha256sum                          # 流式输出到 stdout（curl -o - 对标）
# —— 链接发现（输出可直接 pipe 给 -i）——
porter find http://example.com/list.html -ext mp4,mkv -probe -depth 2 > urls.txt
porter ls ftp://mirror.example.com/pub/ -r          # FTP 全站清单
porter bookmarks bookmarks.html -out urls.txt       # 浏览器书签导出 → 批量下载
echo "见 http://x.com/a.mp4" | porter extract -      # 文本提取 URL
porter torrent movie.torrent                        # 种子解析（info_hash/WebSeed）
# —— 下载后处理 ——
porter info movie.mp4                                # 媒体预览：时长/分辨率/编码
porter transcode song.wav -to mp3                    # ffmpeg 转码（需系统 ffmpeg）
porter organize ~/Downloads -dedupe -dry-run        # 归类整理（先看计划）
porter scrub ~/Downloads                            # 广告/垃圾 → .trash
```

### TUI
```bash
porter-tui                        # 交互界面：a 添加 / p 暂停·继续 / d 删除 / q 退出
porter-tui --selftest -url URL    # 无头自检（CI 用）
porter-tui -proxy socks5://127.0.0.1:1080 -load-cookies cookies.txt   # 代理/Cookie 同样适用
```

### MCP
```bash
porter-mcp -state-root .porter-mcp    # stdio 传输，挂进任意 MCP 客户端
```

## 🔒 安全边界（重要）

**默认仅允许 `127.0.0.0/8` 回环目标**（URL 校验 + 拨号双重强制，含域名解析断言）——
这是本项目的审计边界。下载公网资源需显式打开产品开关：

```bash
porter-mcp -allow-remote     # 或 CLI/TUI 的对应选项
```

打开后即为常规下载器行为；默认关闭时，任何非回环目标在连接层被拒绝。
安全政策与漏洞报告渠道：[SECURITY.md](SECURITY.md)；
隐私（零遥测）、合法使用边界与协议行为自律：[COMPLIANCE.md](COMPLIANCE.md)。

## 🏗 架构

```
cmd/porter      CLI 入口          ┐
tui/cmd/…           TUI 入口          ├─ 三形态共用核心引擎
mcp/cmd/…           MCP Server 入口   ┘
cli/                任务协调：范围队列 + 工作窃取 + 断点续传 + 镜像/慢速/停滞抗劣化
discover/           链接发现：页面链接提取 / FTP 列目录 / 书签 / 文本提取 / bencode 种子解析
media/              下载后处理：纯 Go 容器解析（info）/ ffmpeg 编排（transcode）/ 归类去重 / 清理
scheduler/          分片决策（⌈size/8MiB⌉ 收敛 [3,6]）/ 优先级队列 / 重平衡
network/            协议层：HTTP(S) Range + FTP(S) + HLS(虚拟映射/AES-128)
                    + Metalink4 + file://，Mux 按 scheme 分发，H-3 双层校验
io/                 稀疏文件（.part 不截断）/ 64KiB 环形缓冲 / 原子提交
persist/            任务状态 JSON（.tmp + rename 原子落盘，每分片进度）
hash/               流式 MD5/SHA1/SHA256（64KiB 固定缓冲）
testserver/         环回测试服务端（Range/故障注入/限速/确定性内容/HLS/Meta4）
```

依赖边界：核心模块零第三方依赖；`tui/`（bubbletea）、`gui/`（选型证据，已归档）、
`mcp/`（go-sdk）为独立 module，各自持有依赖。

## 📊 与同类工具对比（本机实测，非转述）

| 维度 | IDM | aria2 1.37 | **本工具** |
|---|---|---|---|
| 分段 | 动态分段（闭源） | chunked 静态 | 计划期分片 + 运行时尾段窃取 |
| 连接上限 | 32 | `-x` 16 | 自动 6 / `-n` 16 |
| 续传粒度 | — | 控制文件 | **字节级**（500ms 持久化，强杀实测） |
| 代理 / Cookie / Header | ✅ | ✅ `--all-proxy`/`--load-cookies`/`--header` | ✅ `-proxy`(HTTP/SOCKS5) / `-load-cookies` / `-H` |
| 批量任务 / 并发上限 | ✅ | ✅ `-i` / `-j` | ✅ `-i urls.txt` / `-j N` |
| 协议 | HTTP/FTP | HTTP/FTP/SFTP/BT/Metalink | HTTP(S)/FTP(S)/**HLS**/Metalink4/file（SFTP/BT 受零依赖约束，见 BENCHMARK 取舍） |
| 链接发现 | — | — | ✅ find/ls/bookmarks/extract/torrent（页面/FTP/书签/文本/种子→WebSeed） |
| 抗劣化 | 续传 | 重试/镜像（`--all-proxy`） | ✅ 镜像 failover + 慢速/停滞判定 + 无限退避（字节级续传保持） |
| 后处理 | 打开文件 | 无 | ✅ info（纯 Go 解析）/ transcode（ffmpeg）/ organize / scrub |
| AI 插件 | ✗ | ✗ | ✅ MCP（5 工具） |

完整对标与差距清单：[BENCHMARK.md](BENCHMARK.md)。性能声明边界：无与第三方同环境对比数据，不做"快 N%"宣称。

## 📚 文档

[USAGE.md](USAGE.md)（CLI/TUI/MCP 全参数）· [DESIGN.md](DESIGN.md)（架构与契约）·
[TEST_REPORT.md](TEST_REPORT.md)（全部测试证据）· [BENCHMARK.md](BENCHMARK.md)（同类对标）·
[SECURITY.md](SECURITY.md)（安全政策）· [COMPLIANCE.md](COMPLIANCE.md)（合规说明）·
[PROTOCOL_TASK.md](PROTOCOL_TASK.md)（协议扩展任务书）·
[GUI_TASK.md](GUI_TASK.md)/[gui/SPIKE_LOG.md](gui/SPIKE_LOG.md)（GUI 选型为何被否决）·
[TUI_TASK.md](TUI_TASK.md)

## 🤝 开发

```bash
./run_tests.sh          # 全量门禁（vet/-race/产物/四套进程级 e2e）
./scripts/rename_module.sh github.com/<you>/<repo>   # fork 后一键改模块路径
```

## License

[MIT](LICENSE)
