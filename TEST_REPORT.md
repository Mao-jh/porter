# TEST_REPORT.md — 运行时测试报告（第 4 轮：门禁全通过）

> **本轮变化**：执行环境迁移至 **Windows 原生（go1.26.2 windows/amd64 可用）**，
> 第 1~3 轮被阻塞的 G-3/G-4 门禁（真实编译 / vet / -race 测试 / exe 运行时）**全部执行并通过**。
> 以下所有命令输出均来自本轮真实执行，无伪造。

## 0. 门禁总览

| 门禁 | 第 1~3 轮 | 本轮（第 4 轮） |
|---|---|---|
| `go build ./...` | ⛔ 无工具链 | ✅ exit=0 |
| `go vet ./...` | ⛔ | ✅ exit=0 |
| `go test ./...` | ⛔ | ✅ 10 包全 ok |
| `go test -race ./...` | ⛔ | ✅ 10 包全 ok（跑 2 轮防抖动，exit=0） |
| Windows `.exe` 真实产物 | ⛔ | ✅ PE32+ x86-64，`file` 验证 |
| exe 运行时下载 + sha256 | ⛔ | ✅ 64 MiB 内容逐字节一致 |
| 进程强杀 + 断点续传 | ⛔ | ✅ 强杀于 56% 处，续传后 sha256 一致 |

## 1. 环境与工具链（真实）

```
$ go version
go version go1.26.2 windows/amd64

$ gcc --version | head -1
gcc.exe (MinGW-w64 x86_64-ucrt-posix-posix-seh, built by Brecht Sanders, r8) 13.2.0
```

构建约束保持：`GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0`（测试中 `-race` 按官方要求
临时 `CGO_ENABLED=1`，产物构建保持 0）。

## 2. 编译 / vet / 测试门禁（真实输出）

```
$ go build ./...        → (无输出) exit=0
$ go vet ./...          → (无输出) exit=0

$ go test -count=1 ./...
ok  downloader/cli        0.365s
?   downloader/cmd/downloader   [no test files]
?   downloader/cmd/testserver   [no test files]
ok  downloader/hash       0.097s
ok  downloader/io         0.189s
ok  downloader/network    0.099s
ok  downloader/persist    0.064s
ok  downloader/retry      0.051s
ok  downloader/scheduler  0.051s
ok  downloader/testserver 0.134s

$ go test -race -count=1 ./...   → 全部 ok，exit=0（复跑 count=2 仍全 ok）
```

### 2.1 首次 `-race` 暴露并修复的问题（真实缺陷清单）

| # | 位置 | 缺陷 | 修复 |
|---|---|---|---|
| 1 | `io/buffer_test.go` | 测试自身死锁：写 1MiB 只读 4KiB 后等待写完成 → 包挂 600s 超时 | 重写：并发排空 + 背压阻塞断言 + `Close` 解除阻塞 |
| 2 | `retry.Backoff` | `Base << attempt` 大 attempt 移位溢出为负（生产 bug） | 饱和倍增（达上限即止），新增溢出回归测试 |
| 3 | `retry_test` 抖动断言 | 测试内 `1s<<i` 溢出产生负区间 | 用 float 幂计算 nominal，attempt≤30 |
| 4 | `scheduler.splitLocked` | **拆分后新片 append 到尾部 → 分片出现间隙**（违反 S-2 不变量） | 新片插入 idx+1 并重排 Index；拆分连续性回归断言 |
| 5 | `scheduler.Submit` | 强制要求 `URL` 非空，通用任务无法入队 | 改为要求非空 `ID` |
| 6 | `retry.Config.randSrc` | **数据竞争**（-race 检出）：多 worker 并发调用 `Backoff` 共享非并发安全的 `*rand.Rand` | 改用全局并发安全源，Config 明确并发安全 |
| 7 | `testserver.CreateFile` | 全零内容掩盖偏移错位类缺陷 | 确定性偏移相关模式填充（`PatternFill` 可独立复算） |
| 8 | `network.FetchRange` | **`start=0` 的分片不发 Range 头 → 200 全量响应被当分片写入 → 数据错位**（内容校验捕获） | 任何非全量请求必发 Range；200+已发Range 判为服务器不支持 Range（不可重试错误）；响应体用 `LimitReader` 限长并校验完整性 |
| 9 | `cli.runTask` | **`cancel()` 之后检查 `atCtx.Err()` → 恒非 nil → 所有传输错误被误判为"上下文取消"而静默丢弃任务**（文件空洞却报成功） | 在 `cancel()` 之前读取 ctx 状态；新增分片覆盖守卫（Done≠End-Start 即硬失败） |
| 10 | `cli.Parse` | Go `flag` 包在首个位置参数后停止解析：`downloader <url> -o x` 的 `-o` 被当成位置参数（**全部 flag 失效**） | 先重排（标志前置、位置参数后置）再 `fs.Parse` |
| 11 | `io.OpenSparse` | `os.Create` 截断已有 `.part` → 断点续传基础被破坏 | `O_RDWR\|O_CREATE` 不截断；大于 size 收缩、小于则预分配；新增 `Close()`（保留文件）与 `Abort()`（删除）语义分离 |
| 12 | `hash.New("")` | 空算法静默返回 sha256（调用方误用陷阱） | 空算法显式报错 |
| 13 | DESIGN §3.1 分片公式 | `min(max(⌊size/1MiB⌋,3),6)` 与"每片≤8MiB、Rebalance 可拆分"数学上互斥（恒 ≥5 片且无法再分） | 修订为 `n = min(max(⌈size/8MiB⌉,3),6)`（自洽：默认3、封顶6、≤48MiB 时每片≤8MiB，且为拆分预留空间），DESIGN.md 已同步 |

