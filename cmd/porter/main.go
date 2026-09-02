// Command porter 是 Porter 下载器的 CLI 入口。
// 子命令：tasks（列出持久化任务）/ rm（删除任务）/ clean（清理完成记录）/
// probe（探测资源，不下载）/ meta（响应头）/ retry（续传）/ find / ls / bookmarks /
// extract / torrent / info / transcode / organize / scrub；默认动作：下载。
// Agent-First 契约（对齐《面向 AI 的 CLI 上下文工程最佳实践》）：
//   - porter help / <子命令> --help：分层帮助，帮助给出下一跳而非全文；
//   - porter schema：机器可读命令清单（直接转工具 schema）；
//   - --output json|ndjson：统一封套，错误带 code/retryable/next_actions；
//   - 退出码长期稳定：0=成功 · 2=用法错误 · 1=执行失败（历史兼容，见 help）。
// 构建：GOFLAGS=-mod=readonly GOPROXY=off CGO_ENABLED=0 go build -ldflags="-s -w" ./cmd/porter
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Mao-jh/porter/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, mainHelp)
		os.Exit(2) // 用法错误（历史语义）
	}
	switch args[0] {
	case "help", "--help", "-h", "-help":
		fmt.Fprint(os.Stdout, mainHelp)
		return
	case "version", "ver", "--version":
		printVersion()
		return
	case "schema":
		runSchema()
		return
	}

	// 子命令各自的 --help（帮助是教学材料，但由精确契约兜底——schema 管契约）
	if len(args) >= 2 {
		if args[1] == "--help" || args[1] == "-h" {
			if h, ok := subHelp[args[0]]; ok {
				fmt.Fprint(os.Stdout, h)
				fmt.Fprintf(os.Stdout, "\nNEXT:\n  porter %s --output json   # 机器可读输出\n  porter schema              # 命令清单与退出码\n", args[0])
				return
			}
		}
	}

	switch args[0] {
	case "tasks", "status": // 列出持久化任务与历史
		dir, mode, _, err := subArgs(args[1:])
		if err != nil {
			die(2, "tasks", err, mode)
		}
		if err := cli.RunTasks(dir, mode); err != nil {
			// RunTasks/emit 出错主要是 IO；--output json 时顺带输出错误封套
			die(1, "tasks", err, mode)
		}
	case "rm": // 删除指定任务（拒绝运行中且有 .part 的任务）
		dir, mode, ids, err := subArgs(args[1:])
		if err != nil {
			die(2, "rm", err, mode)
		}
		removed, refused, err := cli.RemoveTasks(dir, ids, false)
		if err != nil {
			die(1, "rm", err, mode)
		}
		if mode == cli.OutputTable {
			fmt.Fprintf(os.Stderr, "已删除 %d 个任务\n", removed)
			for _, r := range refused {
				fmt.Fprintln(os.Stderr, "跳过:", r)
			}
		} else {
			env := cli.OKEnv("rm.result", struct {
				Removed int      `json:"removed"`
				Refused []string `json:"refused,omitempty"`
			}{Removed: removed, Refused: refused})
			_ = cli.Emit(os.Stdout, mode, env)
		}
		if len(refused) > 0 {
			os.Exit(1)
		}
	case "clean": // 清理全部 status=done 的完成记录
		dir, mode, _, err := subArgs(args[1:])
		if err != nil {
			die(2, "clean", err, mode)
		}
		removed, refused, err := cli.RemoveTasks(dir, nil, true)
		if err != nil {
			die(1, "clean", err, mode)
		}
		if mode == cli.OutputTable {
			fmt.Fprintf(os.Stderr, "已清理 %d 个完成记录\n", removed)
			for _, r := range refused {
				fmt.Fprintln(os.Stderr, "跳过:", r)
			}
		} else {
			env := cli.OKEnv("clean.result", struct {
				Removed int      `json:"removed"`
				Refused []string `json:"refused,omitempty"`
			}{Removed: removed, Refused: refused})
			_ = cli.Emit(os.Stdout, mode, env)
		}
		if len(refused) > 0 {
			os.Exit(1)
		}
	case "probe": // 探测资源：size / ranged / name / final_url（不下载）
		opt, err := cli.Parse(args[1:])
		if err != nil {
			die(2, "probe", err, cli.OutputTable)
		}
		if err := cli.RunProbe(ctx, opt); err != nil {
			die(1, "probe", err, opt.OutMode)
		}
	case "meta": // 查看响应头：状态行 + key: value（对标 curl -I）
		opt, err := cli.Parse(args[1:])
		if err != nil {
			die(2, "meta", err, cli.OutputTable)
		}
		if err := cli.RunMeta(ctx, opt); err != nil {
			die(1, "meta", err, opt.OutMode)
		}
	case "retry": // 续传重跑状态目录中的未完成任务
		opt, err := cli.ParseRetry(args[1:])
		if err != nil {
			die(2, "retry", err, cli.OutputTable)
		}
		if err := cli.RunRetry(ctx, opt); err != nil {
			die(1, "retry", err, opt.OutMode)
		}
	case "find": // 抓取页面提取可下载链接
		if err := cli.RunFind(ctx, args[1:]); err != nil {
			die(1, "find", err, cli.OutputTable)
		}
	case "ls": // FTP 目录列取
		if err := cli.RunLS(ctx, args[1:]); err != nil {
			die(1, "ls", err, cli.OutputTable)
		}
	case "bookmarks": // 解析浏览器书签导出 HTML
		if err := cli.RunBookmarks(args[1:]); err != nil {
			die(1, "bookmarks", err, cli.OutputTable)
		}
	case "extract": // 从文本提取 URL
		if err := cli.RunExtract(args[1:]); err != nil {
			die(1, "extract", err, cli.OutputTable)
		}
	case "torrent": // 解析 .torrent / 磁力链接
		if err := cli.RunTorrent(args[1:]); err != nil {
			die(1, "torrent", err, cli.OutputTable)
		}
	case "info": // 媒体信息预览
		if err := cli.RunInfo(args[1:]); err != nil {
			die(1, "info", err, cli.OutputTable)
		}
	case "transcode": // ffmpeg 转码
		if err := cli.RunTranscode(args[1:]); err != nil {
			die(1, "transcode", err, cli.OutputTable)
		}
	case "organize": // 按类型归类整理
		if err := cli.RunOrganize(args[1:]); err != nil {
			die(1, "organize", err, cli.OutputTable)
		}
	case "scrub": // 广告/垃圾文件移入 .trash（文件级去广告）
		if err := cli.RunClean(args[1:]); err != nil {
			die(1, "scrub", err, cli.OutputTable)
		}
	default:
		opt, err := cli.Parse(args)
		if err != nil {
			die(2, "download", err, cli.OutputTable)
		}
		if err := cli.Run(ctx, opt); err != nil {
			die(1, "download", err, opt.OutMode)
		}
	}
}

