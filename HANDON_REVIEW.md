# 实测评估报告 — Porter 多线程下载器

> 评估方式：独立动手实测（非转述、非仅跑自带验收脚本）。
> 环境：Windows 11 / Git Bash / 本机回环 testserver（固定端口）。
> 日期：2026-09-01

## 结论

**能用，且核心卖点不是吹的。** 官方 `demo.sh` 12/12 通过；我额外手动验证了
脚本没覆盖的场景（FTP、HLS 全家桶、Metalink 正负例、强杀续传、限速精度、
安全边界、错误处理、内存），除了两个测试工具链的小缺陷外，全部符合文档宣称。

一句话评价：作为"AI 的搬运工"，工程完成度高于一般玩具项目——字节级续传、
HLS AES-128、Metalink failover 这些"硬"能力都有真实实现，且自带一键验收脚本，
新环境 5 分钟就能跑通全流程。

## 实测证据（全部本机真实数据）

| # | 项目 | 实测结果 | 判定 |
|---|---|---|---|
| 1 | 官方验收 `./scripts/demo.sh` | PASS=12 FAIL=0 SKIP=0 | ✅ |
| 2 | 64MiB 全速下载 + sha256 | 940ms（≈68MiB/s 回环极限），哈希与参考逐字节一致 | ✅ |
| 3 | 全局限速 4MiB/s | 16.7s（理论 16s），误差 4.5% | ✅ |
| 4 | 全局限速 2MiB/s × 6 分片 | 33s（理论 32s），误差 3% | ✅ |
| 5 | 批量 `-i urls.txt -j 2` | 3 任务全部完成，产物哈希一致 | ✅ |
| 6 | FTP 下载 64MiB | sha256 一致（服务端需带 `-dir`，见问题 1） | ✅ |
| 7 | 强杀续传（37.5% 处 kill -9） | 重启仅补下载剩余 62.5%，40.7s（理论 39.4s），sha256 一致 | ✅ |
| 8 | HLS 明文 64 段并行 | sha256 一致 | ✅ |
| 9 | HLS 主列表选流 | 正确选高码率流，sha256 一致 | ✅ |
| 10 | HLS AES-128 加密 | 自动解密，sha256 一致 | ✅ |
| 11 | HLS 直播流 | 按设计拒绝（合规边界） | ✅ |
| 12 | Metalink4 failover | priority=1 是 404，自动切 priority=2，下载成功 | ✅ |
| 13 | Metalink4 哈希负例 | 期望值比对失败，判失败并删除产物 | ✅ |
| 14 | MCP 全流程 | initialize → 5 工具 → start → status → probe → 产物校验全过 | ✅ |
| 15 | TUI 无头自检 | `--selftest` exit 0 | ✅ |
| 16 | 非回环 URL | 拒绝并显示解析 IP（H-3 安全边界生效） | ✅ |
| 17 | 无参数 / 404 | 退出码 2 + 用法；404 明确报错不重试 | ✅ |
| 18 | 内存占用 | 下载中（6 分片）工作集 ≈16.4MB | ✅ |
| 19 | 任务管理 | `tasks` 列 12 条历史、`clean` 清理完成记录 | ✅ |

## 发现的问题（3 个，均非下载器主链路）

### 1. testserver 的 FTP 服务端在缺省 `-dir` 时全 550（测试工具 bug）
- 现象：`testserver -addr ... -ftp`（不带 `-dir`）时，FTP 的 SIZE/RETR 全部
  返回 `550 access denied`，porter 与 curl 均无法下载。
- 根因：HTTP 服务端对空 `cfg.Dir` 有 `os.MkdirTemp` 兜底（server.go:53-58），
  但 `NewFTPServer` 直接拿到空 `dir`，`resolve()` 里空目录被 `filepath.Clean` 成
  `.`，导致任何路径都判定"越界"。
- 影响面：demo.sh 未覆盖 FTP 段，所以一直没暴露。修复只需在 FTP 路径复用
  同样的 MkdirTemp 兜底。**porter 的 FTP 客户端本身没问题**（带 `-dir` 后立即正常）。
