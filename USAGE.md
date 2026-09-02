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

# 流式输出到 stdout（管道友好，对标 curl -o -；单连接顺序，无续传/校验）
./porter http://127.0.0.1/file.big -o - | sha256sum

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

# URL 列表文件（每行一个 URL，# 注释与空行忽略；行尾可带 " out=name" 指定输出名；对标 aria2 -i）
./porter -i urls.txt -o outdir/

# 任务管理子命令
./porter tasks                                  # 列出持久化任务与历史（含可续传中间态）
./porter rm "outdir/a.bin"                      # 删除指定任务（running 且有 .part 时拒绝）
./porter clean                                  # 清理全部 status=done 的完成记录

# 只探测不下载（对标 wget --spider；输出 key=value 便于脚本）
./porter probe http://127.0.0.1:8080/file/big.bin

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
./porter retry [-state-dir DIR]               # 续传重跑未完成任务（done 跳过）
./porter probe <url>                          # 只探测不下载：size/ranged/name
```

## 参数

| 参数                 | 默认            | 说明                                                                                                                                                                  | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| ------------------ | ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------- | ------ | ------ | ------ | ------------------------------------------------------------------------- |
| `<url> [url2 ...]` | 必填\*          | 一个或多个 URL；协议 `http/https/ftp/ftps/file`；网络协议必须解析到 127.0.0.0/8（H-3，`file://` 为本地读写不涉及；\*-i 文件或 -proxy 见对应行）                                                          | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-i`               | 无             | URL 列表文件（每行一个 URL，`#` 注释/空行忽略；行尾    ` out=<name>` 为该任务输出名，经净化防穿越）；与位置参数合并                                                                                           | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-j`               | 0（自动）         | 并发任务数上限；只下调不上调（不越过 `-mode` 的 CPU 预算）                                                                                                                                | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `probe` 子命令        | —             | `porter probe <url> [-proxy URL] [-load-cookies file] [-H "K: V"]`：只探测不下载，输出 `url=/size=/ranged=/name=`（key=value，脚本友好；对标 wget --spider）                            | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `rm` / `clean` 子命令 | —             | `porter rm <id>... [-state-dir DIR]` 删除指定任务（running 且有 `.part` 时拒绝，避免删到在途引擎）；`porter clean` 仅清理 `status=done` 完成记录；均连带清理同名 `.part`                                  | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-proxy`           | 无             | 代理出口 `http(s)://host:port` 或 `socks5://host:port`；**设置即视为显式允许出站流量**（代理成为唯一出口，目标域解析交给代理）                                                                             | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-load-cookies`    | 无             | Netscape cookie.txt 路径；按域匹配注入 Cookie 头（与 `-H "Cookie: ..."` 共存，透传优先）                                                                                                | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-summary`         | 关             | 每秒输出一次任务进度摘要到 stderr（状态                                                                                                                                             | 已完成/总大小 (百分比) | 速率     | ETA    | 输出     | URL；R19 起差分计算速率与剩余时间，R20 起速率经 EMA(α=0.5) 平滑抗抖动；周期性帧仅显示活跃任务，结束输出全部任务的终态快照） |
| `tasks` 子命令        | —             | `porter tasks [-state-dir DIR]`：按更新时间倒序列出持久化任务（含断点续传中间态）                                                                                                            | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `retry` 子命令        | —             | `porter retry [-state-dir DIR] [-limit bps] [-proxy URL] [-load-cookies file] [-H "K: V"] [-verify algo]`：续传重跑 `status!=done` 的任务（串行、错误聚合；done 跳过）                  | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `probe` 子命令        | —             | `porter probe <url>... [-proxy URL] [-load-cookies file] [-H "K: V"]`：只探测不下载，输出 `url=/size=/ranged=/name=/final_url=`（重定向最终地址，仅不同时输出；对标 wget --spider）              | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `meta` 子命令         | —             | `porter meta <url>... [-proxy URL] [-load-cookies file] [-H "K: V"]`：只查看响应头——状态行 + 全部 `key: value`（排序稳定；对标 curl -I）                                                 | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-o`               | 自动            | 单 URL=输出文件路径；`-o -`=流式输出到 stdout（单连接顺序、无续传/校验，对标 curl `-o -`）；多 URL=输出目录（文件名取自 `out=` 行内命名 > URL 推导，同名自动 -2/-3 后缀）；单 URL 省略时自动命名：服务端 `Content-Disposition` > URL 尾段 | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-n`               | 0（自动）         | 每任务分片数；自动决策 `min(max(⌈size/8MiB⌉,3),6)`；显式 1..16                                                                                                                    | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-limit`           | 0（不限）         | 全局下载限速（字节/秒），跨任务跨分片共享                                                                                                                                               | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-H`               | 无             | 透传请求头 `"Key: Value"`，可重复                                                                                                                                            | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-mode`            | `default`     | `default`(≤60%) / `max`(满载)；多任务时决定并发任务数                                                                                                                             | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-verify`          | `sha256`      | 完成后流式校验；`none` 跳过                                                                                                                                                   | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `-state-dir`       | `.downloader` | 断点续传状态目录                                                                                                                                                            | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |
| `--output`         | `table`       | 输出模式：`table`（人类）\| `json`（统一封套）\| `ndjson`（逐行封套）。与 `-o`（输出路径，curl 语义）不同——`--output` 选的是**格式**。见「面向 AI 的机器接口」                                                        | <br />        | <br /> | <br /> | <br /> | <br />                                                                    |