// die 统一错误出口：人类可读错误写 stderr（历史行为不变）；
// mode=json|ndjson 时把结构化错误封套（code/retryable/next_actions）写 stdout。
// 退出码保持 0/2/1（0=成功；2=用法错误；1=执行失败），帮助中公开映射，长期稳定。
func die(code int, command string, err error, mode cli.OutputMode) {
	msg := err.Error()
	fmt.Fprintln(os.Stderr, "porter "+command+":", msg)
	if mode != cli.OutputTable {
		ae := cli.Classify(err, "porter "+command)
		ae.Message = msg
		_ = cli.Emit(os.Stdout, mode, cli.ErrEnv("error", []cli.AppError{ae}))
	}
	os.Exit(code)
}

// printVersion 输出版本与契约信息（版本可识别：AI 判定帮助/封套缓存有效性的依据）。
func printVersion() {
	fmt.Fprintf(os.Stdout, "porter %s（CLI 契约 schema v%s；零第三方依赖引擎；CLI/TUI/MCP 同引擎）\n",
		cli.Version, cli.ContractVersion)
}

// runSchema 输出机器可读命令清单（L0 自省）：名称/用法/副作用/幂等性/输出格式，
// 附带退出码映射与封套 schema 版本——AI 可直接据此生成工具 schema，无需读帮助正文。
func runSchema() {
	env := cli.OKEnv("schema.list", schemaOut{
		Commands: schemaCommands,
		ExitCodes: map[string]string{
			"0":  "success（按 schema 解析 stdout）",
			"2":  "usage_or_invalid_argument（修订参数，不盲目重试）",
			"1":  "execution_failed（见封套 errors[].code 细分；transient 可重试）",
		},
		Envelope: "schemaVersion/type/ok/data/warnings/errors/meta；错误项=code/retryable/message/next_actions",
	})
	env.Meta.Command = "porter schema"
	_ = cli.Emit(os.Stdout, cli.OutputJSON, env)
}