## 3. 二进制产物与验证（真实）

```
$ file bin/downloader.exe bin/testserver.exe bin/downloader_linux
bin/downloader.exe:   PE32+ executable for MS Windows 6.01 (console), x86-64, 8 sections
bin/testserver.exe:   PE32+ executable for MS Windows 6.01 (console), x86-64, 8 sections
bin/downloader_linux: ELF 64-bit LSB executable, x86-64, statically linked, stripped
```

构建命令（`CGO_ENABLED=0`，静态、零第三方依赖）：
```
go build -trimpath -ldflags="-s -w" -o bin/downloader.exe ./cmd/downloader
go build -trimpath -ldflags="-s -w" -o bin/testserver.exe ./cmd/testserver
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/downloader_linux ./cmd/downloader
```

## 4. exe 端到端运行时测试（真实执行，脚本 `e2e/run_e2e.sh`）

```
=== [E1] testserver.exe 启动（64MiB 确定性模式文件） ===
url=http://127.0.0.1:29380/file/big.bin
=== [E2] 源 sha256 ===
src_sha256=805aa695c801698bf4c6815ca8fbbcf8d962a25b93ecb956c9391e8484f289dd
=== [E3] downloader.exe 全量下载 ===
[verify] out.bin(sha256)=805aa695c801698bf4c6815ca8fbbcf8d962a25b93ecb956c9391e8484f289dd
exit=0
=== [E4] 比对 ===
out_sha256=805aa695...（与源一致）  .part 已清理
=== [E6] CLI 冒烟 ===
无参数            → 用法提示, exit=2
-mode bogus      → 参数错误: 非法 -mode, exit=2
http://10.0.0.1  → 探测资源失败: host 10.0.0.1 not loopback (H-3), exit=1
ftp://           → 参数错误: 仅 http/https, exit=2
```

## 5. 进程强杀断点续传（真实执行，脚本 `e2e/run_resume.sh`）

方法：testserver 限速 4MiB/s/连接（6 分片聚合 ~24MiB/s），下载 1.5s 后 `kill -9`，
检查现场 → 重启同参数续传 → sha256 比对。

```
=== [R1] 强杀现场 ===
.part 存在: 67108864 字节（稀疏预分配）
status= running  done= 37748736 / 67108864      ← 已落盘 56%
shards= [(0,11184811,6291456) (11184811,22369622,6291456) (22369622,33554433,6291456)
         (33554433,44739244,6291456) (44739244,55924054,6291456) (55924054,67108864,6291456)]
       ↑ 每分片在途前缀 6MiB 均已持久化（字节级续传）
=== [R2] 重启续传 ===
[verify] r.bin(sha256)=805aa695c801698bf4c6815ca8fbbcf8d962a25b93ecb956c9391e8484f289dd
MATCH: 进程强杀后续传内容一致
=== [R3] state=done 后重复运行 → 幂等重下成功，exit=0
```

> 字节级续传的实现：`downloader.snapshotShards()` 将在途尝试的已写前缀一并计入
> 持久化快照（FetchRange 顺序写入 `.part`，前缀是有效落盘数据），崩溃最多损失一个
> 500ms 持久化周期的进度。

## 6. 单元/集成测试覆盖要点（全部真实通过）

- `scheduler`：分片不变量（不重叠/无间隙/全覆盖，9 组边界）；显式 `-n` 收敛；快片拆分/停滞合并后仍连续；优先级队列出队序；R-3 模式开关。
- `network`：H-3 回环白名单（含域名解析二次校验）；`Retryable` 分类矩阵；Range/开放区间/全量三种 FetchRange 逐字节内容断言；服务器忽略 Range 的错位防护；Probe 大小与 Accept-Ranges。
- `io`：环形缓冲定容/背压阻塞/Close 解除阻塞/并发不丢数据；SparseFile 原子提交、**重开不截断**（S-3 前提）；CopyBuffer 显式缓冲。
- `cli`（e2e，进程内）：6 分片并行下载 sha256 闭环；**模拟崩溃续传**（只补缺失分片，`ServedBytes` 证明未重下全量）；3 次断连故障注入重试后内容无损；已取消 ctx 立即返回；窃取逻辑（受害者标记/尾段切割/缺口补投）与快照-恢复往返。
- `testserver`：206 + Content-Range 正确性；两段 Range 拼接 sha256 一致；故障注入计数（429/5xx/reset）。