- 复现：`testserver -addr 127.0.0.1:54322 -name big.bin -size 1048576 -ftp`，
  然后任何 FTP 客户端取文件均 550。

### 2. HLS 加密任务在 `porter tasks` 中 size 统计为 0/0B
- 现象：AES-128 HLS 任务下载正常（64MiB、sha256 一致），但 `tasks` 列表显示
  `0/0B (0.0%)`。
- 原因：HLS 加密流走顺序下载，size/done 统计未回填。
- 影响：仅展示层瑕疵，不影响正确性。

### 3. 文档小不一致
- README 对比表写"MCP（4 工具）"，实际 5 个（`download_cancel` 是第 5 个），
  特性区已写 5 个——自相矛盾。
- BUILD.md/README 仍有 `downloader.exe` 旧名残留，bin/ 里新旧产物并存
  （downloader.exe 8/29 构建 vs porter.exe 9/1 构建），容易误导。
- HLS 端点 URL 需带完整文件名（`/hls/<file.bin>.m3u8` 而非 `/hls/<file>.m3u8`），
  README 未给出示例 URL，首次使用容易 404。

## 使用要点（亲测有效）

- **安全边界**：默认只允许 127.0.0.0/8，下公网资源要 `-proxy` 或对应开关——这是
  设计（审计边界），不是缺陷；错误信息会给出解析到的公网 IP，排查友好。
- **断点续传**：异常退出后同参数重启即续传，`-state-dir` 可隔离多任务状态。
  实测 kill -9 后重启只补剩余字节，无重复下载。
- **限速是全局的**：`-limit` 跨任务跨分片共享配额，实测误差 <5%，可用作 CI 断言。
- **HLS**：`.m3u8` 结尾自动识别，加密流自动解密，直播流拒绝；明文流保留分片
  并行与续传，加密流顺序下载。
- **Metalink4**：候选 failover + 期望哈希强制比对，哈希不符自动删产物。
- **MCP**：`porter-mcp -state-root .porter-mcp` 起 stdio 服务，5 个工具即插即用；
  同 URL 重复 start 自动续传。

## 建议

1. 修 testserver FTP 的 `-dir` 兜底（一行的事），并在 demo.sh 补 FTP 段——
   目前 README 宣称的协议矩阵里 FTP 是唯一没被验收脚本覆盖的。
2. 统一命名：清掉 `downloader` 旧产物与文档残留。
3. 修正 README 的 MCP 工具数（4 → 5）。
4. HLS 加密任务补 size 统计回填。

---

# 第二部分：链接发现 / 抗劣化下载 / 下载后处理 实测

> 评估对象：第 23 轮新增能力（`discover/` 包、`media/` 包、cli 抗劣化参数）。
> 环境与方式：同第一部分（Windows 11 / Git Bash / 本机回环 testserver）；
> 可一键复跑：`./scripts/run_discover_media.sh`（退出码 0 = 全过）。

## 结论

三块能力不是"纸面功能"，全部本机实测通过，且与既有下载核心（分片/续传/校验）
同一条链路——链接发现产物可直接喂 `-i`，抗劣化切换保持字节级续传，后处理
产物 sha256 与源一致。零第三方依赖约束全程保持（唯一例外 transcode 调用系统
ffmpeg，缺失时明确报错并给安装指引——诚实降级，非假装支持）。

## 实测证据（全部本机真实数据）

### A. 链接发现（5 个子命令）

