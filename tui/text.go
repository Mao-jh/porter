// UI 文案集中管理（全中文，用户要求）。
//
// 所有界面文本一律经本表取值，禁止在布局/overlay/渲染代码里直接写界面文案。
// 专有名词保持原样：HTTP / BT / 磁力 / URL / KB / MB / GB / /s 等。
// 内部状态字（verdict() 的 "ok"/"failed"、persist Status "done"）非 UI 文案，不在此表。
package tui

// 应用标识
const (
	AppTitle   = "downloader-tui"
	AppVersion = "v2.4.0"
)

// header 状态段（右侧，从右往左排）
const (
	HdrActive = "▼ %d 下载中"
	HdrSpeed  = "↓ %s/s"
	HdrQueued = "⋯ %d 排队"
	HdrPct    = "✓ %d%%"
	HdrLayout = "布局 %s"
)

// footer 键帽说明（§7.1 上下文相关）
const (
	CapAdd      = "添加"
	CapPause    = "暂停"
	CapResume   = "继续"
	CapDelete   = "删除"
	CapOpen     = "打开"
	CapSettings = "设置"
	CapFilter   = "过滤"
	CapFilterOn = "过滤 ✓"
	CapHelp     = "帮助"
	CapQuit     = "退出"
	CapTabHint  = "[Tab] 布局%s"
)

// 布局 A 协议信息（右端）
const (
	LblConn = "%d 连接"
)

// 数值行状态备注（metaLine / 队列项 ETA 列 / 表格 ETA 列共用）
const (
	StPaused    = "已暂停"
	StFailed    = "失败"
	StQueued    = "排队"
	StDone      = "完成"
	StCompleted = "已完成"
	StLeft      = "%s 剩余"
)

// 布局 B 右栏
const (
	LblQueue      = "队列"
	LblCompleted  = "已完成"
	LblThroughput = "吞吐"
	LblChunks     = "分片"
	LblChunkTotal = "%d 片"
	LblChunkDone  = "▓ 完成 %d"
	LblChunkFly   = "▒ 在途 %d"
	LblChunkWait  = "░ 待取 %d"
	LblURL        = "URL"
	LblSaveTo     = "保存到"
	LblConns      = "连接"
	LblPeakAvg    = "峰值 %s · 均值 %s"
	LblTime60s    = "-60秒"
	LblNow        = "现在"
	LblConnStr    = "%d 连接"
)

// 布局 C
const (
	CardTotalSpeed = "总速度"
	CardTasks      = "任务"
	CardDownloaded = "已下载"
	CardQueueEta   = "队列剩余"
	CardActive     = "%d / %d 进行中"
	CardRemaining  = "%d 待处理"
	CardPctOf      = "%d%% / %s"
	ColFile        = "文件"
	ColProgress    = "进度"
	ColDone        = "完成"
	ColSpeed       = "速度"
	ColEta         = "剩余"
	ColSize        = "大小"
	ColConn        = "连接"
	LblPeak        = "峰值 %s"
	LblCap         = "上限 %s/s"
	LblTime2m      = "-2分"
)

// Overlay 标题（§7.2）
const (
	OvHelp       = "帮助"
	OvDeleteConf = "确认删除"
	OvSpeedLimit = "限速"
	OvAddTask    = "添加任务"
	OvProxy      = "代理"
)