## 7. 未覆盖项（诚实声明）

| 项 | 状态 | 说明 |
|---|---|---|
| pprof 内存曲线（H-1/H-2 实测 MB 数） | ⚠️ 未做 | 固定 64KiB 缓冲 + 流式哈希为结构性保证；无运行时曲线数据 |
| 1GiB / 2GiB 大文件 e2e | ⚠️ 未做 | e2e 用 64MiB（模式填充耗时与磁盘占用权衡）；分片不变量已由单测覆盖 2GiB 场景 |
| 长时泄漏 ≥1h | ⚠️ 未做 | goroutine 泄漏经引擎设计规避（Close/取消广播），无长时数据 |
| HTTPS / 断点续传跨服务端兼容 | ⚠️ 未做 | 仅环回 testserver；真实异构服务器未测 |

> **反幻觉声明**：本报告所有输出均来自真实执行（Windows 11 / go1.26.2）。
> 标注 ⚠️ 者确未运行，无伪造日志。

## 8. 第 6 轮新增：限速 / Header / 多任务（真实执行）

### 8.1 新增能力与证据
| 能力 | CLI | 验证 |
|---|---|---|
| 全局限速（跨任务跨分片共享） | `-limit` | 单测：512KiB@1MiBps 耗时 ∈[400ms,5s]；集成：2MiB@512KiBps ≥3.5s；进程 e2e：80MiB@12MiB/s 耗时 7s（理论下限 6.7s） |
| 请求头透传 | `-H "K: V"`（可重复） | testserver 新增 `/echo` 回显端点，逐项断言 Cookie/X-Test/Authorization；`SetHeaders(nil)` 清空 |
| 多 URL 并发队列 | `downloader url1 url2 ... -o dir` | 进程内 e2e：3 任务（含同名去重 a.bin→a-2.bin）全部 sha256 闭环、状态各自 done；失败聚合（404 任务报错不影响成功任务） |
| 显式分片上限 16 | `-n 16` | `NewPlanN(30MiB, 16)` → 16 片连续覆盖；n=99 收敛到 16 |
| R-3 并发任务数 | `-mode` | `Scheduler.Slots()`：default ⌈cpus×0.6⌉ / max cpus，多任务消费协程数取该值 |

### 8.2 进程级多任务 e2e（`e2e/run_multi.sh`，真实输出）
```
=== [M1] 三任务并发 + -o 目录 + 全局限速 12MiB/s ===
[outdir\m3.bin] 完成 <- http://127.0.0.1:3419/file/m3.bin
[outdir\m2.bin] 完成 <- http://127.0.0.1:3419/file/m2.bin
[outdir\m1.bin] 完成 <- http://127.0.0.1:3419/file/m1.bin
elapsed=7s（3 文件共 80MiB@12MiBps 理论下限 ≥6.7s）
=== [M2] sha256 比对 === MATCH: 三任务内容全部一致
=== [M3] 单任务 + -H 透传冒烟 === 带 -H 下载 OK
```

### 8.3 附带修复（本轮实现过程中发现）
- `http.Client.Timeout=30s` 会切断低速/限速大文件 → 移除，改为拨号/TLS/响应头阶段性超时。
- `-o` 语义扩展：多 URL 时作为输出目录（单 URL 仍为文件路径）；
  `deriveOutputs` 同名去重计数修正（原实现对第三同名文件复用 -2 后缀）。


## 9. 第 8 轮：TUI 前端（AI 为第一用户，全部真实输出）

### 9.1 选型收敛链
GUI 四框架实测（§8 前后）：walk 21.5MB/AI★☆☆、Gio 103MB ⛔、Fyne 163MB ⛔、Wails 进程 55.7MB/系统级 273MB ⚠️。
用户裁决：抛弃 GUI、采用 TUI、AI 为第一用户 → **Bubble Tea**（MVU 纯函数状态机、
语料最厚、渲染=字符串可断言、无 cgo 无子进程）。

### 9.2 Spike-6 实测（千行列表，无头）
```
SPIKE6(bubbletea+1000行,无头): ws 7.9MB  private 13.3MB  idle_cpu_max 0%/core
[spike6] go_heap_inuse=2.1MB go_sys=6.8MB
二进制 2.91MB（-trimpath -ldflags="-s -w"，CGO_ENABLED=0）
```
门禁（≤30MB/≤2%/≤15MB）全过，且为全部候选（含 GUI）中占用最优。

