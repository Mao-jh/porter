// Package media 实现「下载后处理」：
//   - info：纯 Go 媒体容器头解析（零第三方依赖）→ 时长/分辨率/编码预览
//   - transcode：调用系统 ffmpeg 转码（无 ffmpeg 时明确报错，诚实降级）
//   - organize：按媒体类型归类整理 + 哈希去重（仅移动，不删除）
//   - clean：广告/垃圾文件移入 .trash（仅移动，不删除）
//
// 设计原则：后处理一律「不删除、只移动」；全部操作可 -dry-run 预览。
package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// MediaInfo 媒体信息（预览用）。
type MediaInfo struct {
	Kind       string // mp4 / mkv / mp3 / flac / wav / jpeg / png / gif / unknown
	Duration   time.Duration
	Width, Height int
	Codec      string
	SampleRate int
	Channels   int
	Title      string
	Artist     string
	Size       int64
}

// String 输出单行信息预览。
func (m *MediaInfo) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "kind=%s size=%d", m.Kind, m.Size)
	if m.Duration > 0 {
		fmt.Fprintf(&b, " duration=%s", m.Duration.Round(time.Second))
	}
	if m.Width > 0 && m.Height > 0 {
		fmt.Fprintf(&b, " resolution=%dx%d", m.Width, m.Height)
	}
	if m.Codec != "" {
		fmt.Fprintf(&b, " codec=%s", m.Codec)
	}
	if m.SampleRate > 0 {
		fmt.Fprintf(&b, " samplerate=%d", m.SampleRate)
	}
	if m.Channels > 0 {
		fmt.Fprintf(&b, " channels=%d", m.Channels)
	}
	if m.Title != "" {
		fmt.Fprintf(&b, " title=%q", m.Title)
	}
	if m.Artist != "" {
		fmt.Fprintf(&b, " artist=%q", m.Artist)
	}
	return b.String()
}

// Probe 解析文件媒体信息。失败返回带文件类型提示的错误（未知类型不算错误——
// 返回 Kind=unknown 的零值信息）。
func Probe(path string) (*MediaInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	m := &MediaInfo{Size: info.Size()}
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(f, hdr); err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(hdr, []byte("\xFF\xD8")):
		m.Kind = "jpeg"
		m.probeJPEG(f)
	case bytes.HasPrefix(hdr, []byte("\x89PNG\r\n\x1a\n")):
		m.Kind = "png"
		m.probePNG(f)
	case bytes.HasPrefix(hdr, []byte("GIF8")):
		m.Kind = "gif"
		m.probeGIF(hdr)
	case bytes.HasPrefix(hdr, []byte("ID3")) || isMPEGFrame(hdr):
		m.Kind = "mp3"
		m.probeMP3(f, hdr)
	case bytes.HasPrefix(hdr, []byte("fLaC")):
		m.Kind = "flac"
		m.probeFLAC(f)
	case bytes.HasPrefix(hdr, []byte("RIFF")) && string(hdr[8:12]) == "WAVE":
		m.Kind = "wav"
		m.probeWAV(f)
	case string(hdr[4:8]) == "ftyp":
		m.Kind = "mp4"
		m.probeMP4(f, hdr)
	case bytes.HasPrefix(hdr, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		m.Kind = "mkv"
		m.probeMKV(f)
	default:
		m.Kind = "unknown"
	}
	return m, nil
}

// --- MP4 / MOV -----------------------------------------------------

// probeMP4 遍历 box 结构（随机访问，mdat 跳过）提取 mvhd 时长与 stsd 编码。
func (m *MediaInfo) probeMP4(f *os.File, hdr []byte) {
	// 从文件尾扫描更可靠（moov 常在 mdat 之后）；先试文件头 box，找不到再试尾部。
	if !walkMP4Boxes(f, 0, m) {
		// 尾部兜底：从最后 64MiB 范围内找 moov
		if size := m.Size; size > 64<<20 {
			if !walkMP4Boxes(f, size-64<<20, m) {
				return
			}
		}
	}
}

