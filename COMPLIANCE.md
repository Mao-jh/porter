# 合规说明（Compliance）

本文档说明 Porter 的合规立场：隐私、合法使用边界、第三方组件义务与协议行为自律。
这不是法律意见；将 Porter 用于任何场景前，请自行确认当地法律与服务条款。

## 1. 隐私（零遥测）

- **不收集、不上报、不跟踪**：无使用统计、无崩溃上报、无更新检查外呼、无内嵌分析 SDK。
- 对外网络行为**仅一种**：把用户提供的 URL 发往该 URL 指向的服务器（以及 HTTP 语义内的重定向）。
- 本地状态文件（`state.json`）记录任务 URL 与进度，用于断点续传；目录权限 `0700`、
  文件权限 `0600`，仅属主可读。删除 state 目录即删除全部本地痕迹。
- 默认仅允许回环目标（H-3）；`-allow-remote` 打开前的行为无法触达公网。

## 2. 合法使用边界

Porter 是通用下载工具。使用者应自行保证：

- 仅下载**有权获取**的内容（自有文件、开源/开放许可资源、获得授权的内容）；
- 遵守目标站点的服务条款与当地法律；不要用 Porter 规避付费、访问控制或身份验证；
- **本项目内置不提供**任何 DRM 绕过能力（HLS 的 `SAMPLE-AES` 等加密方法被显式拒绝，
  代码中这是硬性策略而非配置项）；也不要将其用于侵权用途。

滥用本项目造成的后果由使用者承担；维护者不对第三方内容合法性负责。

## 3. 第三方组件义务

- **核心模块（cli/scheduler/network/io/persist/hash/testserver）**：零第三方运行时依赖
  （`go list -m all` 仅本模块），因此核心不引入任何第三方许可义务。该事实由
  `scripts/compliance.sh` 与 CI 门禁持续断言。
- **tui/**（独立 module）：`charmbracelet/bubbletea` 等——MIT。
- **mcp/**（独立 module）：`modelcontextprotocol/go-sdk`——MIT。
- 各 module 的精确依赖树以各自 `go.mod` 与 `go.sum` 为准；升级依赖时须复核其许可证
  兼容性（本项目整体 MIT，仅兼容 MIT/BSD/Apache 类宽松许可）。

## 4. 协议行为自律（客户端合规）

| 行为 | 政策 |
|---|---|
| 自我标识 | 默认 UA `Porter/<版本> (+https://github.com/Mao-jh/porter)`，便于服务端识别与日志归因；用户可用 `-H "User-Agent: ..."` 覆盖 |
| 服务端限流 | 429/5xx 指数退避；显式尊重 `Retry-After` 头（上限 60s） |
| 凭据传播 | 重定向跨主机、HLS 跨主机段：剥离 `Cookie`/`Authorization`/`Proxy-Authorization` |
| FTP | 被动模式反弹防御（RFC 2577）；FTPS 证书强制校验，无跳过开关 |
| 任务有限性 | HLS 直播流（无 `#EXT-X-ENDLIST`）拒绝——不发起无终点下载 |
| 资源有界 | 见 [SECURITY.md](SECURITY.md) §3 第 5 条 |
| 内容完整性 | Metalink 元数据哈希强制比对；`-verify` 流式校验；分片覆盖守卫 |

## 5. 许可证

本项目以 [MIT](LICENSE) 许可证发布，Copyright (c) 2026 Mao-jh。
再分发时须保留 LICENSE 与本说明中的相关声明。