## 退出码（长期稳定；Agent 消费方二分信号）

- `0` 成功（`--output json` 时按封套解析 stdout）

- `1` 执行失败（下载/校验/IO；`--output json` 时错误细节在 stdout 封套 `errors[]`，人类可读在 stderr）

- `2` 用法或参数错误（修订参数后重试，不盲目重试）

## 面向 AI 的机器接口（Agent-First CLI 契约）

对齐《面向 AI 的 CLI 上下文工程最佳实践》，CLI 提供可被模型稳定调用的接口契约——
机器格式是一等出口，人类格式是默认降级路径；**任意** **`--output`** **下退出码、字段语义、排序均不变化**。

### 统一输出封套（`--output json|ndjson`）

所有机器输出返回同一封套骨架，错误不再伪装成数据：

```json
{"schemaVersion":"1","type":"probe.list","ok":true,
 "data":[...],"warnings":[],"errors":[],"meta":{"command":"porter probe","version":"1.0.0"}}
```

- **成功**：`ok=true`、`errors` 为空、`data` 携带结果；

- **失败/部分失败**：`ok=false`、`errors[]` 每项为 `{code,retryable,message,next_actions}`；
  `next_actions` 是可直接复制执行的纠错命令（如 H-3 回环被拒 → 给 `-proxy` 放行命令）；

- **部分失败**（批量 probe/meta/download）：`data` 保留成功项，`errors` 逐项注明失败原因，退出码非零；

- **stdout/stderr 分工**：stdout 只承载数据（人类表或 JSON）；日志/进度/`[verify]` 等诊断恒走 stderr；

- **错误码**：`invalid_argument` / `not_found` / `permission_denied` / `rate_limited` /
  `transient` / `cancelled` / `internal`；`retryable` 基于可观测条件判定。

- 封套与 MCP 工具字段同源（`tasks` ↔ `list_tasks` 的 `done_bytes/size_bytes/status` 等）；两者数据模型一致，仅传输层不同。

### 机器可读帮助（渐进披露）

- `porter help` / `porter <子命令> --help`：分层帮助——一行用途 + 命令清单 + 自省下一跳；

- `porter schema`：机器可读命令清单（JSON）——每命令的 usage / sideEffect(read|write|destructive) /
  idempotent / outputFormats，外加退出码映射与封套说明；AI 可直接据此生成工具 schema，
  无需在启动时读整份文档；

- `porter version`：版本与契约 schema 版本（帮助/封套缓存的判定依据）。

### 机器接口示例（可直接执行）

```bash
./porter probe https://host/file.bin --output json | jq '.data[0] | {size_bytes, ranged}'
./porter tasks --state-dir .downloader --output ndjson        # 每行一条任务封套，可流式消费
./porter <url> -o out.bin --output json                       # 下载完成封套（含 sha256 校验和）
./porter probe http://10.0.0.1/x --output json                # 失败封套 + next_actions（退出码 1）
```