// walkMP4Boxes 从 offset 起遍历 box，返回是否找到 moov（已填充时长/编码）。
func walkMP4Boxes(f *os.File, start int64, m *MediaInfo) bool {
	off := start
	header := make([]byte, 16)
	for {
		if _, err := f.ReadAt(header, off); err != nil {
			return m.Duration > 0 || m.Codec != ""
		}
		boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := string(header[4:8])
		hdrLen := int64(8)
		if boxSize == 1 {
			boxSize = int64(binary.BigEndian.Uint64(header[8:16]))
			hdrLen = 16
		} else if boxSize == 0 {
			boxSize = m.Size - off
		}
		if boxSize < hdrLen || off+boxSize > m.Size {
			return m.Duration > 0 || m.Codec != ""
		}
		switch boxType {
		case "moov":
			m.parseMoov(f, off+hdrLen, off+boxSize)
			return true
		case "mdat":
			// 跳过媒体数据
		}
		off += boxSize
		if off >= m.Size {
			return m.Duration > 0 || m.Codec != ""
		}
	}
}

func (m *MediaInfo) parseMoov(f *os.File, start, end int64) {
	off := start
	header := make([]byte, 16)
	for off+8 <= end {
		if _, err := f.ReadAt(header, off); err != nil {
			return
		}
		boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := string(header[4:8])
		if boxSize == 1 {
			boxSize = int64(binary.BigEndian.Uint64(header[8:16]))
		} else if boxSize == 0 {
			boxSize = end - off
		}
		if boxSize < 8 || off+boxSize > end {
			return
		}
		switch boxType {
		case "mvhd":
			m.parseMVHD(f, off)
		case "trak":
			m.parseTrak(f, off+8, off+boxSize)
		}
		off += boxSize
	}
}

func (m *MediaInfo) parseMVHD(f *os.File, off int64) {
	buf := make([]byte, 100)
	if _, err := f.ReadAt(buf, off); err != nil {
		return
	}
	version := buf[8]
	var timescale, duration uint32
	if version == 1 {
		timescale = binary.BigEndian.Uint32(buf[28:32])
		duration = uint32(binary.BigEndian.Uint64(buf[32:40]))
	} else {
		timescale = binary.BigEndian.Uint32(buf[20:24])
		duration = binary.BigEndian.Uint32(buf[24:28])
	}
	if timescale > 0 {
		m.Duration = time.Duration(float64(duration) / float64(timescale) * float64(time.Second))
	}
}

func (m *MediaInfo) parseTrak(f *os.File, start, end int64) {
	off := start
	header := make([]byte, 16)
	for off+8 <= end {
		if _, err := f.ReadAt(header, off); err != nil {
			return
		}
		boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := string(header[4:8])
		if boxSize == 1 {
			boxSize = int64(binary.BigEndian.Uint64(header[8:16]))
		} else if boxSize == 0 {
			boxSize = end - off
		}
		if boxSize < 8 || off+boxSize > end {
			return
		}
		switch boxType {
		case "tkhd":
			m.parseTKHD(f, off)
		case "mdia":
			m.parseMdia(f, off+8, off+boxSize)
		}
		off += boxSize
	}
}

func (m *MediaInfo) parseTKHD(f *os.File, off int64) {
	buf := make([]byte, 104)
	if _, err := f.ReadAt(buf, off); err != nil {
		return
	}
	version := buf[8]
	// tkhd payload: v0 width/height 在 [76:84]，v1 在 [88:96]（box 头 8 字节前导）
	wo := 76
	if version == 1 {
		wo = 88
	}
	w := int(binary.BigEndian.Uint32(buf[8+wo : 8+wo+4]))
	h := int(binary.BigEndian.Uint32(buf[8+wo+4 : 8+wo+8]))
	if w>>16 > 0 {
		m.Width, m.Height = w>>16, h>>16
	}
}

func (m *MediaInfo) parseMdia(f *os.File, start, end int64) {
	off := start
	header := make([]byte, 16)
	for off+8 <= end {
		if _, err := f.ReadAt(header, off); err != nil {
			return
		}
		boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := string(header[4:8])
		if boxSize == 1 {
			boxSize = int64(binary.BigEndian.Uint64(header[8:16]))
		} else if boxSize == 0 {
			boxSize = end - off
		}
		if boxSize < 8 || off+boxSize > end {
			return
		}
		if boxType == "minf" {
			m.parseMinf(f, off+8, off+boxSize)
			return
		}
		off += boxSize
	}
}