| # | 项目 | 实测结果 | 判定 |
|---|---|---|---|
| A1 | `find` 首页链接提取 | 5 条（含相对链接绝对化、外部链接）；正确排除 `#锚点`/`.js`/`.css` | ✅ |
| A2 | `find -ext mp4,mkv -probe` | 过滤生效；外部链接探测被 H-3 拒绝并**明确报错**（安全边界贯通） | ✅ |
| A3 | `find -depth 2` 递归 | 首页 5 + 子页 3（tiny.bin/meta4/hls）= 8 条，页面/资源去重独立 | ✅ |
| A4 | `ls ftp://...` FTP 列目录 | MLSD 列取 6 个条目，`-l` 长格式带大小/修改时间 | ✅ |
| A5 | `bookmarks` 书签解析 | 2 条有效书签 + 标题，`javascript:` 坏链接跳过 | ✅ |
| A6 | `extract` 文本提取 | 混合文本（含中文标点紧邻 URL）提取 3 条，中文文本不误吞 | ✅ |
| A7 | `torrent` 种子解析 | name/length/announce/webseed 全对；**info_hash 与独立 Python SHA1 逐字节一致** | ✅ |
| A8 | `torrent` 磁力解析 | btih/name/tracker 提取 + 使用指引（BT 对等协议不在零依赖范围） | ✅ |
| A9 | `find`/`ls` 走 H-3 边界 | 非回环目标拒绝（外部链接探测报解析 IP），代理 `-proxy` 可显式放行 | ✅ |

### B. 抗劣化下载（弱网 / 冷源 / 跨境 / 限速）

| # | 项目 | 实测结果 | 判定 |
|---|---|---|---|
| B1 | `-mirror` 镜像切换（主源 404） | 主源探测失败 → 自动切镜像，sha256 与源一致 | ✅ |
| B2 | `-min-rate` 慢速保护 | 限速源（50KB/s/连接）×3 分片 = 150KB/s，30s 窗口均值 < 200KB/s 阈值 → 判坏源切不限速镜像，sha256 一致（窗口需满 30s 才判定，非 2s 就误判——修复过采样窗口裁剪 bug） | ✅ |
| B3 | `-stall` 停滞超时 | 单测覆盖（watchQuality 停滞判定），语义：N 秒零进度推进 → fail | ✅ |
| B4 | `-retry-forever` 无限重试 | 不可达端口持续指数退避重试（1s→2s→4s→8s 实测），直到成功或 Ctrl-C；修复前不覆盖探测失败，现已在镜像层兜底全链路 | ✅ |
| B5 | 镜像切换保持续传 | 切换基于字节级 .part 续传（失败源进度已持久化，重启/切源不重下） | ✅ |
| B6 | 默认行为不变 | 未开抗劣化参数时与第 22 轮行为一致（全量回归通过） | ✅ |

### C. 下载后处理（预览 / 转码 / 组织 / 去广告）

| # | 项目 | 实测结果 | 判定 |
|---|---|---|---|
| C1 | `info` MP4 解析 | 真实容器头：duration=12s / 1280x720 / avc1（mvhd/tkhd/stsd 全链路） | ✅ |
| C2 | `info` WAV / PNG / MP3 / FLAC | 1s@44100Hz 立体声；320x240；ID3 标题；采样率——全部纯 Go 解析 | ✅ |
| C3 | `info` 未知类型 | 明确报"无法识别"，仍输出基本信息（不误判） | ✅ |
| C4 | `transcode` WAV→MP3 | 调用本机 ffmpeg 8.1.1 真实转码，产物被 `info` 识别为 mp3@44100 | ✅ |
| C5 | `transcode` 无 ffmpeg / 坏格式 | 无 ffmpeg 报安装指引（单测）；`-to xyz` 明确拒绝 | ✅ |
| C6 | `organize -dedupe` | 按类型归类（video/audio/image/…）+ 同名去重（m.mp4 与 m2.mp4 同内容 → 移入 .dupes/） | ✅ |
| C7 | `scrub` 广告/垃圾清理 | `.url`/`.part`/promo.txt/同媒体名 .txt 共 4 件移入 `.trash/`，媒体文件不误伤 | ✅ |
| C8 | `-dry-run` 只读预览 | organize/scrub 的 dry-run 均只打印计划不移动 | ✅ |
| C9 | 安全语义 | organize/scrub 只移动不删除；垃圾进 `.trash/` 由用户确认后清空 | ✅ |

## 新增发现的问题（2 个，均已在本次修复）

