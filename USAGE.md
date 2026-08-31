# USAGE.md — 命令行用法

## 基本用法
```bash
# 构建（或直接使用 bin/ 下的产物）
GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o porter ./cmd/porter

# 最简：下载 + 自动文件名 + 默认 sha256 校验
./porter http://127.0.0.1:8080/file/big.bin

# 指定输出路径
./porter https://127.0.0.1/x.zip -o x.zip

# 多 URL 并发下载（-o 为输出目录；文件名自动取自 URL 并去重）
./porter http://127.0.0.1/a.bin http://127.0.0.1/b.bin -o outdir/

# 全局限速（字节/秒；所有任务、所有分片连接共享该配额）
./porter http://127.0.0.1/big.bin -limit 10485760     # 10 MiB/s

# 透传请求头（可重复；Cookie / Authorization 等）
./porter http://127.0.0.1/x -H "Cookie: session=abc" -H "Authorization: Bearer t1"

# 指定分片数（0=自动决策；显式上限 16，对齐 aria2 -x）
./porter http://127.0.0.1/x -n 16

# CPU 模式（R-3）：多任务时同时决定并发任务数
./porter http://127.0.0.1/x -mode default   # ≤60% CPU（默认），多任务并发 ⌈cpus×0.6⌉
./porter http://127.0.0.1/x -mode max       # 满载；多任务并发 = cpus

# 校验算法
./porter http://127.0.0.1/x -verify sha256   # sha256 | sha1 | md5 | none

# URL 列表文件（每行一个 URL，# 注释与空行忽略；对标 aria2 -i）
./porter -i urls.txt -o outdir/

# 并发任务数上限（0=按 -mode 自动；对标 aria2 -j）
./porter -i urls.txt -j 2

# 代理出口（http/https/socks5；显式配置代理即视为允许出站——见「约束提示」）
./porter https://example.com/big.zip -proxy http://127.0.0.1:7890
./porter https://example.com/big.zip -proxy socks5://127.0.0.1:1080

# Cookie 文件（Netscape cookie.txt，curl/wget/aria2 通用格式；按域匹配注入）
./porter https://example.com/private/x.zip -load-cookies cookies.txt

# 自动文件名：省略 -o 时输出名取 服务端 Content-Disposition > URL 尾段（单 URL）
./porter https://example.com/get/123          # 服务端给 CD → setup v2.exe

# 进度摘要（每秒一行到 stderr，不刷屏）+ 任务列表子命令
./porter -i urls.txt -summary
./porter tasks                                # 列出持久化任务与历史（含可续传中间态）
```

## 参数
| 参数 | 默认 | 说明 |
|---|---|---|
| `<url> [url2 ...]` | 必填* | 一个或多个 URL；协议 `http/https/ftp/ftps/file`；网络协议必须解析到 127.0.0.0/8（H-3，`file://` 为本地读写不涉及；*-i 文件或 -proxy 见对应行） |
| `-i` | 无 | URL 列表文件（每行一个 URL，`#` 注释/空行忽略）；与位置参数合并 |
| `-j` | 0（自动） | 并发任务数上限；只下调不上调（不越过 `-mode` 的 CPU 预算） |
| `-proxy` | 无 | 代理出口 `http(s)://host:port` 或 `socks5://host:port`；**设置即视为显式允许出站流量**（代理成为唯一出口，目标域解析交给代理） |
| `-load-cookies` | 无 | Netscape cookie.txt 路径；按域匹配注入 Cookie 头（与 `-H "Cookie: ..."` 共存，透传优先） |
| `-summary` | 关 | 每秒输出一次任务进度摘要到 stderr（状态 | 已完成/总大小 (百分比) | 输出 | URL） |
| `tasks` 子命令 | — | `porter tasks [-state-dir DIR]`：按更新时间倒序列出持久化任务（含断点续传中间态） |
| `-o` | 自动 | 单 URL=输出文件路径；多 URL=输出目录（文件名取自 URL，同名自动 -2/-3 后缀）；单 URL 省略时自动命名：服务端 `Content-Disposition` > URL 尾段 |
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
./porter http://127.0.0.1:PORT/file/big.bin -o big.bin -verify sha256
# stderr: [verify] big.bin(sha256)=<hex>
```

## 约束提示
- **仅本地/回环**：所有 URL 必须解析到 `127.0.0.0/8`，公网地址被拒绝（H-3）。
  CLI 的唯一显式出站开关是 `-proxy`：设置代理即视为显式允许出站流量（代理成为唯一出口，
  目标域解析与可达性交给代理，本端不再预解析目标域名）。
- **cookie 语义**：`-load-cookies` 仅做域匹配（`.example.com` 与 `example.com` 等价，
  含子域后缀匹配）；不区分 path/secure 维度——cookie 文件本就是用户主动提供的凭据。
  跨主机重定向时 Cookie/Authorization 仍按既有策略剥离。
- **自动文件名**：仅单 URL 且未显式 `-o` 时生效（Metalink 元数据名优先于 Content-Disposition）；
  多 URL 模式保持 URL 推导 + 预去重，避免任务间同名冲突。
- **断点续传（字节级）**：异常退出（kill -9/崩溃/断电）后同参数重启，自动从各分片已写前缀继续；
  崩溃最多损失最近 500ms 的进度。URL 或文件大小变化、或上次已完成（done）时改为全新下载。
- **故障重试**：429/5xx/断连/超时按指数退避（1s→30s 饱和，±20% 抖动）自动重试；
  其余 4xx（如 404）与服务器不支持 Range 不重试。
- **协议**：`http/https/ftp/ftps/file`；服务器不支持 Range 时自动退化为流式单连接。
- **HLS（.m3u8）**：URL 路径以 `.m3u8` 结尾自动按 HLS 处理——仅 VOD（直播流拒绝）、
  主播放列表自动选最高码率、AES-128 自动解密（加密流顺序下载、无续传）；
  明文流完整保留分片并行与字节级续传。建议显式 `-o` 指定输出文件名。
- **Metalink4（.meta4/.metalink）**：自动识别并解析候选列表，按 priority 升序 failover
  （探测阶段；传输中途失败不换源）；元数据 `<hash>` 自动与实际值比对，
  不一致判任务失败并删除产物；显式 `-o` 优先于元数据中的文件名。
- **file://**：本地复制语义（离线镜像/测试）；仅绝对路径（Windows 用 `file:///C:/...` 形式）。
- **限速语义**：`-limit` 为**全局**聚合上限（与 aria2 `--max-overall-download-limit` 同语义），
  多任务多分片并发时总速率不超过该值；实测 80MiB@12MiB/s 耗时 7s（下限 6.7s）。
- **超时语义**：不设总时长超时（低速/限速大文件不会被误杀）；拨号 5s、TLS 握手 10s、
  响应头 15s；响应体停滞由 Ctrl+C（上下文取消）兜底，进度已持久化。