func (m *MediaInfo) parseMinf(f *os.File, start, end int64) {
	off := start
	header := make([]byte, 16)
	for off+8 <= end {
		if _, err := f.ReadAt(header, off); err != nil {
			return
		}
		boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := string(header[4:8])
		if boxSize == 1 {
			boxSize = int64(binary.BigEndian.Uint64(header[8:16]))
		} else if boxSize == 0 {
			boxSize = end - off
		}
		if boxSize < 8 || off+boxSize > end {
			return
		}
		if boxType == "stbl" {
			m.parseStbl(f, off+8, off+boxSize)
			return
		}
		off += boxSize
	}
}

func (m *MediaInfo) parseStbl(f *os.File, start, end int64) {
	off := start
	header := make([]byte, 16)
	for off+8 <= end {
		if _, err := f.ReadAt(header, off); err != nil {
			return
		}
		boxSize := int64(binary.BigEndian.Uint32(header[0:4]))
		boxType := string(header[4:8])
		if boxSize == 1 {
			boxSize = int64(binary.BigEndian.Uint64(header[8:16]))
		} else if boxSize == 0 {
			boxSize = end - off
		}
		if boxSize < 8 || off+boxSize > end {
			return
		}
		if boxType == "stsd" {
			// stsd payload: version/flags(4) + entry_count(4) + 首条 entry(size(4)+format(4))
			buf := make([]byte, 16)
			if _, err := f.ReadAt(buf, off+8); err != nil {
				return
			}
			entrySize := int64(binary.BigEndian.Uint32(buf[8:12]))
			if entrySize >= 8 {
				codec := strings.TrimRight(string(buf[12:16]), "\x00 ")
				if codec != "" {
					if m.Codec == "" {
						m.Codec = codec
					} else {
						m.Codec += "+" + codec
					}
				}
			}
			return
		}
		off += boxSize
	}
}

// --- MKV / WebM ----------------------------------------------------

// probeMKV 浅解析 EBML：Segment → Info（duration/timecodeScale）/ Tracks（codec/宽高）。
// 仅读取文件头 4MiB（Info/Tracks 通常在文件开头）。
func (m *MediaInfo) probeMKV(f *os.File) {
	const readLimit = 4 << 20
	buf := make([]byte, readLimit)
	n, err := f.ReadAt(buf, 0)
	if err != nil && n <= 0 {
		return
	}
	data := buf[:n]
	// 跳过 EBML 头
	_, _, off, ok := ebmlElement(data, 0)
	if !ok {
		return
	}
	// Segment
	id, size, off2, ok := ebmlElement(data, int(off))
	if !ok || id != 0x18538067 {
		return
	}
	end := off2
	if size != unknownSize && off2+size < int64(len(data)) {
		end = off2 + size
	}
	ts := int64(1_000_000) // 默认 1ms
	var dur float64
	for off2+4 <= end {
		elID, elSize, next, ok := ebmlElement(data, int(off2))
		if !ok || next > end {
			break
		}
		switch elID {
		case 0x1549A966: // Info
			sub := data[next : next+min64(elSize, int64(len(data))-next)]
			ts, dur = parseEBMLInfo(sub, ts)
		case 0x1654AE6B: // Tracks
			parseEBMLTracks(data[next:next+min64(elSize, int64(len(data))-next)], m)
		}
		off2 = next
	}
	if dur > 0 {
		m.Duration = time.Duration(dur * float64(ts) / 1e9 * float64(time.Second))
	}
}

const unknownSize = int64(-1)

