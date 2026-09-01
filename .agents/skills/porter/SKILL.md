---
name: porter
description: 使用 Porter 多线程下载器（HTTP/FTP/HLS/Metalink4/file 协议，CLI/TUI/MCP 三形态）完成文件下载、批量下载、断点续传、限速、链接发现与下载后处理。当用户需要下载文件、批量抓取链接、解析种子/书签、转码或整理媒体文件，且本机存在 C:\Users\31423\Desktop\deliverable 项目（Porter 源码仓库）时使用。skill 记录二进制路径、关键参数、回环安全限制与 Windows 路径陷阱。
license: MIT
metadata:
  author: Mao-jh
  version: "1.2"
agent_created: true
---

# Porter 下载器使用指南

## Overview

Porter 是零第三方依赖的多线程下载器，三种形态共用同一引擎：
- **CLI** `bin/porter.exe`（脚本/管道）
- **TUI** `tui/porter-tui.exe`（交互界面）
- **MCP Server** `mcp/porter-mcp.exe`（给 AI 客户端当工具调用）

项目根目录：`C:\Users\31423\Desktop\deliverable`。核心能力实测通过（demo.sh 12/12 PASS，含下载/校验/批量/限速/强杀续传/MCP）。

## 二进制位置

| 形态 | 路径（Windows） | 说明 |
|---|---|---|
| CLI | `C:\Users\31423\Desktop\deliverable\bin\porter.exe` | 主入口 |
| TUI | `C:\Users\31423\Desktop\deliverable\tui\porter-tui.exe` | 交互界面 |
| MCP | `C:\Users\31423\Desktop\deliverable\mcp\porter-mcp.exe` | stdio 传输 |
| 测试服务端 | `C:\Users\31423\Desktop\deliverable\bin\testserver.exe` | 本地环回文件服务 |
| Linux 产物 | `C:\Users\31423\Desktop\deliverable\bin\porter_linux` | 交叉编译 |

## 快速验证（推荐先跑）

```bash
cd C:/Users/31423/Desktop/deliverable && ./scripts/demo.sh [PORT]   # 默认 54321
```
一键覆盖 12 项核心能力：产物检查→服务端→下载+sha256→probe/meta/tasks→批量+限速→强杀续传→MCP 冒烟→清理。退出码 0 = 全过。

手工起服务端（固定端口，URL 可复制）：
```bash
./bin/testserver.exe -addr 127.0.0.1:54322 -size 16777216 -name big.bin -extra "tiny.bin:1024" -ftp
# 提供: /file/big.bin  /file/tiny.bin  /hls/<name>.m3u8(AES/直播/主列表变体)  /meta4/  ftp://127.0.0.1:9022/
```

## 常用命令速查

```bash
PORTER="C:/Users/31423/Desktop/deliverable/bin/porter.exe"

# 基础下载（自动分片 + sha256 校验 + 断点续传）
"$PORTER" <url> -o out.bin
# 多任务 + 限速 + 并发上限
"$PORTER" url1 url2 -o outdir/ -limit 10485760
"$PORTER" -i urls.txt -j 4 -summary        # urls.txt 每行一个 URL，可带 " out=name"
# 探测/元信息/任务
"$PORTER" probe <url>                       # size/ranged/name，不下载
"$PORTER" meta <url>                        # 响应头（curl -I）
"$PORTER" tasks                             # 持久化任务列表；retry 续传未完成任务
# 抗劣化
"$PORTER" <url> -mirror u1,u2 -min-rate 102400 -stall 30 -retry-forever
# 链接发现
"$PORTER" find <page-url> -ext mp4,mkv -probe -depth 2 > urls.txt
"$PORTER" ls ftp://host/pub/ -r
"$PORTER" bookmarks bookmarks.html -out urls.txt
echo "见 http://x.com/a.mp4" | "$PORTER" extract -
"$PORTER" torrent movie.torrent             # info_hash/WebSeed 直链
# 下载后处理
"$PORTER" info movie.mp4
"$PORTER" transcode song.wav -to mp3        # 需系统 ffmpeg
"$PORTER" organize ~/Downloads -dedupe -dry-run
```

## 关键行为与陷阱（实测确认）

1. **默认仅允许回环目标（H-3 强制）**：CLI 无 `-allow-remote` 参数（README 说法不准确）。
   公网 URL 会被拒绝：`host ... resolves to non-loopback ... (H-3)`。
   唯一放行公网的方式：**显式指定 `-proxy`**（http/https/socks5），设置代理即视为允许出站。
   MCP 服务端另有 `-allow-remote` 开关。
   **TUI 界面内**：按 `x` 直接输入代理地址（Enter 生效 / 空值清除 / Esc 取消），新任务与按 `p` 重试生效，
   无需重启；公网任务失败时界面会提示该入口。TUI 按键大小写均可（大写 A/X/P/D/Q 同效）。
2. **输出目录必须已存在**：不自动创建，`-o` 目录不存在直接失败（与 curl 一致）。
3. **Windows 路径陷阱**：Git Bash 下 `/tmp/xxx` 传给原生 exe 会被解析为 `\tmp\xxx` 而失败。
   一律用 Windows 格式：`-o C:/Users/31423/Desktop/deliverable/out/file.bin`。
4. **HLS URL 需完整文件名**：以 `.m3u8` 结尾自动识别（如 `.../hls/big.bin.m3u8`），AES-128 自动解密。
5. **testserver 是前台进程**：在后台任务中启动（run_in_background），否则每条命令结束即被回收；
   产物 sha256 与源文件逐字节一致。
6. **验证算法**：`-verify sha256|sha1|md5|none`（默认 sha256），下载完成后自动校验。
7. **强杀续传**：每分片 500ms 持久化，`kill -9` 最多损失一个周期；重启同命令自动续传。
   注意 Windows 下 Git Bash `kill -9` 杀不干净 exe，需 PowerShell 按命令行特征杀：
   `powershell -NoProfile -Command "Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*resume.bin*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }"`

## MCP 接入（给 AI 客户端）

```jsonc
// mcp.json
{ "mcpServers": { "porter": {
    "command": "C:/Users/31423/Desktop/deliverable/mcp/porter-mcp.exe",
    "args": ["-state-root", ".porter-mcp"] } } }
```
5 个工具：`download_start`（异步，返回 task_id）/`download_status`（轮询）/`download_cancel`/`list_tasks`/`download_probe`。
同 URL 重复 `download_start` 从断点续传。公网目标需加 `-allow-remote`。

冒烟测试：`python scripts/mcp_smoke.py mcp/porter-mcp.exe <url> <out_dir>`

## 常见任务模板

- **抓整站资源**：`find <page> -ext mp4,mkv -probe -depth 2 > urls.txt` 然后 `-i urls.txt -j 4 -summary`
- **弱网/跨境挂机**：`-mirror u1,u2 -min-rate 102400 -stall 30 -retry-forever`，进度持久化，重启续传
- **书签批量下载**：`bookmarks bookmarks.html -out urls.txt && "$PORTER" -i urls.txt -o ~/Downloads/`
- **给 AI 用**：走 MCP，`download_start {"url": "...", "output_dir": "/tmp/dl"}` → 轮询 `download_status`
