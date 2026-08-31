// media 包单元测试：容器解析（最小合法文件构造）+ organize/clean 行为。
package media

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func box(t string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], t)
	copy(b[8:], payload)
	return b
}

// --- PNG ---
func TestProbePNG(t *testing.T) {
	ihdr := make([]byte, 4+4+13)
	binary.BigEndian.PutUint32(ihdr[0:4], 13)
	copy(ihdr[4:8], "IHDR")
	binary.BigEndian.PutUint32(ihdr[8:12], 1920)
	binary.BigEndian.PutUint32(ihdr[12:16], 1080)
	data := append([]byte("\x89PNG\r\n\x1a\n"), ihdr...)
	m, err := Probe(writeFile(t, "pic.png", data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "png" || m.Width != 1920 || m.Height != 1080 {
		t.Errorf("PNG 解析错误: %+v", m)
	}
}

// --- JPEG ---
func TestProbeJPEG(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	b.Write([]byte{0xFF, 0xC0}) // SOF0
	segLen := []byte{0, 17}
	b.Write(segLen)
	b.Write([]byte{8})                    // 精度
	binary.Write(&b, binary.BigEndian, uint16(720)) // 高
	binary.Write(&b, binary.BigEndian, uint16(1280)) // 宽
	b.Write(make([]byte, 11))             // 组件
	b.Write([]byte{0xFF, 0xD9})           // EOI
	m, err := Probe(writeFile(t, "pic.jpg", b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "jpeg" || m.Width != 1280 || m.Height != 720 {
		t.Errorf("JPEG 解析错误: %+v", m)
	}
}

// --- WAV ---
func TestProbeWAV(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(2))      // 声道
	binary.Write(&b, binary.LittleEndian, uint32(44100))  // 采样率
	binary.Write(&b, binary.LittleEndian, uint32(44100*4)) // 字节率
	binary.Write(&b, binary.LittleEndian, uint16(4))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	dataSize := uint32(44100 * 4) // 1 秒
	binary.Write(&b, binary.LittleEndian, dataSize)
	b.Write(make([]byte, dataSize))
	m, err := Probe(writeFile(t, "a.wav", b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "wav" || m.SampleRate != 44100 || m.Channels != 2 {
		t.Errorf("WAV 解析错误: %+v", m)
	}
	if m.Duration < time.Second || m.Duration > 2*time.Second {
		t.Errorf("WAV 时长应约 1s，got %v", m.Duration)
	}
}

// --- MP3（ID3v2.3 + CBR 128kbps 帧头） ---
func TestProbeMP3(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("ID3")
	b.Write([]byte{3, 0})             // 版本 2.3.0
	b.Write([]byte{0})                 // flags
	b.Write([]byte{0, 0, 0, 13})       // syncsafe size=13（TIT2 帧：10 头 + 3 负载）
	b.WriteString("TIT2")
	binary.Write(&b, binary.BigEndian, uint32(3))
	b.Write([]byte{0, 0})
	b.Write([]byte{3}) // UTF-8
	b.WriteString("Hi")
	// 帧头：V1 L3 128kbps 44100Hz
	b.Write([]byte{0xFF, 0xFB, 0x90, 0x00})
	b.Write(make([]byte, 4096)) // 近似 128kbps 下的 0.25s
	m, err := Probe(writeFile(t, "song.mp3", b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "mp3" {
		t.Fatalf("MP3 类型错误: %+v", m)
	}
	if m.Title != "Hi" {
		t.Errorf("标题解析错误: %q", m.Title)
	}
	if m.SampleRate != 44100 {
		t.Errorf("采样率错误: %d", m.SampleRate)
	}
	if m.Duration <= 0 {
		t.Errorf("时长应为正: %v", m.Duration)
	}
}

// --- FLAC ---
func TestProbeFLAC(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("fLaC")
	b.Write([]byte{0x80})             // last block, STREAMINFO
	binary.Write(&b, binary.BigEndian, uint32(34))
	si := make([]byte, 34)
	// 采样率 48000=0xBB80（20 位：si[10]=0x0B, si[11]=0xB8, si[12] 高 4 位=0x0）
	si[10] = 0x0B
	si[11] = 0xB8
	// 声道 2 → 3bit 值 1；位深 16 → 5bit 值 15
	si[12] = 0x00 | (1 << 1) // 采样率低 4 位=0；channels-1=1
	si[13] = 0xF0            // 位深低 4 位=0xF；总采样最高 4 位=0x0
	si[16] = 0xBB            // 总采样 48000=0xBB80（36 位）
	si[17] = 0x80
	b.Write(si)
	m, err := Probe(writeFile(t, "a.flac", b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "flac" {
		t.Fatalf("FLAC 类型错误: %+v", m)
	}
	if m.SampleRate != 48000 {
		t.Errorf("采样率错误: %d", m.SampleRate)
	}
}

// --- MP4（ftyp + moov/mvhd + trak/tkhd + mdia/minf/stbl/stsd） ---
func TestProbeMP4(t *testing.T) {
	mvhd := make([]byte, 100)
	mvhd[0], mvhd[1], mvhd[2], mvhd[3] = 0, 0, 0, 0 // version 0
	binary.BigEndian.PutUint32(mvhd[12:16], 1000)     // timescale
	binary.BigEndian.PutUint32(mvhd[16:20], 9500)     // duration 9.5s
	tkhd := make([]byte, 84)
	tkhd[0] = 0
	binary.BigEndian.PutUint32(tkhd[76:80], 1920<<16) // 定点 16.16
	binary.BigEndian.PutUint32(tkhd[80:84], 1080<<16)
	stsd := make([]byte, 24)
	binary.BigEndian.PutUint32(stsd[4:8], 1)   // entry count
	binary.BigEndian.PutUint32(stsd[8:12], 16) // 首条 entry size
	copy(stsd[12:16], "avc1")                  // 编码格式
	stbl := box("stbl", box("stsd", stsd))
	minf := box("minf", stbl)
	mdia := box("mdia", minf)
	trak := box("trak", append(box("tkhd", tkhd), mdia...))
	moov := box("moov", append(box("mvhd", mvhd), trak...))
	ftyp := box("ftyp", []byte("isom"))
	data := append(ftyp, moov...)
	m, err := Probe(writeFile(t, "v.mp4", data))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "mp4" {
		t.Fatalf("MP4 类型错误: %+v", m)
	}
	if m.Width != 1920 || m.Height != 1080 {
		t.Errorf("分辨率错误: %dx%d", m.Width, m.Height)
	}
	if m.Duration < 9*time.Second || m.Duration > 10*time.Second {
		t.Errorf("时长应约 9.5s，got %v", m.Duration)
	}
	if m.Codec != "avc1" {
		t.Errorf("编码错误: %q", m.Codec)
	}
}

// --- 未知类型 ---
func TestProbeUnknown(t *testing.T) {
	m, err := Probe(writeFile(t, "x.bin", []byte("hello world this is not media")))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "unknown" {
		t.Errorf("应 unknown，got %q", m.Kind)
	}
}

// --- organize ---
func TestOrganize(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.mp4":  "video-content-a",
		"b.mkv":  "video-content-b",
		"c.mp3":  "audio-content",
		"d.jpg":  "image-content",
		"e.pdf":  "doc-content",
		"f.zip":  "archive-content",
		"g.xyz":  "unknown-content",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plans, err := Organize(dir, OrganizeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != len(files) {
		t.Fatalf("应 %d 条计划，got %d", len(files), len(plans))
	}
	// 验证移动
	for _, want := range []string{"video/a.mp4", "video/b.mkv", "audio/c.mp3",
		"image/d.jpg", "docs/e.pdf", "archive/f.zip", "other/g.xyz"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(want))); err != nil {
			t.Errorf("缺 %s: %v", want, err)
		}
	}
}

func TestOrganizeDedupe(t *testing.T) {
	dir := t.TempDir()
	content := "same-content-bytes"
	for _, n := range []string{"a.mp4", "b.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plans, err := Organize(dir, OrganizeConfig{Dedupe: true})
	if err != nil {
		t.Fatal(err)
	}
	dupes := 0
	for _, p := range plans {
		if p.Kind == "duplicate" {
			dupes++
		}
	}
	if dupes != 1 {
		t.Errorf("应 1 个重复，got %d（plans=%+v）", dupes, plans)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".dupes"))
	if len(entries) != 1 {
		t.Errorf(".dupes 应有 1 个文件，got %d", len(entries))
	}
}

func TestOrganizeDryRun(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.mp4"), []byte("x"), 0o644)
	plans, err := Organize(dir, OrganizeConfig{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Kind != "move" {
		t.Fatalf("dry-run 计划错误: %+v", plans)
	}
	// 文件不应被移动
	if _, err := os.Stat(filepath.Join(dir, "a.mp4")); err != nil {
		t.Fatal("dry-run 不应移动文件")
	}
}

// --- clean ---
func TestClean(t *testing.T) {
	dir := t.TempDir()
	junk := []string{"x.mp4.url", "movie.crdownload", "old.part", "promo.txt",
		"movie.mp4.txt", "movie.mp4", "keep.mp3"}
	for _, n := range junk {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Clean(dir, CleanConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// 预期清理：x.mp4.url / movie.crdownload / old.part / promo.txt / movie.mp4.txt
	if res.Total != 5 {
		t.Fatalf("应清理 5 个，got %d: %v", res.Total, res.Moved)
	}
	for _, n := range []string{"x.mp4.url", "movie.crdownload", "old.part", "promo.txt", "movie.mp4.txt"} {
		if _, err := os.Stat(filepath.Join(dir, ".trash", n)); err != nil {
			t.Errorf(".trash 缺 %s: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.mp4")); err != nil {
		t.Error("媒体文件不应被清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.mp3")); err != nil {
		t.Error("正常文件不应被清理")
	}
}

func TestCleanDryRun(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ad.txt"), []byte("x"), 0o644)
	res, err := Clean(dir, CleanConfig{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Fatalf("dry-run 应报 1 个，got %d", res.Total)
	}
	if _, err := os.Stat(filepath.Join(dir, "ad.txt")); err != nil {
		t.Fatal("dry-run 不应移动")
	}
}

// --- transcode（无 ffmpeg 时明确报错） ---
func TestTranscodeNoFFmpeg(t *testing.T) {
	// 不 mock exec.LookPath：只验证错误信息包含安装指引（本机可能装了 ffmpeg，
	// 装了则跳过；没装则验证报错文案）。
	ff := FindFFmpeg()
	if ff != "" {
		t.Skip("系统存在 ffmpeg，跳过无 ffmpeg 路径验证")
	}
	_, err := Transcode("x.mp4", TranscodeConfig{To: "mp3"})
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("无 ffmpeg 应报错并含指引，got %v", err)
	}
}