// ebmlElement 解析 EBML 元素头：返回 (ID, 数据长度, 数据起始, 是否有效)。
func ebmlElement(d []byte, off int) (int64, int64, int64, bool) {
	// ID：VINT（首字节最高位位置决定长度 1-4）
	if off >= len(d) {
		return 0, 0, 0, false
	}
	first := d[off]
	idLen := 1
	for mask := byte(0x80); mask > 0 && first&mask == 0; mask >>= 1 {
		idLen++
	}
	if idLen > 4 || off+idLen > len(d) {
		return 0, 0, 0, false
	}
	var id int64
	for i := 0; i < idLen; i++ {
		id = id<<8 | int64(d[off+i])
	}
	// Size：VINT（数据长度，全 1 表示未知）
	pos := off + idLen
	if pos >= len(d) {
		return 0, 0, 0, false
	}
	sizeLen := 1
	for mask := byte(0x80); mask > 0 && d[pos]&mask == 0; mask >>= 1 {
		sizeLen++
	}
	if sizeLen > 8 || pos+sizeLen > len(d) {
		return 0, 0, 0, false
	}
	var size int64
	allOnes := true
	for i := 0; i < sizeLen; i++ {
		b := d[pos+i]
		if i == 0 {
			b &^= byte(0x80 >> (sizeLen - 1)) // 清除长度标记位
		}
		if b != 0xFF {
			allOnes = false
		}
		size = size<<8 | int64(b)
	}
	if allOnes {
		size = unknownSize
	}
	return id, size, int64(pos + sizeLen), true
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// parseEBMLInfo 解析 Info 元素：timecodeScale（uint）与 duration（float）。
func parseEBMLInfo(d []byte, defTS int64) (int64, float64) {
	ts := defTS
	var dur float64
	off := 0
	for off+2 <= len(d) {
		id, size, next, ok := ebmlElement(d, off)
		if !ok || next+size > int64(len(d)) {
			break
		}
		val := d[next : next+size]
		switch id {
		case 0x2AD7B1: // TimecodeScale
			ts = readUint(val, defTS)
		case 0x4489: // Duration（float）
			if len(val) == 4 {
				dur = float64(binary.BigEndian.Uint32(val))
			} else if len(val) == 8 {
				bits := binary.BigEndian.Uint64(val)
				dur = float64(int64(bits)) // 简化：按整数读（常见值）
			}
		}
		off = int(next + size)
	}
	return ts, dur
}

// parseEBMLTracks 解析 Tracks 元素：取首个视频轨宽高与编码 ID。
func parseEBMLTracks(d []byte, m *MediaInfo) {
	off := 0
	for off+2 <= len(d) {
		id, size, next, ok := ebmlElement(d, off)
		if !ok || next+size > int64(len(d)) {
			break
		}
		if id == 0xAE { // TrackEntry
			parseEBMLTrackEntry(d[next:next+size], m)
		}
		off = int(next + size)
	}
}

func parseEBMLTrackEntry(d []byte, m *MediaInfo) {
	off := 0
	for off+2 <= len(d) {
		id, size, next, ok := ebmlElement(d, off)
		if !ok || next+size > int64(len(d)) {
			break
		}
		val := d[next : next+size]
		switch id {
		case 0x86: // CodecID
			if m.Codec == "" {
				m.Codec = string(val)
			}
		case 0xE0: // Video
			parseEBMLVideo(val, m)
		case 0xE1: // Audio
			if len(val) >= 4 {
				m.SampleRate = int(binary.BigEndian.Uint32(val) >> 3) // float32 → 近似
			}
		}
		off = int(next + size)
	}
}

func parseEBMLVideo(d []byte, m *MediaInfo) {
	off := 0
	for off+2 <= len(d) {
		id, size, next, ok := ebmlElement(d, off)
		if !ok || next+size > int64(len(d)) {
			break
		}
		val := d[next : next+size]
		switch id {
		case 0xB0: // PixelWidth
			m.Width = int(readUint(val, 0))
		case 0xBA: // PixelHeight
			m.Height = int(readUint(val, 0))
		}
		off = int(next + size)
	}
}

func readUint(b []byte, def int64) int64 {
	if len(b) == 0 || len(b) > 8 {
		return def
	}
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return int64(v)
}

// --- MP3 ------------------------------------------------------------

// isMPEGFrame 判断前 4 字节是否 MPEG 音频帧同步。
func isMPEGFrame(hdr []byte) bool {
	return len(hdr) >= 2 && hdr[0] == 0xFF && hdr[1]&0xE0 == 0xE0
}

func (m *MediaInfo) probeMP3(f *os.File, hdr []byte) {
	// ID3v2 标签头：ID3 + 版本(2) + flags(1) + syncsafe size(4)
	id3Size := int64(0)
	if bytes.HasPrefix(hdr, []byte("ID3")) {
		if len(hdr) >= 10 {
			id3Size = syncsafe(hdr[6:10])
			id3Size += 10
		}
		// 读标签取标题/艺术家（ID3v2.3/2.4 文本帧）
		m.readID3v2(f, id3Size)
	}
	// 帧头（ID3 之后）：版本/层/比特率/采样率
	fh := make([]byte, 4)
	if _, err := f.ReadAt(fh, id3Size); err != nil || !isMPEGFrame(fh) {
		return
	}
	bitrate, sampleRate := mp3FrameInfo(fh)
	if bitrate > 0 {
		audioBytes := m.Size - id3Size
		m.Duration = time.Duration(float64(audioBytes) * 8 / float64(bitrate) * float64(time.Second))
	}
	if sampleRate > 0 {
		m.SampleRate = sampleRate
	}
}

func (m *MediaInfo) readID3v2(f *os.File, tagSize int64) {
	buf := make([]byte, tagSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return
	}
	// 跳过 ID3 头 10 字节；每帧：ID(4) + size(4) + flags(2)
	pos := 10
	for pos+10 <= len(buf) {
		frameID := string(buf[pos : pos+4])
		if frameID[0] == 0 {
			break
		}
		size := int(binary.BigEndian.Uint32(buf[pos+4 : pos+8]))
		if pos+10+size > len(buf) {
			break
		}
		payload := buf[pos+10 : pos+10+size]
		switch frameID {
		case "TIT2", "TIT1":
			if m.Title == "" {
				m.Title = decodeTextFrame(payload)
			}
		case "TPE1":
			if m.Artist == "" {
				m.Artist = decodeTextFrame(payload)
			}
		}
		pos += 10 + size
	}
}

// decodeTextFrame 解码 ID3 文本帧（编码字节 0=Latin1, 1=UTF-16, 2=UTF-16BE, 3=UTF-8）。
func decodeTextFrame(p []byte) string {
	if len(p) == 0 {
		return ""
	}
	body := p[1:]
	switch p[0] {
	case 1, 2: // UTF-16（可能带 BOM）
		if len(body) >= 2 {
			return strings.TrimRight(string(utf16BytesToStr(body)), "\x00")
		}
	case 3: // UTF-8
		return strings.TrimRight(string(body), "\x00")
	}
	return strings.TrimRight(string(body), "\x00")
}

// utf16BytesToStr 简易 UTF-16LE/BE → string（仅用于标签显示）。
func utf16BytesToStr(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	le := true
	if b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	} else if b[0] == 0xFE && b[1] == 0xFF {
		le = false
		b = b[2:]
	}
	var sb strings.Builder
	for i := 0; i+1 < len(b); i += 2 {
		var u uint16
		if le {
			u = binary.LittleEndian.Uint16(b[i:])
		} else {
			u = binary.BigEndian.Uint16(b[i:])
		}
		if u != 0 {
			sb.WriteRune(rune(u))
		}
	}
	return sb.String()
}

