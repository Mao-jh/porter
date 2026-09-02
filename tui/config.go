// 布局参数集中管理（方案 §5 / §8.4）。
//
// 这些是"可调但默认固定"的布局参数，集中在此便于调整，不散落在渲染代码里。
// 颜色 token 在 tokens.go（设计语言，改需谨慎）；状态/键位映射在 glyphs.go / model.go（逻辑固定，硬编码合理）。
package tui

// 布局自适应阈值（§5.0 表：<100 A / 100–140 B / >140 C）
const (
	LayoutAWidthMax = 99  // 低于此用 A
	LayoutBWidthMax = 140 // 低于等于此用 B，超过用 C
)

// 布局最小尺寸约束（§8.4：窗口太小自动降级，不放大输出）
const (
	MinWindowW  = 50  // View 层最小逻辑宽
	MinWindowH  = 8   // View 层最小逻辑高
	LayoutBMinW = 48  // 布局 B 所需最小宽（否则降级 A）
	LayoutCMinW = 120 // 布局 C 所需最小宽（卡片总宽，否则降级 B）
	LayoutCMinH = 20  // 布局 C 所需最小高（否则降级 B）
)

// 布局 A（§5.1 紧凑单列）
const (
	LayoutAItemStep  = 4  // 每项 3 内容行 + 1 间隙
	LayoutABarOffset = 38 // 进度条宽 = 窗口宽 - 38（100 列 → 62）
	LayoutASparkW    = 20 // 行内 sparkline 宽
)

// 布局 B（§5.2 主从双栏）
const (
	LayoutBRW     = 63  // 右栏基准宽（窄窗压缩）
	LayoutBNarrow = 100 // 低于此宽开始压缩右栏
	LayoutBRWMin  = 20  // 右栏压缩下限
	LayoutBBarW   = 26  // 左栏迷你进度条宽
)

// 布局 C（§5.3 仪表盘）
const (
	LayoutCCardGap   = 2  // 卡片间空隙
	LayoutCTableMax  = 10 // 表格最大行数（表头 + 8 数据 + 1）
	LayoutCTPRows    = 11 // 吞吐图满高行数
	LayoutCTPRowsMin = 4  // 吞吐图最小行数
	LayoutCBarW      = 26 // 表格进度条宽（§5.3 BW）
)

// 渲染原语默认参数
const (
	SparkWidthDefault = 20 // 默认 sparkline 宽度
	ProgressEighth    = 8  // 亚字符 1/8 精度档位
)
