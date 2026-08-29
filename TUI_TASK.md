# TUI_TASK.md — 第 8 轮目标任务书：TUI 前端（Bubble Tea），AI 为第一用户

> **性质**：任务书（替代 [GUI_TASK.md](GUI_TASK.md)，后者归档为选型证据）。
> **裁决链**：Gio ⛔（内存门禁）→ GUI 全线放弃（用户裁决）→ **TUI，AI 为第一用户**（本轮）。
> **为什么 TUI 是 AI 第一用户的形态最优解**：
> 1. **全程 Go 单语言**——AI 语料最厚的领域，无 HTML/CSS/Win32/线程亲和等 lore 依赖；
> 2. **状态机可无头测试**——Bubble Tea 的 MVU 架构（Model/Update/View 均为纯函数），
>    逻辑用 `go test` 全覆盖，**渲染结果是字符串，可断言**——GUI 需要"人眼验收"的部分归零；
> 3. 无 cgo、无子进程、无 GPU 管线——构建约束 `CGO_ENABLED=0` 完整保留；
> 4. 占用与 GUI 不在一个量级（预期 <20MB，Spike-6 实测为准）。

## 〇、选型（AI 可开发性优先）

| 候选 | AI 可开发性 | 备注 |
|---|---|---|
| **Bubble Tea** (charmbracelet) | ★★★ | Go TUI 事实标准（2021+），MVU 架构纯函数、生态最大（bubbles/lipgloss）、teatest 无头测试、训练语料海量 |
| tview (rivo) | ★★☆ | 表格/表单控件全，但状态在 widget 树里（命令式），可测性差于 MVU |
| termui/gocui/tcell 裸写 | ★☆☆ | 语料少或太底层 |

**选定：Bubble Tea + bubbles(textinput) + lipgloss**，纯 Go、`CGO_ENABLED=0`。
Spike-6 实测门禁：Private ≤ 30MB、空闲 CPU ≤ 2%、二进制 ≤ 15MB（TUI 没理由不达标，
超标即回退 tview 重选）。

## 一、架构与依赖边界（沿用既有裁决）

```
tui/                          ← 独立 Go module（module downloader/tui；require downloader + replace ../）
  model.go model_test.go      ← 状态机（纯函数，-race 全覆盖，不依赖终端）
  view.go                     ← View() 字符串渲染（可断言）
  keys.go                     ← 按键语义
  cmd/downloader-tui/main.go  ← 入口 + --selftest 无头自检模式
gui/                          ← 归档（GUI 选型证据，不再演进）
core                          ← 零改动
```

**与 GUI_TASK 相同的铁律**：
1. 第三方依赖（bubbletea/bubbles/lipgloss）只进 `tui/` module，根模块 `go list -m all` 保持仅 `downloader`（前后留证）。
2. 依赖方向 `tui → core 公开 API`，core 零改动（首选）；逃生舱增补需 DESIGN 附录记录。
3. 线程模型天然简化：Bubble Tea 的 Cmd 在独立 goroutine 执行、Msg 单线程投递 Update——
   **UI 状态机无锁、race-free by construction**；引擎 goroutine 只通过 tea.Cmd/tea.Msg 与 UI 交互。

## 二、功能范围

- **G-A Spike-6**：Bubble Tea 千行列表，静置采样（门禁见 §〇）。
- **G-B MVP**：
  - 任务列表：输出名 / URL / 大小 / 进度条 / 速度 / 状态（含 sha256 校验结果透出）；
  - 添加任务：`a` 聚焦输入框 → 输入 URL → Enter 启动（每任务独立 ctx + 独立 `cli.RunMulti`，
    天然支持单任务暂停/继续）；
  - 控制：`↑/↓` 选中、`p` 暂停/继续（取消 ctx 后重新入队，persist 字节级续传自动生效）、
    `d` 删除记录、`q` 退出（取消全部 → 引擎 flush 落盘）；
  - 进度数据源：**直接读 `state.json`**（注意：`persist.Store` 实例打开后缓存不自动刷新，
    轮询须重读文件——这是 core 现状的真实约束，写死在实现注释里）；
  - 引擎 stderr 输出（`[verify]`/`完成` 行）重定向到日志文件，不污染终端界面。
- **G-C 验收**：`--selftest` 无头模式（预置 URL → 自动下载 → 完成自动退出 → 退出码 0/1）
  接入 e2e 脚本，与 testserver 组成**全自动化、无人眼**的验收闭环；
  旧门禁不回退（run_tests.sh 全绿）；`go test -race ./tui/...` 全绿。

## 三、验收门禁清单（全部通过 ✅）

- [x] Spike-6 实测表：13.3MB / 0% / 2.91MB
- [x] 根模块 `go list -m all` 仍仅 `downloader`（tui/gui 独立 module 隔离）
- [x] `go test -race ./...`（tui module，model 纯函数测试 11 项：添加/完成/失败/暂停/删除/恢复/速度差分/格式化）全绿
- [x] `View()` 字符串断言测试（进度条渲染、状态列文案、速度）
- [x] `--selftest` e2e：T1 48MiB 下载 → sha256 一致 → 自动退出 exit=0
- [x] 中断续传：T2 强杀后重启 → 续传完成 sha256 一致 exit=0
- [x] 旧门禁不回退：根模块 build/vet/test 全绿
- [x] TEST_REPORT §9 新章节；BENCHMARK #7 更新为「TUI 已实现」

## 四、估时

G-A Spike(0.25d) → G-B MVP(0.75d) → G-C 验收(0.5d)，合计 **1~1.5 天**（TUI 无视觉调优成本）。

## 五、执行结果（2026-08-29，全部真实证据）

**✅ 全部门禁通过，G-C 验收完成。**

- **Spike-6 实测**（门禁 ≤30MB/≤2%/≤15MB）：Private **13.3MB**、空闲 CPU **0%**、二进制 **2.91MB** —— 为全部候选（GUI 四框架 + TUI）中最优，比最优 GUI（walk 21.5MB）还小 38%。
- **MVP 交付**：`tui/` 独立 module（Bubble Tea v1.3 + bubbles/lipgloss）——任务列表（进度条/速度差分/状态色）、`a` 添加、`p` 暂停·继续（字节级续传）、`d` 删除、`q` 退出；启动时自动从磁盘恢复历史任务；`--selftest` 无头验收模式（终态自动退出，退出码 0/1）。
- **架构要点（实现期沉淀）**：
  1. **每任务独立 state 子目录**（URL 哈希命名）——规避多个 `cli.RunMulti` 并发写同一 state.json 互相覆盖（core 现状约束，注释已写明）；
  2. **引擎完成事件统一走每任务 `doneCh`，tick 轮询抽取**——曾用 tea.Cmd/Program.Send 双机制导致队列态任务完成事件漏抽（自检挂起 bug），重构后单机制、无 Program 依赖；
  3. 进度轮询**直接重读 state.json**（persist.Store 实例缓存不自动刷新的 core 现状约束）。
- **-race 全绿**；`View()` 字符串断言测试（进度条/状态列/速度）；
- **e2e（e2e/run_tui_selftest.sh）**：T1 全量 48MiB 下载 sha256 一致、自动退出 exit=0；T2 限速下 1.5s 强杀 → .part 保留 → 重启续传完成 sha256 一致 exit=0。
- **根模块纯净**：`go list -m all` 仍仅 `downloader`；旧门禁（build/vet/test 全包）零回退。
- **二进制**：downloader-tui.exe 7.74MB（-trimpath -ldflags="-s -w"，CGO_ENABLED=0）。