// mp3FrameInfo 从 MPEG 帧头提取比特率（kbps）与采样率（Hz）。
func mp3FrameInfo(fh []byte) (int, int) {
	version := (fh[1] >> 3) & 0x3  // 0=2.5, 2=2, 3=1
	layer := (fh[1] >> 1) & 0x3    // 1=III, 2=II, 3=I
	brIdx := (fh[2] >> 4) & 0xF
	srIdx := (fh[2] >> 2) & 0x3
	brTable := [5][16]int{
		{}, // 保留
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}, // V1 L1
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},   // V1 L2
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},   // V1 L3
	}
	srTable := []int{44100, 48000, 32000, 0}
	var br int
	if version == 3 && layer >= 1 && layer <= 3 && brIdx > 0 && brIdx < 15 {
		br = brTable[layer][brIdx] * 1000
	}
	var sr int
	if srIdx < 3 {
		sr = srTable[srIdx]
		if version == 2 { // V2: 采样率减半
			sr /= 2
		} else if version == 0 { // V2.5: 再减半
			sr /= 4
		}
	}
	return br, sr
}

func syncsafe(b []byte) int64 {
	var v int64
	for _, x := range b {
		v = v<<7 | int64(x&0x7F)
	}
	return v
}

// --- FLAC / WAV / 图像 ----------------------------------------------