### 9.3 MVP 与验收（tui/ 独立 module）
```
$ go test -race -count=1 ./...      (tui module)
ok  downloader/tui  1.101s           ← model 纯函数测试 11 项（-race）

$ downloader-tui.exe  → 7.74MB PE32+（CGO_ENABLED=0）
```
e2e（e2e/run_tui_selftest.sh，进程级、全自动无人眼）：
```
=== [T1] selftest 全量下载（48MiB，服务端限速 2MiB/s/连接） ===
输出文件存在: 50331648 字节
MATCH: sha256 一致
=== [T2] 中断续传：限速下 1.5s 强杀 TUI 进程 → 重启 selftest 续传 ===
.part 存在: 50331648 字节（预分配）
MATCH: 续传后 sha256 一致
=== TUI SELFTEST E2E DONE ===   （退出码 0）
```

### 9.4 实现期发现并修复的真实缺陷
| # | 缺陷 | 修复 |
|---|---|---|
| 1 | 多个 `cli.RunMulti` 并发共享同一 state.json 会互相覆盖（每实例缓存独立全量写回） | TUI 侧每任务独立 state 子目录（URL 哈希命名），重启续传以 URL 为恢复键 |
| 2 | 队列态任务的引擎完成事件被 `drainDone` 的 `State==Running` 守卫漏抽 → selftest 永不退出 | 引擎启动即置 Running；完成事件统一走每任务 doneCh + tick 抽取（单机制，去除 Program.Send 依赖） |
| 3 | `persist.Store` 打开后缓存不自动刷新，轮询会读到陈旧进度 | 进度轮询直接重读 state.json（约束已写入实现注释） |


## 10. 第 9 轮：MCP 插件 + 开源发布打包（真实执行）

### 10.1 交付
- **模块路径**：`github.com/nymjin22/downloader`（`scripts/rename_module.sh` 一键改名脚本，二次改名一条命令）
- **MCP Server**（`mcp/` 独立 module，官方 go-sdk v1.7）：4 工具
  `download_start`（异步）/ `download_status` / `download_cancel` / `list_tasks`（含历史恢复扫描）
- **allowRemote 产品开关**：`-allow-remote`（默认关闭，H-3 回环边界保持；`validateURL`
  贯穿开关语义，域名解析断言随开关联动）
- **开源打包**：README（三形态安装/MCP 接入配置/实测数据）、MIT LICENSE、.gitignore（排除
  128MB testdata 等大文件）、GitHub Actions CI（windows/ubuntu 矩阵 + tag 触发三平台 Release）

### 10.2 验收证据
```
$ go test -race -count=1 ./...        (mcp module，内存传输全链路)
ok  github.com/nymjin22/downloader/mcp  6.928s
    ↑ start→轮询至 done→文件 sha256 一致；取消→paused→重新 start 续传→done；非法输入 isError

$ python scripts/mcp_smoke.py …       (stdio 冒烟：真实 JSON-RPC 对话)
initialize ok: downloader 1.0.0
tools: ['download_cancel', 'download_start', 'download_status', 'list_tasks']
download_start: {…task_id: t1, state: running}
final state: done
MCP STDIO SMOKE OK                    （退出码 0）

$ go list -m all   （根模块）
github.com/nymjin22/downloader        ← 零第三方依赖保持
```

### 10.3 过程记录
- Wails 式教训再现：冒烟首跑失败原因为测试脚本自身的任务匹配键错误（`task_id` vs `id`）
  与诊断时误杀后台进程——非服务端缺陷；修复后全绿。
- git 初始提交排除 128MB testdata 与测试遗留 bin（.gitignore 固化）。

## 11. 第 13 轮：协议扩展（file/HLS/Metalink4）+ 开源合规（真实执行）

### 11.1 交付
- **file://**（`network/file.go`）：本地复制语义，`Mux` 按 scheme 分发；仅绝对路径
  （host 空/localhost），Windows 盘符 `file:///C:/...` 归一。
- **HLS（RFC 8216）**（`network/hls.go`）：`.m3u8` 自动识别。媒体/主播放列表解析
  （EXTINF/KEY/MAP/BYTERANGE/MEDIA-SEQUENCE/ENDLIST，属性解析含引号转义）、
  **虚拟字节映射**复用引擎的分片并行/工作窃取/字节级续传、AES-128 流式解密
  （CBC + PKCS7 + 尾块滞留，缺省 IV=媒体序列号 128 位大端）、主列表取最高 BANDWIDTH。
  合规守卫：直播流拒绝（无 ENDLIST）、SAMPLE-AES 拒绝（不做 DRM 绕过）、
  播放列表 ≤1MiB/段数 ≤2048/单段 ≤64MiB/密钥 ≤8、跨主机段剥离凭据头。
- **Metalink4（RFC 5854）**（`network/metalink.go`）：token 遍历解析（命名空间无关）、
  priority 升序 failover（≤32 候选）、`<size>` 交叉核对、元数据哈希 → cli **期望值校验**
  （不符删产物判失败——补齐"仅计算打印"的缺口）；输出名取 `<file name>`（显式 -o 优先）。