### 1. `-retry-forever` 起初不覆盖探测失败
- 现象：目标端口不可达时立即退出（探测在分片重试循环之外，重试没生效）。
- 修复：无限重试提升到镜像层（`runOneWithMirrors` 全候选失败后按 1s→30s 饱和
  退避从头重试），实测日志确认 1s→2s→4s→8s 指数退避持续重试。

### 2. 慢速判定窗口裁剪 bug + 进度统计口径
- 现象①：采样窗口裁剪 15 点（跨度 28s）永远达不到 30s 判定阈值 → 慢速永不触发；
- 现象②：`progress()` 只统计已完成分片，运行中恒为 0（"已下载 0 字节"）。
- 修复：窗口保留 16 点（跨度 30s）；`progress()` 计入在途 attempt 已写前缀
  （与 `snapshotShards` 同口径）。

### 实测踩坑记录（供后续维护）
- Go regexp 在 `(?i)` 模式下对字符类做 case-fold 闭包（`s↔ſ`、`k↔K`）——
  `[^\x{80}-\x{10FFFF}]` 会误排 ASCII 字母，中文文本剥离必须在代码层完成。
- bencode 字符串**无终止符**：`20:...` 后直接 `e` 关字典，多写一个 `e` 会
  提前关闭根字典（"尾部多余 N 字节"）。
- FTP 列取必须**先 EPSV/PASV 再发 MLSD/LIST**（协议顺序），testserver 的
  `-dir` 空兜底 bug（第一部分问题 1）本次已顺手修复（`NewFTPServer` 空目录
  MkdirTemp）。

## 使用要点（亲测有效）

- 发现产物直接可消费：`porter find <url> > urls.txt && porter -i urls.txt -j 4`；
  `bookmarks`/`extract` 同理，是 `-i` 的天然上游。
- 抗劣化组合拳：`-mirror "备源A,备源B" -min-rate 102400 -stall 30 -retry-forever`
  ——主源断/慢/卡都自动换源，全挂也不放弃，挂机即可。
- 慢速判定是 30s 窗口语义（与 README 一致），判定后切源从断点续传，不重下。
- 转码依赖系统 ffmpeg；无 ffmpeg 时命令给出明确安装指引（诚实降级）。
- organize/scrub 先 `-dry-run` 看计划再执行；垃圾文件进 `.trash/` 可一键清空。

## 建议（后续）

1. `run_discover_media.sh` 纳入 `run_tests.sh` 门禁（当前独立脚本，未挂钩 CI）。
2. `find` 的 `-probe` 对非回环链接默认报 H-3 拒绝——考虑加 `-allow-remote` 开关
   与主下载器对齐（当前 `-proxy` 可显式放行）。
3. 种子解析目前止于元数据 + WebSeed；若未来需要完整 BT 传输，建议以独立 module
   引入（保持核心零依赖），本包接口已为后续预留。


---

# 第三部分：修复复测（第 24 轮，2026-09-01）

> 背景：上一轮 agent 声称"全部修复完成"。本复测用**现场重新构建**的二进制
> （go1.26.2，GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0）逐项验证，
> 不信任旧产物。全部证据为本机真实数据。

## 一、复测结论总表

| 报告问题 | 上轮修复 | 本次复测结果 | 判定 |
|---|---|---|---|
| ① testserver FTP 空 `-dir` 全 550 | NewFTPServer 加 MkdirTemp 兜底 | **修歪了**：兜底建的目录与 HTTP 的临时目录不一致，RETR 仍 550（详见下） | ❌→已纠正 |
| ② HLS 加密任务 `tasks` 0/0B | 未修 | 下载后 `.part` 真实大小回填，`done 2.0/2.0MiB (100.0%)` | ❌→已补修 |
| ③ 文档：MCP 工具数 4→5 | 未修 | README 对比表已改 5 工具 | ❌→已补修 |
| ③ 文档：BUILD.md/README downloader 旧名 | 未修 | BUILD.md 全量 porter 化；bin/ 旧产物已清 | ❌→已补修 |
| ③ 文档：HLS 端点 URL 示例 | 未修 | README 用法区补 `<名称>.m3u8` 示例 | ❌→已补修 |
| ④ `-retry-forever` 不覆盖探测失败 | 提升到镜像层无限退避 | 不可达端口 12s 内 4 次退避（1s→2s→4s→8s），日志确认 | ✅ 有效 |
| ⑤ 慢速判定窗口 + progress 口径 | 16 点窗口 + 在途前缀 | 限速源 30s 判慢切镜像，产物哈希一致 | ✅ 有效 |
| ⑥ testserver MLSD/LIST + /page/ 路由 | 新增 | `ls` FTP 5 条目、`find` 页面提取 5 链接 | ✅ 有效 |