func (m *MediaInfo) probeFLAC(f *os.File) {
	buf := make([]byte, 4+5+34) // fLaC(4) + 块头(1+4) + streaminfo(34)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return
	}
	if buf[4] != 0x80 { // METADATA_BLOCK_STREAMINFO（last-flag 位）
		return
	}
	si := buf[4+5:]
	if len(si) < 18 {
		return
	}
	// streaminfo: 采样率 20bit、声道 3bit、位深 5bit、总采样 36bit
	sampleRate := int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
	channels := int(si[12]>>1&0x7) + 1
	total := int64(si[13]&0x0F)<<32 | int64(si[14])<<24 | int64(si[15])<<16 | int64(si[16])<<8 | int64(si[17])
	m.SampleRate, m.Channels = sampleRate, channels
	if sampleRate > 0 && total > 0 {
		m.Duration = time.Duration(float64(total) / float64(sampleRate) * float64(time.Second))
	}
}

func (m *MediaInfo) probeWAV(f *os.File) {
	buf := make([]byte, 12+40)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return
	}
	// 找 fmt 与 data chunk
	off := 12
	for off+8 <= len(buf) {
		chunk := string(buf[off : off+4])
		size := int(binary.LittleEndian.Uint32(buf[off+4 : off+8]))
		switch chunk {
		case "fmt ":
			if size >= 16 && off+8+16 <= len(buf) {
				f := buf[off+8:]
				audioFormat := binary.LittleEndian.Uint16(f[0:2])
				_ = audioFormat
				m.Channels = int(binary.LittleEndian.Uint16(f[2:4]))
				m.SampleRate = int(binary.LittleEndian.Uint32(f[4:8]))
			}
		case "data":
			m.Duration = wavDuration(size, m.SampleRate, m.Channels)
		}
		off += 8 + size
		if chunk == "data" || size == 0 {
			break
		}
	}
}

func wavDuration(dataSize, rate, channels int) time.Duration {
	if rate <= 0 || channels <= 0 {
		return 0
	}
	bytesPerSec := rate * channels * 2 // 16-bit 假定（PCM 主流）
	if bytesPerSec <= 0 {
		return 0
	}
	return time.Duration(float64(dataSize) / float64(bytesPerSec) * float64(time.Second))
}

func (m *MediaInfo) probeJPEG(f *os.File) {
	// 扫描段到 SOF0/1/2（FFC0/FFC1/FFC2）
	buf := make([]byte, 64<<10)
	n, err := f.ReadAt(buf, 0)
	if err != nil && n <= 0 {
		return
	}
	d := buf[:n]
	pos := 2 // 跳过 SOI
	for pos+4 <= len(d) {
		if d[pos] != 0xFF {
			pos++
			continue
		}
		marker := d[pos+1]
		if marker == 0xC0 || marker == 0xC1 || marker == 0xC2 {
			if pos+9 <= len(d) {
				m.Height = int(binary.BigEndian.Uint16(d[pos+5 : pos+7]))
				m.Width = int(binary.BigEndian.Uint16(d[pos+7 : pos+9]))
			}
			return
		}
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			pos += 2
			continue
		}
		if pos+4 > len(d) {
			return
		}
		segLen := int(binary.BigEndian.Uint16(d[pos+2 : pos+4]))
		pos += 2 + segLen
	}
}

func (m *MediaInfo) probePNG(f *os.File) {
	buf := make([]byte, 8+4+4+13)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return
	}
	if string(buf[12:16]) == "IHDR" {
		m.Width = int(binary.BigEndian.Uint32(buf[16:20]))
		m.Height = int(binary.BigEndian.Uint32(buf[20:24]))
	}
}

func (m *MediaInfo) probeGIF(hdr []byte) {
	if len(hdr) >= 10 {
		m.Width = int(binary.LittleEndian.Uint16(hdr[6:8]))
		m.Height = int(binary.LittleEndian.Uint16(hdr[8:10]))
	}
}