// schemaOut schema 响应体。
type schemaOut struct {
	Commands  []schemaCmd       `json:"commands"`
	ExitCodes map[string]string `json:"exitCodes"`
	Envelope  string            `json:"envelope"`
}

// schemaCmd 单命令的机器契约。
type schemaCmd struct {
	Name        string   `json:"name"`                 // 命令标识（默认动作为 download）
	Usage       string   `json:"usage"`                // 完整用法（可替换占位符）
	Description string   `json:"description"`          // 一句话用途 + 触发条件
	SideEffect  string   `json:"sideEffect"`           // read | write | destructive
	Idempotent  bool     `json:"idempotent"`           // 重复执行是否安全（断点续传/去重）
	Outputs     []string `json:"outputFormats"`        // 支持的输出格式
}

var schemaCommands = []schemaCmd{
	{Name: "download", Usage: "porter <url> [url2 ...] [-o OUT] [-n N] [-j N] [-i urls.txt] [-limit BPS] [-proxy URL] [-load-cookies FILE] [-summary] [-mirror u1,u2] [-min-rate B] [-stall SEC] [-retry-forever] [-verify sha256] [--output json|ndjson|table]",
		Description: "下载一个或多个 URL（分片并行+字节级断点续传+完成后校验；默认动作，无子命令名）。", SideEffect: "write", Idempotent: true,
		Outputs: []string{"table", "json", "ndjson"}},
	{Name: "tasks", Usage: "porter tasks [-state-dir DIR] [--output json|ndjson|table]",
		Description: "列出持久化任务与历史（含完成校验和）。", SideEffect: "read", Idempotent: true,
		Outputs: []string{"table", "json", "ndjson"}},
	{Name: "probe", Usage: "porter probe <url> [-proxy URL] [-load-cookies FILE] [-H \"K: V\"] [--output json|ndjson|table]",
		Description: "只探测不下载：size/ranged/name/final_url（wget --spider 对标）。", SideEffect: "read", Idempotent: true,
		Outputs: []string{"table", "json", "ndjson"}},
	{Name: "meta", Usage: "porter meta <url> [-proxy URL] [-load-cookies FILE] [-H \"K: V\"] [--output json|ndjson|table]",
		Description: "查看响应头（curl -I 对标）。", SideEffect: "read", Idempotent: true,
		Outputs: []string{"table", "json", "ndjson"}},
	{Name: "retry", Usage: "porter retry [-state-dir DIR] [-limit BPS] [-proxy URL] [-load-cookies FILE] [--output json|ndjson|table]",
		Description: "续传重跑状态目录中的未完成任务（done 跳过）。", SideEffect: "write", Idempotent: true,
		Outputs: []string{"table", "json", "ndjson"}},
	{Name: "rm", Usage: "porter rm <id>... [-state-dir DIR] [--output json|ndjson|table]",
		Description: "删除指定任务（拒绝运行中且有 .part 的任务）。", SideEffect: "destructive", Idempotent: true,
		Outputs: []string{"table", "json", "ndjson"}},
	{Name: "clean", Usage: "porter clean [-state-dir DIR] [--output json|ndjson|table]",
		Description: "清理全部 status=done 的完成记录。", SideEffect: "destructive", Idempotent: true,
		Outputs: []string{"table", "json", "ndjson"}},
	{Name: "find", Usage: "porter find <page-url> [-ext mp4,mkv] [-probe] [-depth N] [-out urls.txt]",
		Description: "抓取页面提取可下载链接（输出每行一个 URL，可直接喂 -i）。", SideEffect: "read", Idempotent: true, Outputs: []string{"lines"}},
	{Name: "ls", Usage: "porter ls <ftp-url> [-l] [-r]",
		Description: "FTP 目录列取/递归（每行一个 URL）。", SideEffect: "read", Idempotent: true, Outputs: []string{"lines"}},
	{Name: "bookmarks", Usage: "porter bookmarks <bookmarks.html> [-out urls.txt]",
		Description: "解析浏览器书签导出（Netscape 格式）。", SideEffect: "read", Idempotent: true, Outputs: []string{"lines"}},
	{Name: "extract", Usage: "porter extract <file|-> [-out urls.txt]",
		Description: "从任意文本/日志提取 URL。", SideEffect: "read", Idempotent: true, Outputs: []string{"lines"}},
	{Name: "torrent", Usage: "porter torrent <file.torrent|magnet:...>",
		Description: "解析种子元数据：info_hash/WebSeed 直链（不实现 BT 对等协议）。", SideEffect: "read", Idempotent: true, Outputs: []string{"lines"}},
	{Name: "info", Usage: "porter info <file>",
		Description: "纯 Go 容器解析媒体信息（时长/分辨率/编码）。", SideEffect: "read", Idempotent: true, Outputs: []string{"lines"}},
	{Name: "transcode", Usage: "porter transcode <file> -to mp3|mp4|... [-crf N] [-out dir]",
		Description: "调用系统 ffmpeg 转码（无 ffmpeg 时明确报错）。", SideEffect: "write", Idempotent: false, Outputs: []string{"table"}},
	{Name: "organize", Usage: "porter organize <dir> [-dry-run] [-dedupe]",
		Description: "按媒体类型归类（只移动不删除；-dry-run 先看计划）。", SideEffect: "destructive", Idempotent: true, Outputs: []string{"table"}},
	{Name: "scrub", Usage: "porter scrub <dir> [-dry-run]",
		Description: "广告/垃圾文件移入 .trash。", SideEffect: "destructive", Idempotent: true, Outputs: []string{"table"}},
}