- **transport 内部扩展**（公开签名冻结不变）：`probe/fetchRange` 带 headers 私有变体
  （HLS 跨主机剥离凭据）、`getBounded`/`openStream` 元数据/流式拉取。
- **testserver**：`/hls/`（明文/AES-128/直播/主列表端点，段自包含 Range 语义）、
  `/key/`、`/meta4/`（failover 正例 + 坏哈希负例）；`-extra name:size` 附加文件。
- **开源合规**：LICENSE 持有人修正（nymjin22 → Mao-jh）；SECURITY.md（报告渠道/安全边界）；
  COMPLIANCE.md（零遥测/合法使用/第三方义务/协议自律）；`scripts/compliance.sh`
  （零依赖断言+LICENSE+遥测扫描+UA+文档五项）；CI 新增 govulncheck 任务与
  workflow `permissions: contents: read` 最小化。

### 11.2 验收证据（原始输出见 test_raw.log / e2e 终端记录）
```
$ go test -count=1 ./...    （核心模块；含新增 file/hls/metalink 单测）
ok  github.com/Mao-jh/porter/network     （解析/AES 往返/PKCS7 损坏/直播拒绝/选流/上限）
ok  github.com/Mao-jh/porter/cli
ok  github.com/Mao-jh/porter/testserver  （HLS 段语义 + Meta4）

$ bash e2e/run_protocol.sh  （真实 exe 端到端，8 用例）
P2 file:///… 全量下载           MATCH: file:// 内容一致
P3 /hls/big.bin.m3u8            MATCH: HLS 明文内容一致（虚拟映射并行分片）
P4 /hls/big.bin.enc.m3u8        MATCH: AES-128 解密内容一致
P5 /hls/big.bin.master.m3u8     MATCH: 主列表选中高码率变体
P6 /meta4/big.bin.meta4         MATCH: priority=1 404 → failover → 哈希校验通过
P7 坏哈希负例                    exit=1 + "哈希不一致" + 产物已删除
P8 直播流负例                    exit=1 + "ENDLIST" 提示

$ bash scripts/compliance.sh   （五项检查）
== 合规检查全部通过 ==（exit 0）

$ go list -m all    （根模块）
github.com/Mao-jh/porter           ← 零第三方依赖保持（第 13 轮未引入任何依赖）
```

### 11.3 边界声明（诚实）
- HLS **仅 VOD**；加密播放列表顺序下载、**无续传**（PKCS7 明文长度不可预知，
  虚拟映射不成立）；BYTERANGE 支持显式/缺省偏移，但跨资源缺省偏移拒绝。
- Metalink failover 仅在探测阶段；传输中途失败不换源（字节级续传状态绑定单一源）。
- 单元测试中 E2E 覆盖 3MiB/8MiB 量级；SFTP/BT/HTTP3 仍明确不做（零依赖约束，见 BENCHMARK）。

## 12. 第 14 轮：代理 / Cookie / 批量任务 / 自动命名（真实执行）

> 门禁：`./run_tests.sh`（vet=0 / 单测=0 / -race=0 / 四套进程级 e2e 全过 / 合规检查全过，
> 原始输出见 `test_raw.log`）。

### 12.1 新增能力与测试证据
| 能力 | 实现 | 测试（真实通过） |
|---|---|---|
| 代理出口 `-proxy` | `network.SetProxy`（http/https/socks5，net/http 原生支持）；代理=显式出站同意（allowRemote 自动置位）；validateURL 跳过目标 DNS 预解析 | `TestSetProxy_Validation`（非法/合法 scheme 分类）、`TestSetProxy_BypassesTargetDNS`（代理模式下目标域名放行 + scheme 白名单不放松）、`TestFetchRange_ViaForwardProxy`（端到端：httptest 转发代理，全量+Range 分片内容断言、请求确实经代理出口） |
| Cookie 文件 `-load-cookies` | `network.ParseNetscapeCookies`（7 列 TAB 格式，# 注释/#HttpOnly_ 前缀，畸形行跳过）+ `Transport.SetCookies/applyCookies`（按域后缀匹配，与 -H Cookie 共存、透传优先，probe/fetchRange/getBounded/openStream 四路径覆盖） | `TestParseNetscapeCookies`、`TestParseNetscapeCookies_Empty`、`TestSetCookies_DomainMatchAndMerge`（echo 回显断言合并顺序与域隔离）、`TestSetCookies_Cleared`、`TestCookieE2EThroughCLI`（CLI 全链路 + sha256 校验通过） |
| 批量任务 `-i` / `-j` | `cli.readURLFile`（每行一 URL，# 注释/空行忽略）；-j 为并发任务上限（只下调不上调 R-3 模式预算） | `TestParse_URLFile`（合并/注释/畸形行报错）、`TestParse_NoURLs`、`TestRun_JobsCap`（-j=1 三任务全完成 + -summary 路径顺带覆盖） |
| 自动文件名 | `Transport.ContentFilename`（RFC 6266 filename + RFC 5987 filename* 优先，quoted-string 转义）；优先级：显式 -o > Metalink 名 > CD > URL 尾段（CD 仅单 URL 启用，多 URL 保持 URL 推导 + 预去重） | `TestParseContentDisposition`（7 用例含 filename*/转义/缺失）、`TestContentFilename`（/cd 端点 HEAD + filename* + 无头/非回环负例）、`TestAutoFilename_CD`（端到端：产物落盘为 `setup v2.exe`） |
| 进度摘要 `-summary` | 每秒 persist.Store 快照单行/任务（状态/进度百分比/大小），终态再输出一次 | `TestPrintSummary`；`TestRun_JobsCap` 全链路覆盖 |
| `porter tasks` 子命令 | `cli.RunTasks/listTasks`（按更新时间倒序，状态/百分比/大小/URL/输出名） | `TestListTasks`（倒序/计数/百分比）、`TestListTasks_Empty` |

