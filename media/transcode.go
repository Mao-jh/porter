// transcode.go 调用系统 ffmpeg 转码（诚实降级：无 ffmpeg 时明确报错并给安装指引）。
// Go 零依赖核心不做媒体编码——转码是"外部工具编排"，由本包探测与调用。
package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FindFFmpeg 探测系统 ffmpeg：PATH 优先，其次常见安装路径。
// 返回空串表示未找到。
func FindFFmpeg() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, p := range []string{
			`C:\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Packages", "Gyan.FFmpeg*", "ffmpeg.exe"),
		} {
			if matches, _ := filepath.Glob(p); len(matches) > 0 {
				if _, err := os.Stat(matches[0]); err == nil {
					return matches[0]
				}
			}
		}
	}
	return ""
}

// TranscodeConfig 转码参数。
type TranscodeConfig struct {
	To     string // 目标扩展名（mp3 / m4a / mp4 / webm / ogg / flac / wav）
	CRF    int    // 视频质量 0-51（默认 23；仅视频输出时生效）
	AudioQ int    // 音频质量 0-9（默认 2；仅转音频时生效）
	OutDir string // 输出目录（默认与源同目录）
	Extra  []string // 附加 ffmpeg 参数（透传，如 "-ss 00:10:00 -t 60"）
	Quiet  bool   // -nostats -loglevel error
}

// Transcode 调用 ffmpeg 将 src 转码为 cfg.To 格式，返回输出文件路径。
// ffmpeg 缺失返回带安装指引的错误。
func Transcode(src string, cfg TranscodeConfig) (string, error) {
	ff := FindFFmpeg()
	if ff == "" {
		return "", fmt.Errorf("未找到系统 ffmpeg（转码依赖它）：Windows 可 `winget install Gyan.FFmpeg`，macOS `brew install ffmpeg`，Linux `apt install ffmpeg`")
	}
	if cfg.To == "" {
		return "", fmt.Errorf("transcode: 缺少目标格式（-to mp3|m4a|mp4|webm|ogg|flac|wav）")
	}
	to := strings.TrimPrefix(strings.ToLower(cfg.To), ".")
	dir := cfg.OutDir
	if dir == "" {
		dir = filepath.Dir(src)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("transcode: 创建输出目录失败: %w", err)
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	out := filepath.Join(dir, base+"."+to)
	if _, err := os.Stat(out); err == nil {
		return "", fmt.Errorf("transcode: 输出已存在 %s（先删除或换 -out 目录）", out)
	}

	args := []string{"-y"}
	// 输入
	args = append(args, "-i", src)
	// 按目标类型选编码器映射
	switch to {
	case "mp3":
		args = append(args, "-vn", "-c:a", "libmp3lame", "-q:a", fmt.Sprint(cfg.AudioQ))
	case "m4a", "aac":
		args = append(args, "-vn", "-c:a", "aac", "-b:a", "192k")
	case "flac":
		args = append(args, "-vn", "-c:a", "flac")
	case "wav":
		args = append(args, "-vn", "-c:a", "pcm_s16le")
	case "ogg":
		args = append(args, "-vn", "-c:a", "libvorbis", "-q:a", "4")
	case "mp4", "mov":
		crf := cfg.CRF
		if crf <= 0 {
			crf = 23
		}
		args = append(args, "-c:v", "libx264", "-crf", fmt.Sprint(crf), "-preset", "medium", "-c:a", "aac", "-b:a", "192k")
	case "webm":
		args = append(args, "-c:v", "libvpx-vp9", "-b:v", "0", "-crf", "35", "-c:a", "libopus")
	case "mkv":
		args = append(args, "-c", "copy")
	default:
		return "", fmt.Errorf("transcode: 不支持的目标格式 %q（支持 mp3/m4a/aac/flac/wav/ogg/mp4/mov/webm/mkv）", to)
	}
	args = append(args, cfg.Extra...)
	if cfg.Quiet {
		args = append(args, "-nostats", "-loglevel", "error")
	}
	args = append(args, out)

	cmd := exec.Command(ff, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(out) // 失败清理半成品
		return "", fmt.Errorf("transcode: ffmpeg 执行失败: %w", err)
	}
	return out, nil
}