## 示例（端到端，使用 cmd/testserver）

```bash
# 终端 A：启动本地测试服务端（-addr 固定端口，避免随机端口带来的 URL 不确定；
# -limit 限速字节/秒可选）
./bin/testserver.exe -addr 127.0.0.1:54321 -name big.bin -size 67108864 -limit 4194304
# stdout: file=... size=... url=http://127.0.0.1:54321/file/big.bin

# 终端 B：下载（自动分片并行 + 完成后 sha256 校验）
./porter http://127.0.0.1:54321/file/big.bin -o big.bin -verify sha256
# stderr: [verify] big.bin(sha256)=<hex>
```

一键试用：`./scripts/demo.sh`（起服务端 → 12 项核心能力演示 → 自动清理，全部通过时退出码 0）。

## 约束提示

- **仅本地/回环**：所有 URL 必须解析到 `127.0.0.0/8`，公网地址被拒绝（H-3）。
  CLI 的唯一显式出站开关是 `-proxy`：设置代理即视为显式允许出站流量（代理成为唯一出口，
  目标域解析与可达性交给代理，本端不再预解析目标域名）。

- **cookie 语义**：`-load-cookies` 仅做域匹配（`.example.com` 与 `example.com` 等价，
  含子域后缀匹配）；不区分 path/secure 维度——cookie 文件本就是用户主动提供的凭据。
  跨主机重定向时 Cookie/Authorization 仍按既有策略剥离。

- **自动文件名**：仅单 URL 且未显式 `-o` 时生效（Metalink 元数据名优先于 Content-Disposition）；
  多 URL 模式保持 URL 推导 + 预去重，避免任务间同名冲突。

- **磁盘空间预检**：已知大小的下载开始前检查目标卷剩余空间，不足立即失败（早期失败，
  避免下载到一半因磁盘满中止）；续传按 `.part` 已有量折算；查询失败仅警告不阻断。

- **流式模式** **`-o -`**：单 URL 强制单连接顺序输出 stdout（不可寻址 → 无分片并行/
  断点续传/完成后校验，与 curl `-o -` 同类取舍）；与 `-n` 分片或多 URL 同时使用会报错。

- **断点续传（字节级）**：异常退出（kill -9/崩溃/断电）后同参数重启，自动从各分片已写前缀继续；
  崩溃最多损失最近 500ms 的进度。URL 或文件大小变化、或上次已完成（done）时改为全新下载。

- **故障重试**：429/5xx/断连/超时按指数退避（1s→30s 饱和，±20% 抖动）自动重试；
  其余 4xx（如 404）与服务器不支持 Range 不重试。

- **协议**：`http/https/ftp/ftps/file`；服务器不支持 Range 时自动退化为流式单连接。

- **HLS（.m3u8）**：URL 路径以 `.m3u8` 结尾自动按 HLS 处理——仅 VOD（直播流拒绝）、
  主播放列表自动选最高码率、AES-128 自动解密（加密流顺序下载、无续传）；
  明文流完整保留分片并行与字节级续传。未显式 `-o` 时输出名自动去 `.m3u8` 后缀
  （服务端 Content-Disposition 优先）。

- **Metalink4（.meta4/.metalink）**：自动识别并解析候选列表，按 priority 升序 failover
  （探测阶段；传输中途失败不换源）；元数据 `<hash>` 自动与实际值比对，
  不一致判任务失败并删除产物；显式 `-o` 优先于元数据中的文件名。

- **file://**：本地复制语义（离线镜像/测试）；仅绝对路径（Windows 用 `file:///C:/...` 形式）。

- **限速语义**：`-limit` 为**全局**聚合上限（与 aria2 `--max-overall-download-limit` 同语义），
  多任务多分片并发时总速率不超过该值；实测 80MiB\@12MiB/s 耗时 7s（下限 6.7s）。

- **超时语义**：不设总时长超时（低速/限速大文件不会被误杀）；拨号 5s、TLS 握手 10s、
  响应头 15s；响应体停滞由 Ctrl+C（上下文取消）兜底，进度已持久化。