### 12.2 H-3 边界回归
- 代理未配置时：非回环目标/域名解析断言拒绝行为不变（`TestSetProxy_BypassesTargetDNS`
  前半段 + 既有 `TestValidateURL_*` 全过）。
- 代理配置后：出站同意语义以「显式配置代理」承接（USAGE.md/DESIGN.md 已声明），
  scheme 白名单与重定向剥离策略不变。

### 12.3 边界声明（诚实）
- Cookie 仅域匹配，不区分 path/secure 维度（cookie.txt 本为用户主动提供的凭据文件）。
- CD 自动命名仅单 URL 且未显式 -o 时启用；多 URL 场景保留 URL 推导 + 预去重。
- SOCKS5 经 net/http 原生支持（socks5:// 代理 URL），未自实现拨号——零依赖约束下最短路径。
- NTLM/摘要认证未实现（标准库无现成实现，使用面窄，见 BENCHMARK §3.2 第 4 条）。

## 13. 第 15 轮：TUI / MCP 形态补齐第 14 轮能力（代理 / Cookie）

> 三形态一致性：`-proxy` / `-load-cookies` 从 CLI 同步到 TUI 与 MCP 服务端入口
> （MCP 经 `mcpserver.Config` 透传，TUI 经 `cli.Options` 复用既有接线）。

### 13.1 变更与证据
| 形态 | 变更 | 测试 |
|---|---|---|
| TUI `porter-tui` | 新增 `-proxy` / `-load-cookies` 旗标（`tui/cmd/porter-tui/main.go` → `cli.Options`） | 全量门禁 TUI 模块（vet/单测/-race + `run_tui_selftest.sh` 进程级 e2e） |
| MCP `porter-mcp` | 新增 `-proxy` / `-load-cookies` 旗标 + `mcpserver.Config.Proxy/CookieFile` → `cli.Options` | **`TestMCP_ProxyAndCookies`**（httptest 转发代理端到端：下载经代理出口完成、代理命中计数>0、cookie 文件正常加载） |
| 共享语义 | 代理=显式出站同意、Cookie 按域匹配注入——与 CLI 完全同源（network 层实现，无重复代码） | network 层既有 9 用例回归 |

### 13.2 边界声明
- MCP/TUI 的代理与 Cookie 语义与 CLI 一致（README/USAGE 已声明）；
  MCP `-allow-remote`（直连）与 `-proxy`（代理出口）为两条独立的受控出站通道。
- TUI 的 `-summary` 不适用（TUI 自带界面）；`-i/-j` 不适用（任务由界面/`-url` 添加）。

## 14. 第 16 轮：CLI 面补齐（-i out= / rm / clean / probe，对标 aria2 / wget）

### 14.1 变更与证据
| 能力 | 实现 | 测试（真实通过） |
|---|---|---|
| `-i` 行内命名 | `readURLFile` 支持 `URL out=name`（空格分隔），out 经 `sanitizeFilename` 净化（防路径穿越/Windows 非法字符）；RunMulti 优先级：`out=` > URL 推导；与 `-o` 目录共存 | `TestParse_URLFileOut`（解析+净化含 `../escape.exe` 穿越用例）、`TestRun_URLFileOut`（端到端：out= 命名与自动命名产物并存落盘） |
| `porter rm` / `porter clean` | `cli.RemoveTasks`：精确删除/仅清 done；running 且有 `.part` 拒绝（防删在途引擎）；连带清理同名 `.part` | `TestRemoveTasks`（done 删除连带 .part、running 拒绝、clean 仅清 done） |
| `porter probe` | `cli.RunProbe`：复用 Mux 探测 + `ContentFilename`，输出 `url=/size=/ranged=/name=`（key=value 脚本友好）；支持 -proxy/-load-cookies/-H；失败聚合报错 | `TestRunProbe`（size/ranged 断言、CD name 断言、非回环报错） |

### 14.2 边界声明
- `rm` 的 running 拒绝条件 = 状态为 running **且** `.part` 存在（进程已退出但残留中间态仍可 rm，需显式二次确认由用户承担）；
- `probe` 不触发下载、不写状态目录（无副作用）。

