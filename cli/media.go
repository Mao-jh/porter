// media.go 下载后处理子命令（第 23 轮）：
//   info       媒体信息预览（纯 Go 容器解析，无外部依赖）
//   transcode  调用系统 ffmpeg 转码（无 ffmpeg 明确报错）
//   organize   按类型归类整理 + 去重（只移动不删除，-dry-run 预览）
//   scrub      广告/垃圾文件移入 .trash（只移动不删除，-dry-run 预览）
package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Mao-jh/porter/media"
)

// RunInfo 输出媒体信息预览（kind/size/duration/resolution/codec/...）。
func RunInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	if err := fs.Parse(flagFirst(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("info: 需要媒体文件路径")
	}
	m, err := media.Probe(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("info: %w", err)
	}
	if m.Kind == "unknown" {
		fmt.Fprintf(os.Stderr, "info: %s 无法识别的媒体类型（仍输出基本信息）\n", fs.Arg(0))
	}
	fmt.Println(m.String())
	return nil
}

// RunTranscode 调用系统 ffmpeg 转码。
func RunTranscode(args []string) error {
	fs := flag.NewFlagSet("transcode", flag.ContinueOnError)
	to := fs.String("to", "", "目标格式（mp3/m4a/aac/flac/wav/ogg/mp4/mov/webm/mkv）")
	crf := fs.Int("crf", 0, "视频质量 0-51（默认 23，仅视频输出）")
	q := fs.Int("q", 2, "音频质量 0-9（默认 2，仅音频输出）")
	outDir := fs.String("out", "", "输出目录（默认与源同目录）")
	quiet := fs.Bool("quiet", false, "安静模式（仅错误输出）")
	if err := fs.Parse(flagFirst(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("transcode: 需要输入文件")
	}
	if *to == "" {
		return fmt.Errorf("transcode: 缺少 -to 目标格式")
	}
	out, err := media.Transcode(fs.Arg(0), media.TranscodeConfig{
		To: *to, CRF: *crf, AudioQ: *q, OutDir: *outDir, Quiet: *quiet,
	})
	if err != nil {
		return err
	}
	fmt.Printf("transcode: %s → %s\n", fs.Arg(0), out)
	return nil
}

// RunOrganize 按类型归类整理下载目录。
func RunOrganize(args []string) error {
	fs := flag.NewFlagSet("organize", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "仅打印计划，不实际移动")
	dedupe := fs.Bool("dedupe", false, "按 sha256 去重（重复文件移入 .dupes/）")
	recurse := fs.Bool("r", false, "递归处理子目录")
	if err := fs.Parse(flagFirst(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("organize: 需要下载目录")
	}
	plans, err := media.Organize(fs.Arg(0), media.OrganizeConfig{
		DryRun: *dry, Dedupe: *dedupe, Recurse: *recurse,
	})
	if err != nil {
		return err
	}
	if *dry {
		fmt.Printf("organize: [dry-run] %d 条计划\n", len(plans))
	} else {
		fmt.Printf("organize: 完成 %d 项\n", len(plans))
	}
	for _, p := range plans {
		switch p.Kind {
		case "move":
			fmt.Printf("  move    %s → %s（%s）\n", p.Src, p.Dst, p.Why)
		case "duplicate":
			fmt.Printf("  dupe    %s → %s（%s）\n", p.Src, p.Dst, p.Why)
		default:
			fmt.Printf("  %-7s %s（%s）\n", p.Kind, p.Src, p.Why)
		}
	}
	return nil
}

// RunClean 清理下载目录广告/垃圾文件。
func RunClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "仅打印计划，不实际移动")
	if err := fs.Parse(flagFirst(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("clean: 需要下载目录")
	}
	res, err := media.Clean(fs.Arg(0), media.CleanConfig{DryRun: *dry})
	if err != nil {
		return err
	}
	verb := "清理"
	if *dry {
		verb = "[dry-run] 计划清理"
	}
	fmt.Printf("clean: %s %d 个文件 → .trash/\n", verb, res.Total)
	for _, f := range res.Moved {
		fmt.Printf("  %s\n", f)
	}
	if res.Total > 0 && !*dry {
		fmt.Printf("clean: 已移入 %s/.trash/（确认无误后可手动删除该目录）\n",
			strings.TrimRight(fs.Arg(0), `\/`))
	}
	return nil
}
