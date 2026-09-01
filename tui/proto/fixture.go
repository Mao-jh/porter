// Package proto 是下载器 TUI 的"原型三步法"工作区：
//
//	fixture 驱动 → 变体并排 → 选优定稿
//
// 本包只做原型渲染，不接真实下载：所有数据来自 fixture（假任务混排），
// 供肉眼对比多个布局变体后，把胜出方案移植进 tui 包正式代码。
// 正式包 tui 的渲染与热区不受本包影响。
package proto

// PTask 原型任务（fixture 数据源）。字段对齐表现力清单的"协议差异化"：
// HTTP 显速度/剩余，BT 显连接数/做种，磁力显解析状态。
type PTask struct {
	Name  string  // 文件名（可含 CJK，验证截断与宽度度量）
	State string  // running / paused / done / failed / queued
	Proto string  // http / bt / magnet
	Size  int64   // 0=未知大小（流式）
	Done  int64
	Speed float64 // B/s
	ETA   int64   // 剩余秒数（HTTP 用）
	Peers int     // BT 连接数
	Seeds int     // BT 做种数
	Meta  string  // 磁力解析状态
	Err   string  // 错误信息（fixture 故意含 \n，验证单行化）
}

// Fixture 原型任务的"同一场景"：10 个任务混排，
// 覆盖全部协议 × 全部状态 × 边界（未知大小 / CJK / 多行错误 / 全绿完成）。
func Fixture() []PTask {
	return []PTask{
		{Name: "ubuntu-24.04-desktop-amd64.iso", State: "running", Proto: "http",
			Size: 2 << 30, Done: 58 * (2 << 30) / 100, Speed: 3.4 * (1 << 20), ETA: 250},
		{Name: "debian-13.1.0-amd64-netinst.iso", State: "paused", Proto: "http",
			Size: 650 << 20, Done: 43 * (650 << 20) / 100},
		{Name: "archlinux-2026.09.01-x86_64.iso", State: "running", Proto: "bt",
			Size: 1 << 30, Done: 12 * (1 << 30) / 100, Speed: 2.1 * (1 << 20), Peers: 45, Seeds: 8},
		{Name: "magnet:?xt=urn:btih:3f5a…官方蓝光原盘合集", State: "running", Proto: "magnet",
			Meta: "解析中… 等待 DHT 节点"},
		{Name: "broken-link-archive.tar.gz", State: "failed", Proto: "http",
			Err: "探测资源失败:\nconnection refused (127.0.0.1:8080)"},
		{Name: "release-notes-2026Q3.txt", State: "done", Proto: "http",
			Size: 12 << 10, Done: 12 << 10},
		{Name: "kde-live-x86_64.torrent", State: "done", Proto: "bt",
			Size: 4 << 30, Done: 4 << 30, Seeds: 2, Peers: 0},
		{Name: "《Linux 内核设计与实现》第三版扫描版.pdf", State: "running", Proto: "http",
			Size: 88 << 20, Done: 3 * (88 << 20) / 100, Speed: 512 << 10, ETA: 600},
		{Name: "live-stream-unknown-size.flv", State: "queued", Proto: "http"},
		{Name: "magnet:?xt=urn:btih:9c2e…纪录片合集", State: "done", Proto: "magnet",
			Size: 12 << 30, Done: 12 << 30, Meta: "解析完成 · 12 文件"},
	}
}