// subArgs 解析子命令参数：提取 -state-dir 与 --output，其余非旗标参数原样返回（任务 ID 等）。
func subArgs(args []string) (dir string, mode cli.OutputMode, rest []string, err error) {
	dir = ".downloader"
	mode = cli.OutputTable
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-state-dir":
			if i+1 >= len(args) {
				return dir, mode, rest, errors.New("-state-dir 缺少目录参数")
			}
			dir = args[i+1]
			i++
		case "--output":
			if i+1 >= len(args) {
				return dir, mode, rest, errors.New("--output 缺少取值（应为 table|json|ndjson）")
			}
			if m, merr := cli.ParseOutputMode(args[i+1]); merr != nil {
				return dir, mode, rest, merr
			} else {
				mode = m
			}
			i++
		default:
			rest = append(rest, a)
		}
	}
	return dir, mode, rest, nil
}

// mainHelp 分层帮助（L0）：一行用途 + 命令清单 + 自省下一跳。
// 详细参数不在此反复（token 预算）；单命令帮助走 `porter <子命令> --help`，
// 全量参数文档在 USAGE.md。帮助与 schema 单一来源（schemaCommands 生成命令签名）。
const mainHelp = `porter —— AI 的专职搬运工：多线程下载器（零第三方依赖引擎；CLI/TUI/MCP 三形态共用）

用法（默认动作=下载）:
  porter <url> [url2 ...] [-o OUT] [-n N] [-j N] [-i urls.txt] [-limit BPS] [-verify sha256]
         [-proxy URL] [-load-cookies FILE] [-summary] [-mirror u1,u2] [-min-rate B] [-stall SEC] [-retry-forever]
         [--output table|json|ndjson]
  字节级断点续传 + 完成后 sha256 校验；默认仅允许回环目标，公网需 -proxy（H-3 审计边界）。

信息（只读，不下载）:
  porter probe <url> [--output json]    探测 size/ranged/name（wget --spider 对标）
  porter meta  <url> [--output json]    查看响应头（curl -I 对标）
  porter tasks [--output json]          持久化任务与历史（含校验和）
  porter schema                         机器可读命令清单 + 退出码映射（JSON）
  porter version                        版本与契约信息

任务管理:
  porter retry                          续传重跑未完成任务
  porter rm <id>... / clean             删除任务 / 清理完成记录

链接发现（输出每行一个 URL，可直接喂 -i）:
  porter find <page> [-ext mp4,mkv] [-probe] [-depth N]     页面提取下载链接
  porter ls <ftp-url> [-r] + bookmarks <html> + extract <file|->
  porter torrent <file.torrent|magnet:...>                  种子解析（info_hash/WebSeed）

下载后处理:
  porter info <file> + transcode <file> -to mp3 + organize <dir> [-dry-run] + scrub <dir>

Machine（AI 消费方）:
  --output json|ndjson  统一封套：schemaVersion/type/ok/data/warnings/errors/meta
                         错误项含 code/retryable/message/next_actions（可直接执行的下一条命令）
  --output 与 -o 不同：-o 是输出路径（curl 语义），--output 是输出格式。
  stdio 分工：stdout=数据（人类表或 JSON）；stderr=日志/进度/告警。
  退出码（长期稳定）: 0=成功 · 2=用法错误 · 1=执行失败（transient 类已带重试语义）

NEXT:
  porter schema                # 机器可读命令清单与退出码
  porter <子命令> --help       # 单命令帮助
  porter tasks --output json   # 任务与校验和（字段与 MCP list_tasks 同源）
`

