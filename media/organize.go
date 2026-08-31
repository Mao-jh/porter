// organize.go 下载目录组织管理：按媒体类型归类、同名防冲突、哈希去重。
// 安全语义：只移动不删除；移动目标均在源目录内（<dir>/<类别>/）；
// 全部操作可 -dry-run 预览。
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Category 分类规则（扩展名 → 类别目录名）。
var CategoryExts = map[string]string{
	"mp4": "video", "mkv": "video", "avi": "video", "mov": "video",
	"wmv": "video", "flv": "video", "webm": "video", "ts": "video",
	"m4v": "video", "rmvb": "video", "mpg": "video", "mpeg": "video",

	"mp3": "audio", "flac": "audio", "wav": "audio", "aac": "audio",
	"ogg": "audio", "m4a": "audio", "wma": "audio", "ape": "audio",
	"opus": "audio",

	"jpg": "image", "jpeg": "image", "png": "image", "gif": "image",
	"bmp": "image", "webp": "image", "heic": "image", "svg": "image",
	"tiff": "image", "ico": "image",

	"zip": "archive", "rar": "archive", "7z": "archive", "tar": "archive",
	"gz": "archive", "bz2": "archive", "xz": "archive",

	"pdf": "docs", "doc": "docs", "docx": "docs", "xls": "docs",
	"xlsx": "docs", "ppt": "docs", "pptx": "docs", "txt": "docs",
	"md": "docs", "epub": "docs",
}

// CategoryOf 返回文件类别名（unknown 兜底）。
func CategoryOf(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if c, ok := CategoryExts[ext]; ok {
		return c
	}
	return "other"
}

// MovePlan 一条移动计划。
type MovePlan struct {
	Src  string
	Dst  string
	Kind string // move / duplicate / skip
	Why  string
}

// OrganizeConfig 整理配置。
type OrganizeConfig struct {
	DryRun bool // 仅打印计划不执行
	Dedupe bool // 按 sha256 去重（重复移入 .dupes/）
	Recurse bool // 递归子目录（默认仅顶层）
}

// Organize 将 dir 下文件按类别移动到子目录，返回计划（执行与否取决于 DryRun）。
// 同名冲突自动追加 " (N)"；-dedupe 时同内容保留首个，其余移入 <dir>/.dupes/。
func Organize(dir string, cfg OrganizeConfig) ([]MovePlan, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("organize: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("organize: %s 不是目录", abs)
	}
	var plans []MovePlan
	var files []string
	if cfg.Recurse {
		err = filepath.Walk(abs, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if fi.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(abs, p)
			if !strings.HasPrefix(rel, ".") {
				files = append(files, p)
			}
			return nil
		})
	} else {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			files = append(files, filepath.Join(abs, e.Name()))
		}
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	// 目标目录去重记账：类别 → 已用文件名
	taken := map[string]map[string]bool{}
	seenHash := map[string]string{} // sha256 → 首个保留文件

	for _, f := range files {
		rel, _ := filepath.Rel(abs, f)
		// 非递归模式：仅处理顶层文件；递归模式：跳过已在分类/垃圾目录内的文件
		if !cfg.Recurse {
			if strings.Contains(rel, string(filepath.Separator)) {
				continue
			}
		} else if isManagedDir(rel) {
			continue
		}
		cat := CategoryOf(f)
		destDir := filepath.Join(abs, cat)
		name := filepath.Base(f)
		if cfg.Dedupe {
			sum, err := fileSHA256(f)
			if err != nil {
				plans = append(plans, MovePlan{Src: f, Kind: "skip", Why: "读哈希失败: " + err.Error()})
				continue
			}
			if keep, ok := seenHash[sum]; ok {
				dupeDir := filepath.Join(abs, ".dupes")
				plans = append(plans, MovePlan{Src: f, Dst: filepath.Join(dupeDir, name), Kind: "duplicate",
					Why: fmt.Sprintf("与 %s 内容相同", filepath.Base(keep))})
				if !cfg.DryRun {
					_ = os.MkdirAll(dupeDir, 0o755)
					_ = os.Rename(f, uniqueDest(filepath.Join(dupeDir, name)))
				}
				continue
			}
			seenHash[sum] = f
		}
		dst := filepath.Join(destDir, uniqueName(name, taken[cat]))
		if taken[cat] == nil {
			taken[cat] = map[string]bool{}
		}
		taken[cat][filepath.Base(dst)] = true
		plans = append(plans, MovePlan{Src: f, Dst: dst, Kind: "move",
			Why: "按类型归类 " + cat})
		if !cfg.DryRun {
			_ = os.MkdirAll(destDir, 0o755)
			if err := os.Rename(f, dst); err != nil {
				plans = append(plans, MovePlan{Src: f, Dst: dst, Kind: "skip", Why: err.Error()})
			}
		}
	}
	return plans, nil
}

// isManagedDir 判断相对路径是否落在已管理的子目录（分类/.dupes/.trash）内。
func isManagedDir(rel string) bool {
	first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	switch first {
	case "video", "audio", "image", "archive", "docs", "other", ".dupes", ".trash":
		return true
	}
	return false
}

// uniqueName 冲突追加 " (N)" 后缀。
func uniqueName(name string, taken map[string]bool) string {
	if taken == nil || !taken[name] {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !taken[cand] {
			return cand
		}
	}
}

// uniqueDest 目标存在时追加序号（移动去重文件用）。
func uniqueDest(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 64<<10)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
