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
