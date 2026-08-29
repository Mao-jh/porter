<div align="center">

# downloader

> **Porter** —— 本仓库的 MCP 插件名：给 AI 配一个专职搬运工。
> mcp.json 里 `"porter"` 即可把下载能力装进任何 AI 客户端。

**多线程下载器 · CLI / TUI / MCP 三形态 · AI 第一用户**

`go install` 即用 · 零第三方依赖核心 · 字节级断点续传 · MCP 插件化给 AI 客户端

[![CI](https://github.com/Mao-jh/downloader/actions/workflows/ci.yml/badge.svg)](https://github.com/Mao-jh/downloader/actions/workflows/ci.yml)
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
- **字节级断点续传**：每分片已写前缀 500ms 周期原子持久化，`kill -9` 最多损失一个周期；
  重启同命令自动续传
- **三层完整性防线**：Range 强制 + 200 全量拒绝 + 响应体限长校验 → 分片覆盖守卫 →
  流式 sha256/sha1/md5 校验
- **全局限速**：`-limit` 平滑令牌配额，跨任务跨分片严格共享
- **故障自愈**：429/5xx/断连/超时指数退避（1s→30s 饱和，±20% 抖动）；其余 4xx 不重试
- **MCP 插件**：4 个工具（start/status/cancel/list），任何 MCP 客户端即插即用

实测数据（Windows 11 / go1.26，采样脚本在 `scripts/measure_gui.ps1`）：
进程内存 **13.3MB**（TUI）、空闲 CPU **0%**、64MiB 下载 sha256 逐字节一致、
限速 12MiB/s 聚合误差 < 5%、48MiB 强杀续传 sha256 一致。

## 📦 安装

```bash
# 源码安装（需要 Go 1.22+）
go install github.com/Mao-jh/downloader/cmd/downloader@latest
go install github.com/Mao-jh/downloader/tui/cmd/downloader-tui@latest
go install github.com/Mao-jh/downloader/mcp/cmd/downloader-mcp@latest
```

或到 [Releases](https://github.com/Mao-jh/downloader/releases) 下载预编译二进制
（Windows / Linux，静态、无动态依赖）。

### 接入 AI 客户端（MCP · Porter）

```jsonc
// ZCode / Claude Desktop / Cursor 的 mcp.json
{
  "mcpServers": {
    "porter": {
      "command": "downloader-mcp",
      "args": ["-state-root", ".downloader-mcp"]
    }
  }
}
```

之后 AI 客户端即拥有 4 个工具：`download_start`（异步启动）→ `download_status`（轮询进度）
→ `download_cancel` → `list_tasks`（含历史恢复）。

## 🚀 使用

### CLI
```bash
downloader http://127.0.0.1:8080/file/big.bin            # 自动分片 + sha256 校验
downloader url1 url2 -o outdir/ -limit 10485760          # 多任务 + 10MiB/s 全局限速
downloader url -H "Cookie: session=abc" -n 16            # 透传头 + 16 分片
```

### TUI
```bash
downloader-tui                        # 交互界面：a 添加 / p 暂停·继续 / d 删除 / q 退出
downloader-tui --selftest -url URL    # 无头自检（CI 用）
```

### MCP
```bash
downloader-mcp -state-root .downloader-mcp    # stdio 传输，挂进任意 MCP 客户端
```

## 🔒 安全边界（重要）

**默认仅允许 `127.0.0.0/8` 回环目标**（URL 校验 + 拨号双重强制，含域名解析断言）——
这是本项目的审计边界。下载公网资源需显式打开产品开关：

```bash
downloader-mcp -allow-remote     # 或 CLI/TUI 的对应选项
```

打开后即为常规下载器行为；默认关闭时，任何非回环目标在连接层被拒绝。

## 🏗 架构

```
cmd/downloader      CLI 入口          ┐
tui/cmd/…           TUI 入口          ├─ 三形态共用核心引擎
mcp/cmd/…           MCP Server 入口   ┘
cli/                任务协调：范围队列 + 工作窃取 + 断点续传
scheduler/          分片决策（⌈size/8MiB⌉ 收敛 [3,6]）/ 优先级队列 / 重平衡
network/            HTTP(S) Range + 探测 + 限速 + 重试分类 + H-3 双层校验
io/                 稀疏文件（.part 不截断）/ 64KiB 环形缓冲 / 原子提交
persist/            任务状态 JSON（.tmp + rename 原子落盘，每分片进度）
hash/               流式 MD5/SHA1/SHA256（64KiB 固定缓冲）
testserver/         环回测试服务端（Range/故障注入/限速/确定性内容）
```

依赖边界：核心模块零第三方依赖；`tui/`（bubbletea）、`gui/`（选型证据，已归档）、
`mcp/`（go-sdk）为独立 module，各自持有依赖。

## 📊 与同类工具对比（本机实测，非转述）

| 维度 | IDM | aria2 1.37 | **本工具** |
|---|---|---|---|
| 分段 | 动态分段（闭源） | chunked 静态 | 计划期分片 + 运行时尾段窃取 |
| 连接上限 | 32 | `-x` 16 | 自动 6 / `-n` 16 |
| 续传粒度 | — | 控制文件 | **字节级**（500ms 持久化，强杀实测） |
| 协议 | HTTP/FTP | HTTP/FTP/SFTP/BT/Metalink | HTTP/HTTPS |
| AI 插件 | ✗ | ✗ | ✅ MCP（4 工具） |

完整对标与差距清单：[BENCHMARK.md](BENCHMARK.md)。性能声明边界：无与第三方同环境对比数据，不做"快 N%"宣称。

## 📚 文档

[USAGE.md](USAGE.md)（CLI/TUI/MCP 全参数）· [DESIGN.md](DESIGN.md)（架构与契约）·
[TEST_REPORT.md](TEST_REPORT.md)（全部测试证据）· [BENCHMARK.md](BENCHMARK.md)（同类对标）·
[GUI_TASK.md](GUI_TASK.md)/[gui/SPIKE_LOG.md](gui/SPIKE_LOG.md)（GUI 选型为何被否决）·
[TUI_TASK.md](TUI_TASK.md)

## 🤝 开发

```bash
./run_tests.sh          # 全量门禁（vet/-race/产物/四套进程级 e2e）
./scripts/rename_module.sh github.com/<you>/<repo>   # fork 后一键改模块路径
```

## License

[MIT](LICENSE)