## 二、上轮修复的实质性缺陷（本次发现并纠正）

**testserver FTP 兜底修歪了——目录不一致导致 550 依旧。**
- 现象：`testserver -addr ... -ftp`（无 `-dir`）时，HTTP 能下载、FTP 仍
  `RETR 应答 550`（`分片 [0,0) 下载失败`）。
- 根因：`testserver.New(cfg)` 是**值传递**，其内部对空 `cfg.Dir` 的 MkdirTemp
  只改了副本；main 的 `cfg.Dir` 仍为空串，随后 `NewFTPServer(cfg.Dir="")`
  又兜底 MkdirTemp——**HTTP 文件在目录 A，FTP 在空目录 B 里找文件**。
- 修复：`cmd/testserver/main.go` 在调用 New 前显式 MkdirTemp 并写入
  `cfg.Dir`（HTTP/FTP 共享同一目录）；NewFTPServer 的空目录兜底保留（
  防御直接调用方）。
- 复测：R1a/R1b/R1c 全过——FTP 无 `-dir` 下载成功，sha256 与 HTTP 参考一致。

## 三、本次补修的遗留问题

1. **HLS 加密任务 size 回填**（`cli/cli.go`）：HLS 加密流 Probe 返回
   `(0,false)`（播放列表长度不可预知，network/hls.go:112-114）→ size=0。
   修复：下载完成后用 `.part` 真实大小回填（仅展示用；HLS 完整性由虚拟映射
   + 流式 sha256 保证，分片守卫对 HLS 跳过）。复测：`done 2.0/2.0MiB (100.0%)`。
2. **README 对比表 MCP 4→5**（README.md:235，此前自相矛盾）。
3. **README HLS URL 示例**：补 `porter http://host/hls/movie.bin.m3u8`
   （URL 需带完整文件名）。
4. **BUILD.md 旧名清理**：downloader.exe/downloader_linux → porter.exe/
   porter_linux（含 go.mod 模块名说明）；bin/ 下 8/29 旧产物已删除。

## 四、全量回归证据（修改后重新跑，防回退）

| 项目 | 结果 |
|---|---|
| `go vet ./...` | 干净 |
| `go test ./...` | 全部 ok（cli 55s / discover / media / network / testserver 等） |
| `./scripts/demo.sh`（官方验收） | **PASS=12 FAIL=0 SKIP=0** |
| `./scripts/run_discover_media.sh`（链接发现/抗劣化/后处理） | **PASS=16 FAIL=0** |
| `./scripts/retest_fixes.sh`（定向复测 5 修复点） | **PASS=9 FAIL=0** |

定向复测细节（retest_fixes.sh，本仓库可复跑）：
- R1a/b/c：FTP 空 -dir MkdirTemp + 下载成功 + 哈希一致
- R2a/b：HLS AES-128 下载成功 + tasks 2.0/2.0MiB 回填
- R3：不可达端口 `-retry-forever` 12s 内 4 次指数退避（1s→2s→4s→8s）
- R4a/b/c：限速源（50KB/s×3<200KB/s 阈值）30s 判慢 → 切镜像 → 哈希一致

## 五、结论

"全部修复完成"的说法**不成立**：第 24 轮复测发现 5 项未修/修歪（① 目录
不一致、② HLS size、③ 文档三件套），均已在本轮补修并复测通过；上轮真正的
有效修复（④ 无限重试、⑤ 慢速窗口、⑥ MLSD/LIST 与 /page/）经全量回归确认
未回退。当前全部门禁绿色：demo 12/12、discover/media 16/16、定向 9/9、
vet/test 干净。

