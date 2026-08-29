# PROTOCOL_TASK.md — 第 13 轮任务书：协议扩展与合规化

> 前置：第 12 轮已落 FTP(S)（Mux 分发、Fetcher 契约、UA 标识、Retry-After、
> 重定向跨主机剥离凭据、PASV bounce 防御）。本轮在其上扩展协议面并做开源合规包。

## 一、硬约束（不得违反）

1. **零第三方运行时依赖**（B-1/H-4 冻结）：全部新协议只用标准库。
   因此 SFTP（需 x/crypto）、BitTorrent、HTTP/3（需 QUIC）明确**不做**，诚实记录差距。
2. **H-3 回环边界**：新协议全部复用既有校验（HTTP 侧 validateURL / 拨号层断言，
   FTP 侧回环强制）。file:// 无网络行为，不在 H-3 范围，但路径校验从严。
3. **DESIGN §二 契约只增不改**：新增构造器/类型，冻结签名不动。
   `Transport.FetchRange` 内部抽出带头参数的私有变体（签名与语义不变），供 HLS
   按「跨主机剥离凭据」策略注入头（与重定向策略同源）。

## 二、协议扩展（三项）

### 2.1 file://（本地文件）
- `network/file.go`：`FileTransport`，Probe=stat，FetchRange=Seek+LimitReader。
- host 必须为空或 localhost；仅绝对路径；Windows 盘符形式 `/C:/x` 归一。
- 用途：离线镜像、e2e 闭环、本地复制。

### 2.2 HLS（HTTP Live Streaming，RFC 8216）——超越 aria2 的协议面
- `network/hls.go`：`HLSTransport` 实现 Fetcher。URL 路径以 `.m3u8` 结尾自动启用。
- **虚拟字节映射**：媒体播放列表 → 段序列（Probe 并发 HEAD 定长）→ 映射为一段连续
  虚拟空间；cli 引擎的分片并行 / 工作窃取 / 字节级续传 / sha256 校验**零改动**复用。
- 主播放列表：取 BANDWIDTH 最高变体（深度 ≤1）。
- **AES-128**（crypto/aes + CBC）：密钥懒取缓存、IV 显式或缺省=媒体序列号（RFC 8216 §5.2）、
  流式解密（64KiB 块 + 尾块滞留 + PKCS7 去填充）；加密播放列表退化为顺序全量
  （密文含 padding，明文长度不可预知，虚拟映射不成立——诚实降级，续传不可用，文档标注）。
- **合规边界**：
  - 仅 VOD：无 `#EXT-X-ENDLIST`（直播流）直接拒绝——下载任务必须有限；
  - 资源上限：播放列表 ≤1MiB、段数 ≤2048、单段 ≤64MiB、密钥 ≤8 个；
  - 跨主机段：Cookie/Authorization/Proxy-Authorization 剥离（与重定向策略同源）；
  - 加密仅支持 AES-128；SAMPLE-AES 等显式拒绝（**不做 DRM 绕过**）。

### 2.3 Metalink4（RFC 5854）
- `network/metalink.go`：`.meta4`/`.metalink` 后缀自动识别，GET（≤1MiB）+ encoding/xml 解析。
- `<url priority>` 升序 failover（探测阶段逐候选，≤32 个）；`<size>` 与服务端交叉核对；
- **元数据哈希自动校验**：`<hash type="sha-256|sha-1|md5">` → 下载后与实际值比对，
  不一致判任务失败并删除产物——补齐「期望值校验」缺口（此前只有"计算并打印"）。

## 三、合规化

| 项 | 动作 |
|---|---|
| LICENSE 持有人 | `nymjin22` → `Mao-jh`（仓库改名后的真实遗留问题） |
| SECURITY.md | 新增：报告渠道、支持版本、安全边界（H-3/allow-remote） |
| COMPLIANCE.md | 新增：零遥测声明、合法使用边界、第三方组件义务（核心零依赖实证） |
| scripts/compliance.sh | 新增：零依赖断言、许可证存在性、遥测关键词扫描、UA 自标识检查 |
| CI 供应链 | ci.yml 加 govulncheck 任务 + workflow `permissions` 最小化 |
| 文档 | README/USAGE/DESIGN §2.3b/BENCHMARK/TEST_REPORT 同步；对比表协议行如实更新 |

## 四、验收门禁

- 单测：file/HLS（解析、AES、上限、直播拒绝、跨主机剥离）/Metalink（优先级、哈希映射、上限）。
- e2e `e2e/run_protocol.sh`：file:// sha256 闭环；HLS 明文/AES-128/主列表选流 sha256 闭环；
  Metalink failover + 自动哈希校验（含坏哈希负例）；直播流拒绝负例。
- `./run_tests.sh` 全量全绿；`go list -m all` 仍仅自身。