## 15. 第 17 轮：MCP 探测 / retry 子命令 / 续传守卫 / HLS 自动命名（真实执行）

> 门禁：`./run_tests.sh`（vet / 单测 / -race / 四套进程级 e2e / 合规检查，原始输出见 `test_raw.log`）。

### 15.1 新增能力与测试证据
| 能力 | 实现 | 测试（真实通过） |
|---|---|---|
| MCP `download_probe`（第 5 个工具） | `cli.ProbeURL` 语义同源（buildTransport 统一 proxy/cookie/headers 构建，RunMulti/RunProbe/retry 共用；AllowRemote 透传） | `TestMCP_DownloadProbe`（size/ranged/CD 名）、`TestMCP_DownloadProbe_Errors`（非法 scheme / 非回环 → isError） |
| `porter retry` 子命令 | `cli.ParseRetry/RunRetry`：串行续传重跑 store 中 `status!=done` 任务（按 UpdatedAt 升序，错误聚合，done 跳过） | `TestRunRetry_SkipsDoneAndResumesPending`（done 跳过 + running 重跑至 sha256 一致 + 状态落 done）、`TestRunRetry_NoPending`、`TestParseRetry` |
| 续传守卫（健壮性修复） | 恢复分片计划前校验 `.part` 存在且尺寸==期望；不符删除并全新下载（防"误删 .part 后按旧状态续传产生空洞损坏文件"） | `TestResumeGuard_PartWrongSize`（状态声称 50%，.part 仅 1 字节 → 全新下载 sha256 一致）、`TestRunRetry_*` 覆盖 |
| HLS 自动命名 | 未显式 `-o` 时输出名去 `.m3u8` 后缀（CD 名优先） | `TestRun_HLSAutoName`（/hls/big.bin.m3u8 无 -o → 产物 big.bin，sha256 与源一致，且无 .m3u8 残留） |
| 探测复用重构 | `buildTransport`/`ProbeURL` 抽出，CLI probe 与 MCP 同一路径 | `TestProbeURL_CD`（普通文件 + CD 端点 size/ranged/name）；`TestRun_JobsCap` 等既有门禁回归全过 |

### 15.2 边界声明（诚实）
- `retry` 串行执行（确定性）；单个失败聚合报错，不影响其余任务。
- 续传守卫仅校验 `.part` 尺寸；不校验分片级内容（引擎 Range 强制 + 覆盖守卫已有三层防线）。
- MCP `download_probe` 复用 CLI 语义：默认 H-3 回环；`-proxy` 时目标域解析交给代理。

## 16. 第 18 轮：磁盘空间预检 / -o - stdout 流式（真实执行）

> 门禁：`./run_tests.sh`（vet / 单测 / -race / 四套进程级 e2e / 合规检查 + TUI/MCP 模块，
> 原始输出见 `test_raw.log`）。

### 16.1 新增能力与测试证据
| 能力 | 实现 | 测试（真实通过） |
|---|---|---|
| 磁盘空间预检 | `cli.preflightDisk`（已知大小且非流式时执行；`.part` 已有量折算；不足快速失败）；`diskfree_windows.go`（kernel32!GetDiskFreeSpaceExW 经 stdlib `syscall.NewLazyDLL` 直调，零第三方依赖）/ `diskfree_unix.go`（`syscall.Statfs`，Bavail×Bsize） | `TestDiskFreeBytes`（可用>0）、`TestPreflightDisk_Enough`、`TestPreflightDisk_NotEnough`（MaxInt64 → 报"磁盘空间不足"）、`TestPreflightDisk_PartDeduction`（.part 折算）、`TestPreflightDisk_SkipZero` |
| `-o -` 流式输出 | `cli.runStream`（单连接顺序写 stdout，Metalink/HLS 内容形态包装后短路）；`validateStreamOutput`（单 URL、无 -n 约束） | `TestRun_StreamStdout`（stdout 3MiB sha256 与源一致）、`TestRun_StreamHLS`（HLS 虚拟映射流式 sha256 一致）、`TestValidateStreamOutput`（多 URL/-n 报错） |
| 跨平台编译 | `//go:build` 双实现 | Windows 门禁全过；`GOOS=linux/darwin CGO_ENABLED=0 go build ./cli/` 交叉编译通过 |

### 16.2 边界声明（诚实）
- 预检仅覆盖已知大小场景；流式（`-o -`）与未知大小（size=0）跳过；查询失败（权限/跨平台）
  降级为 stderr 警告，不阻断下载。
- `-o -` 强制单连接顺序流：无分片并行、无断点续传、无完成后校验（stdout 不可寻址，
  与 curl `-o -` 同类取舍）；`-n` 分片与多 URL 同时使用报错。

## 17. 第 19 轮：HTTP/2 显式启用 / -summary 速率与 ETA（真实执行）