遗留建议：
1. ~~`run_discover_media.sh` / `retest_fixes.sh` 未挂进门禁~~ **已落地**：`run_tests.sh`
   新增 **[T5b] 段**（链接发现/抗劣化/后处理 + 修复点定向复测），T4 构建产物统一为
   `porter_linux`（原 `downloader_linux` 旧名残留一并修正）；`retest_fixes.sh` 端口
   基址参数化（`PBASE=` 可覆盖）避免与门禁其他段冲突。**另发现并修复 `e2e/run_multi.sh`
   引用已删除的 `bin/downloader.exe`（门禁 T5 段必挂的隐患）**。完整 `./run_tests.sh`
   实测 7m35s 全绿：vet/test/race 0、T5 各 e2e 段 0、demo 12/12、discover_media 16/16、
   retest 9/9、T6 依赖仅本 module、T7 合规通过。
2. `find -probe` 对非回环链接默认 H-3 拒绝，可考虑 `-allow-remote` 开关
   （同第一部分建议 2）。
3. ~~附注：`run_tests.sh` 的 `xxx_exit=$?` 均取在管道 `| tee` 之后，测的是 tee 的
   退出码（恒 0，无断言意义）~~ **已修复**：全部 19 处改为 `${PIPESTATUS[0]}`
   取管道左侧命令的真实退出码（含 T4b/T4c 子 shell 内 6 处）；`bash -n` 通过、
   机制验证（false→1/true→0）、`quick` 与完整门禁复跑均正常。
   **该修复立即暴露 3 个被恒 0 掩盖的历史脚本 bug（已全部修复）**：
   - `e2e/run_e2e.sh` [E6]、`e2e/run_ftp.sh` [F6]：断言段（`porter` 无参数/非回环
     URL 等故意失败命令）在 `set -e` 下直接杀死脚本——任何环境必挂，仅因 tee 恒 0
     一直"假装通过"。修复：断言段包 `set +e`/`set -e`。
   - `e2e/run_tui_selftest.sh` 等 6 个 e2e 脚本：清理 `rm -rf` 删工作区测试数据，
     在沙箱批量删除守卫下失败触发 `set -e`。修复：全部清理 rm 加 `|| true` 容错。
   - 修复后沙箱外独立验证 3 脚本 EXIT=0；沙箱内完整门禁 13 段退出码**全部真实为 0**
     （e2e/ftp/tui 从 1 → 0），MATCH 断言全命中，demo 12/12、discover 16/16、retest 9/9。
4. **门禁现已可作 CI 断言**：新增 **[T8] 聚合段**——`gate_check` 解析 test_raw.log
   全部 `*_exit=` 记录（正则已覆盖带数字的段名，如 `e2e_exit`/`porter_build_exit`），
   任一非 0 → `GATE_RESULT: HAS_FAILURES` 且进程退出码非 0（quick 模式同样聚合）。
   同时修复 T4b/T4c 子 shell 退出码落盘路径（原 `log` 相对路径写进 tui//mcp/
   子目录，根日志缺失 6 个段）与 T4 构建/T6 的退出码记录。完整门禁实测 9m55s：
   **23 个段全部 GATE-OK → ALL_PASS**。


---

# 第四部分：第 25 轮独立复测（2026-09-01）

> 背景：上一轮 agent 声称"全部修复完成"且第 24 轮复测确认 23 段 ALL_PASS。
> 本轮按同样标准**独立复测**：不信任任何旧产物，现场删除并重新构建
> （go1.26.2，GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0），重跑完整门禁，
> 并对两个关键修复点做脚本之外的手动抽查。

## 一、复测结论

**"全部修复完成"的说法本轮成立。** 完整门禁 10m51s 跑完，23 段退出码全部
真实为 0（T8 聚合逐条断言），`GATE_RESULT: ALL_PASS`；关键修复点 ① FTP 空
`-dir`、② HLS tasks size 回填，除门禁内定向复测外，我另起独立端口手动复验
通过。无回退、无新增缺陷。

