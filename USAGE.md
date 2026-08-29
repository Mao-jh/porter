# USAGE.md — 命令行用法

## 基本用法
```bash
# 构建（或直接使用 bin/ 下的产物）
GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o downloader ./cmd/downloader

# 最简：下载 + 自动文件名 + 默认 sha256 校验
./downloader http://127.0.0.1:8080/file/big.bin

# 指定输出路径
./downloader https://127.0.0.1/x.zip -o x.zip

# 多 URL 并发下载（-o 为输出目录；文件名自动取自 URL 并去重）
./downloader http://127.0.0.1/a.bin http://127.0.0.1/b.bin -o outdir/

# 全局限速（字节/秒；所有任务、所有分片连接共享该配额）
./downloader http://127.0.0.1/big.bin -limit 10485760     # 10 MiB/s

# 透传请求头（可重复；Cookie / Authorization 等）
./downloader http://127.0.0.1/x -H "Cookie: session=abc" -H "Authorization: Bearer t1"

# 指定分片数（0=自动决策；显式上限 16，对齐 aria2 -x）
./downloader http://127.0.0.1/x -n 16

# CPU 模式（R-3）：多任务时同时决定并发任务数
./downloader http://127.0.0.1/x -mode default   # ≤60% CPU（默认），多任务并发 ⌈cpus×0.6⌉
./downloader http://127.0.0.1/x -mode max       # 满载；多任务并发 = cpus

# 校验算法
./downloader http://127.0.0.1/x -verify sha256   # sha256 | sha1 | md5 | none
```

## 参数
| 参数 | 默认 | 说明 |
|---|---|---|
| `<url> [url2 ...]` | 必填 | 一个或多个 URL；仅 http/https；必须解析到 127.0.0.0/8（H-3） |
| `-o` | `download.bin` | 单 URL=输出文件路径；多 URL=输出目录（文件名取自 URL，同名自动 -2/-3 后缀） |
| `-n` | 0（自动） | 每任务分片数；自动决策 `min(max(⌈size/8MiB⌉,3),6)`；显式 1..16 |
| `-limit` | 0（不限） | 全局下载限速（字节/秒），跨任务跨分片共享 |
| `-H` | 无 | 透传请求头 `"Key: Value"`，可重复 |
| `-mode` | `default` | `default`(≤60%) / `max`(满载)；多任务时决定并发任务数 |
| `-verify` | `sha256` | 完成后流式校验；`none` 跳过 |
| `-state-dir` | `.downloader` | 断点续传状态目录 |

## 退出码
- `0` 成功
- `1` 下载/校验失败
- `2` 参数错误

## 示例（端到端，使用 cmd/testserver）
```bash
# 终端 A：启动本地测试服务端（127.0.0.1 随机端口，-limit 限速字节/秒可选）
go run ./cmd/testserver -dir ./e2edata -name big.bin -size 67108864 -limit 4194304
# stdout: file=... size=... url=http://127.0.0.1:PORT/file/big.bin

# 终端 B：下载（自动分片并行 + 完成后 sha256 校验）
./downloader http://127.0.0.1:PORT/file/big.bin -o big.bin -verify sha256
# stderr: [verify] big.bin(sha256)=<hex>
```

## 约束提示
- **仅本地/回环**：所有 URL 必须解析到 `127.0.0.0/8`，公网地址被拒绝（H-3）。
- **断点续传（字节级）**：异常退出（kill -9/崩溃/断电）后同参数重启，自动从各分片已写前缀继续；
  崩溃最多损失最近 500ms 的进度。URL 或文件大小变化、或上次已完成（done）时改为全新下载。
- **故障重试**：429/5xx/断连/超时按指数退避（1s→30s 饱和，±20% 抖动）自动重试；
  其余 4xx（如 404）与服务器不支持 Range 不重试。
- **协议**：仅 http/https；服务器不支持 Range 时自动退化为流式单连接。
- **限速语义**：`-limit` 为**全局**聚合上限（与 aria2 `--max-overall-download-limit` 同语义），
  多任务多分片并发时总速率不超过该值；实测 80MiB@12MiB/s 耗时 7s（下限 6.7s）。
- **超时语义**：不设总时长超时（低速/限速大文件不会被误杀）；拨号 5s、TLS 握手 10s、
  响应头 15s；响应体停滞由 Ctrl+C（上下文取消）兜底，进度已持久化。