// subHelp 单子命令的分层帮助（L1，命令命中时按需加载；字段语义在 --output json）。
var subHelp = map[string]string{
	"probe": `USAGE:
  porter probe <url> [-proxy URL] [-load-cookies FILE] [-H "K: V"] [--output json|ndjson|table]

WHAT:
  只探测不下载：返回 size/ranged/name(Content-Disposition)/final_url(重定向)。
  probe 无副作用，可安全重复执行。

OUTPUT:
  table（默认）: url= / size= / ranged= / name= / final_url=（脚本友好 key=value）
  --output json: 统一封套 type=probe.list
  --output ndjson: 每 URL 一行封套（type=probe.list.row）

SIDE EFFECTS: 无（只读）
EXAMPLES:
  porter probe https://host/file.bin
  porter probe https://host/file.bin --output json | jq .data[0].size_bytes
`,
	"meta": `USAGE:
  porter meta <url> [-proxy URL] [-load-cookies FILE] [-H "K: V"] [--output json|ndjson|table]

WHAT:
  查看响应头（状态行 + 全部 header，curl -I 对标）；无副作用。

OUTPUT:
  table（默认）: "URL 状态行" + "Key: Value"（key 按字典序）
  --output json: 统一封套 type=meta.list，headers 为 {key:[values]}
`,
	"tasks": `USAGE:
  porter tasks [-state-dir DIR] [--output json|ndjson|table]

WHAT:
  列出持久化任务与历史（断点续传进度在此一目了然）。

OUTPUT:
  table（默认）: 状态/进度/时间/URL/输出路径
  --output json: type=tasks.list，字段=id/url/file_size/done/status/hash/updated_at/shards
                 （与 MCP list_tasks 同源；hash 为完成任务的校验和）
`,
	"rm": `USAGE:
  porter rm <id>... [-state-dir DIR] [--output json|ndjson|table]

WHAT:
  删除指定任务状态与同名 .part（运行中且有 .part 的任务拒绝删除）。
  危险操作确认：拒绝即保护，需人工确认无在途引擎后重试。
`,
	"clean": `USAGE:
  porter clean [-state-dir DIR] [--output json|ndjson|table]

WHAT:
  清理全部 status=done 的完成记录（历史收尾）。
`,
}