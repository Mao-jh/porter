// clean.go 下载目录清理：广告/垃圾文件移入 <dir>/.trash（只移动不删除，
// 用户确认后可手动清空）。文件级"去广告"——播放器内嵌广告不在下载器职责内，
// 这里清理的是下载附带的 .url/.lnk 快捷方式、广告说明页与断点残留。
package media

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// junkExts 直接判定为垃圾的扩展名（断点残留/快捷方式/临时文件）。
var junkExts = map[string]bool{
	".crdownload": true, ".part": true, ".tmp": true, ".download": true,
	".aria2": true, ".url": true, ".lnk": true, ".nfo": true,
}

// adNames 广告文件命名关键词（匹配文件名或扩展名组合）。
var adNames = []string{"ad", "ads", "advert", "advertising", "promo", "promotion",
	"sponsor", "sponsored", "banner", "推广", "广告", "宣传"}

// CleanResult 清理结果。
type CleanResult struct {
	Moved []string // 已移入 .trash 的文件
	Total int
}

// CleanConfig 清理配置。
type CleanConfig struct {
	DryRun bool // 仅打印不移入
}

// Clean 扫描 dir 顶层文件，将垃圾/广告文件移入 dir/.trash。
// 规则：
//  1. 扩展名命中 junkExts（断点残留/快捷方式/临时文件）；
//  2. 广告命名：文件名含 ad 类关键词且扩展名是 .txt/.html/.htm（下载附带的广告说明页）；
//  3. 与媒体文件同名的 .txt/.html/.htm（"xx.mp4.txt" 广告说明页）。
func Clean(dir string, cfg CleanConfig) (*CleanResult, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("clean: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("clean: %s 不是目录", abs)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var junk []string
	names := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			names[e.Name()] = true
		}
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if junkExts[ext] {
			junk = append(junk, name)
			continue
		}
		lower := strings.ToLower(name)
		if (ext == ".txt" || ext == ".html" || ext == ".htm") && isAdName(lower) {
			junk = append(junk, name)
			continue
		}
		// 与媒体文件同名的说明页：base.ext 对应 base 是媒体
		if ext == ".txt" || ext == ".html" || ext == ".htm" {
			base := strings.TrimSuffix(name, filepath.Ext(name))
			if names[base] && CategoryOf(base) != "other" {
				junk = append(junk, name)
			}
		}
	}
	sort.Strings(junk)
	res := &CleanResult{Total: len(junk)}
	trashDir := filepath.Join(abs, ".trash")
	for _, name := range junk {
		src := filepath.Join(abs, name)
		dst := filepath.Join(trashDir, name)
		res.Moved = append(res.Moved, src)
		if !cfg.DryRun {
			_ = os.MkdirAll(trashDir, 0o755)
			if err := os.Rename(src, uniqueDest(dst)); err != nil {
				return nil, fmt.Errorf("clean: 移动 %s 失败: %w", name, err)
			}
		}
	}
	return res, nil
}

// isAdName 判定广告命名（文件名主体含关键词）。
func isAdName(lower string) bool {
	base := strings.TrimSuffix(lower, filepath.Ext(lower))
	for _, k := range adNames {
		if strings.Contains(base, k) {
			return true
		}
	}
	return false
}