## 二、复测方式（拒绝信任）

1. `rm -f bin/porter.exe bin/testserver.exe bin/porter_linux` 后现场重新构建，
   产物时间戳 07:31（porter.exe 7.0M / porter_linux 6.8M / testserver.exe 5.9M）。
2. `./run_tests.sh` 完整门禁（T0–T8），全程记录于 test_raw.log。
3. 修复点 ①/② 手动抽查：独立端口 + 独立临时目录，不经由任何验收脚本。

## 三、门禁结果（2026-09-01 实测）

| 段 | 内容 | 结果 |
|---|---|---|
| T1 vet | `go vet ./...` | exit 0 |
| T2 单测 | 11 包全部 `ok`（cli 61.7s 等） | exit 0 |
| T3 -race | 核心 11 包 + tui + mcp 全 ok | exit 0 |
| T4 构建 | porter.exe / testserver.exe / porter_linux 三产物 | exit 0 ×3 |
| T4b TUI | vet / race / build | exit 0 ×3 |
| T4c MCP | vet / race / build（mcp 冒烟含 initialize→5 工具→start→status→probe→产物） | exit 0 ×3 |
| T5 e2e | e2e / resume / multi / ftp / protocol / tui 六段（64MiB 哈希、强杀续传、FTP 续传、非回环拒绝） | exit 0 ×6 |
| T5b | demo **12/12**；discover_media **16/16**；retest **9/9** | exit 0 ×3 |
| T6 依赖 | 仅本 module（零第三方） | exit 0 |
| T7 合规 | 依赖/许可证/遥测/UA/文档 5 项 | exit 0 |
| **T8 聚合** | **23 段 GATE-OK → GATE_RESULT: ALL_PASS** | exit 0 |

门禁总耗时 10m51s，`GATE_EXIT=0`。

## 四、手动抽查（脚本之外，独立复验）

**修复点①：testserver FTP 空 `-dir`（第 24 轮"修歪"处，现确认已正）**
- 操作：`testserver -addr 127.0.0.1:54322 -name big.bin -size 4194304 -ftp`
  （无 `-dir`），日志确认 HTTP 与 FTP 共享同一 `MkdirTemp` 目录
  （`dltest339931916`），不再出现"HTTP 目录 A / FTP 目录 B"分叉。
- 结果：`porter ftp://127.0.0.1:4984/big.bin`（随机 FTP 端口）下载成功，
  产物 sha256 `2be2c277...` 与 HTTP 参考逐字节一致 → **HASH_MATCH**。

**修复点②：HLS AES-128 任务 size 回填**
- 操作：独立 testserver（端口 54901）提供 2MiB 源，下载
  `/hls/big.bin.enc.m3u8`，`-state-dir` 隔离。
- 结果：下载成功，产物 2097152B，sha256 `21b97c21...` 与源文件一致；
  `porter tasks` 显示 **`done 2.0/2.0MiB (100.0%)`**——不再是 0/0B。

**文档三件套抽查（grep 全仓）**
- README MCP 工具数：L73/L236 均为"5 工具（start/status/cancel/list/probe）" ✅
- README HLS 示例：L161 已补 `<名称>.m3u8` 完整文件名示例 ✅
- BUILD.md/README：`downloader` 旧名 0 残留；bin/ 仅 porter 系产物 ✅

## 五、结论

与第 24 轮复测一致且无回退：全部门禁绿色（23/23 段 ALL_PASS），关键修复点
经独立手动复验有效，文档与产物命名统一。当前仓库状态可作为可靠基线：
`./run_tests.sh` 退出码 0 即为 CI 可断言的全量通过信号。

遗留观察（非缺陷，供后续迭代）：
1. `find -probe` 对非回环链接仍默认 H-3 拒绝，需 `-proxy`/`-allow-remote`
   显式放行（README 已说明，属设计边界）。
2. 完整门禁约 11 分钟，其中 cli 单测（55–62s）与 MCP -race（16s）占大头；
   若需提速可拆 quick 门禁（`./run_tests.sh quick` 仅 vet+单测，已验证可用）。