> 门禁：`./run_tests.sh`（vet / 单测 / -race / 四套进程级 e2e / 合规检查 + TUI/MCP 模块，
> 原始输出见 `test_raw.log`）。

### 17.1 新增能力与测试证据
| 能力 | 实现 | 测试（真实通过） |
|---|---|---|
| HTTP/2 显式启用 | `http.Transport.ForceAttemptHTTP2=true`（自定义 DialContext 下自动协商被 net/http 关闭；显式强制后 https 目标可多路复用，6 分片共享一条连接） | `TestTransport_ForceHTTP2`（配置断言）、`TestHTTP2_NegotiationAndMultiplexing`（httptest TLS h2 服务端：6 并发请求全部协商 HTTP/2.0） |
| `-summary` 速率/ETA | `cli.summaryTracker`：相邻快照差分算速率（B/s 人读格式化），ETA = 剩余/速率（Xs/Xm Ys/Xh Ym）；done 回落钳 0；`renderAt` 时间可注入 | `TestSummaryTracker_SpeedAndETA`（首帧 "-"；10s 增 16MiB → 1.6MiB/s + ETA 20s；回落 → 0B/s + ETA 未知）、`TestFormatETA`、`TestHumanSpeed`、`TestHumanBytes`、`TestSummaryTracker_Empty` |

### 17.2 边界声明（诚实）
- h2 协商验证需 TLS 信任注入，仅在测试内临时设置 `TLSClientConfig`（产品代码不引入
  自签名信任面）；生产 https 目标走系统信任链 + ALPN 自动协商。
- `-summary` 速率/ETA 基于 1s 采样差分：低速长任务首帧无速率（"-"）；任务重启
  （done 回落）速率钳 0、ETA 显示未知。

## 18. 第 20 轮：probe 最终 URL / summary 速率 EMA 平滑（真实执行）

> 门禁：`./run_tests.sh`（vet / 单测 / -race / 四套进程级 e2e / 合规检查 + TUI/MCP 模块，
> 原始输出见 `test_raw.log`）。

### 18.1 新增能力与测试证据
| 能力 | 实现 | 测试（真实通过） |
|---|---|---|
| probe 最终 URL | `Transport.FinalURL`（HEAD/Range GET → `resp.Request.URL`）；CLI 输出 `final_url=`（仅不同时）；`cli.FinalURLFor` 供 MCP 复用（`final_url` 字段） | `TestRunProbe_FinalURL`（/redirect 302 → final_url 指向目标）、`TestRunProbe_NoFinalURL`（无重定向不输出）、`TestFinalURLFor`（含非回环空串负例）、`TestMCP_DownloadProbe`（final_url 有/无断言） |
| summary 速率 EMA 平滑 | `summaryTracker.ema`（α=0.5）；首个有历史帧播种瞬时值，之后混合；瞬时钳 0；ETA 按平滑速率 | `TestSummaryTracker_SpeedAndETA`（回落帧 EMA=819.2KiB/s + ETA 1m 10s）、`TestSummaryTracker_EMADecay`（16→8→4→2MiB/s 指数衰减） |

### 18.2 边界声明（诚实）
- `final_url` 仅 http(s) 输出；HEAD 失败自动回退 Range GET；与输入相同不输出（避免噪音）。
- EMA 平滑延迟真实速率突变（如限速变化后 2-3 帧收敛）；任务重启（done 回落）瞬时钳 0
  但平滑速率需数帧衰减，ETA 随之逐步恢复——比瞬时值更稳但更"保守"。

## 19. 第 21 轮：porter meta / TUI ETA（真实执行）

> 门禁：`./run_tests.sh`（vet / 单测 / -race / 四套进程级 e2e / 合规检查 + TUI/MCP 模块，
> 原始输出见 `test_raw.log`）。

### 19.1 新增能力与测试证据
| 能力 | 实现 | 测试（真实通过） |
|---|---|---|
| `porter meta`（curl -I 对标） | `Transport.Meta`（HEAD 回退 Range GET → 状态行+Header）；`cli.RunMeta` 输出 `<url> <状态行>` + 排序 `key: value` | `TestRunMeta`（状态行/Content-Length/Accept-Ranges）、`TestRunMeta_Errors`（非回环聚合报错）、`TestTransport_MetaDirect` |
| TUI ETA | `Task.ETA` = (Size-Done)/Speed（防溢出钳 2^62）；view 追加 `ETA 1m 30s`；本地 `formatETA` | `TestRefreshProgressAndSpeed`（ETA 公式接线断言）、`TestViewAssertions`（"ETA 1m 30s" 渲染） |

### 19.2 边界声明（诚实）
- `meta` 与 `probe` 同源（HEAD→GET 0-0 回退路径）；`meta` 不做 body 下载。
- TUI ETA 基于 500ms tick 的瞬时速率差分（未做 EMA，刷新频率高抖动可接受）；
  已知大小且速率>0 时才有 ETA，否则不显示。
