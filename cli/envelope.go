// envelope.go 实现 Agent-First CLI 契约层（对齐《面向 AI 的 CLI 上下文工程最佳实践》）：
//
//   - 统一结果封套：schemaVersion / type / ok / data / warnings / errors / meta；
//   - 结构化错误：code / retryable / message / next_actions（可直接执行的下一条命令）；
//   - 输出模式：--output table|json|ndjson（默认 table = 人类格式，与历史输出逐字兼容；
//     json/ndjson 是同一功能的机器一等出口，不改变退出码与字段语义）。
//
// 契约不变量（同封套即契约）：
//   - stdout 只承载可消费数据（人类表或 JSON）；诊断/进度/装饰恒走 stderr；
//   - 成功封套 ok=true 时 errors 为空；失败封套 ok=false 时 data 为空——二者互斥；
//   - json 序列化关闭 HTML 转义，字段顺序稳定，NDJSON 每行一条独立封套（可流式消费）。
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/Mao-jh/porter/network"
)

// ContractVersion 输出封套 schema 版本。字段新增用追加方式；重命名/语义变化必须升版，
// 供 AI 客户端缓存帮助与解析时做版本判定。
const ContractVersion = "1"

// Version 契约层版本（schema/meta/version 子命令上报；独立于下载引擎版本语义）。
const Version = "1.0.0"

// OutputMode 输出模式（--output 的合法取值）。
type OutputMode string

const (
	OutputTable  OutputMode = "table"  // 默认：人类可读
	OutputJSON   OutputMode = "json"   // 单封套（单对象或有限集合）
	OutputNDJSON OutputMode = "ndjson" // 每行一条封套（分页/流式/管道）
)

// ParseOutputMode 校验 --output 取值（拒绝未知枚举，不"尽力猜测"）。
func ParseOutputMode(v string) (OutputMode, error) {
	switch OutputMode(v) {
	case "", OutputTable:
		return OutputTable, nil
	case OutputJSON:
		return OutputJSON, nil
	case OutputNDJSON:
		return OutputNDJSON, nil
	default:
		return "", fmt.Errorf("非法 --output: %q（应为 table|json|ndjson）", v)
	}
}

// NextAction 一条可直接复制的纠错下一跳。
type NextAction struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// AppError 结构化错误项（退出 1/2 时随封套给 AI 消费方）。
type AppError struct {
	Code        string       `json:"code"`              // 稳定错误码（见下方 Code* 常量）
	Retryable   bool         `json:"retryable"`         // 是否值得按原请求重试
	Message     string       `json:"message"`           // 人类可读原因（与退出码语义一致）
	Doc         string       `json:"doc,omitempty"`     // 错误文档锚点（可选）
	NextActions []NextAction `json:"next_actions,omitempty"` // 可执行下一步，无则省略
}

// 错误码（稳定对外契约；映射文档见帮助「退出码」节与 schema）
const (
	CodeInvalidArgument = "invalid_argument"  // 参数/输入不合法 → 修订后重试（不盲重试）
	CodeNotFound        = "not_found"         // 资源/文件不存在 → 检查 ID/路径
	CodePermission      = "permission_denied" // 权限/边界拒绝（如 H-3 非回环）→ 走显式放行通道
	CodeConflict        = "conflict"          // 状态冲突 → 读取资源版本后再决定
	CodeRateLimited     = "rate_limited"      // 被限流 → 遵守退避/Retry-After
	CodeTransient       = "transient"         // 网络/瞬时错误 → 可重试
	CodeCancelled       = "cancelled"         // 用户/上下文取消
	CodeInternal        = "internal"          // 内部错误 → 上报诊断
)

// Envelope 统一结果封套。所有 --output json|ndjson 的输出根对象。
type Envelope struct {
	SchemaVersion string       `json:"schemaVersion"`
	Type          string       `json:"type"`
	OK            bool         `json:"ok"`
	Data          any          `json:"data,omitempty"`
	Warnings      []AppError   `json:"warnings,omitempty"`
	Errors        []AppError   `json:"errors,omitempty"`
	Meta          EnvelopeMeta `json:"meta,omitempty"`
}

// EnvelopeMeta 封套元数据（命令身份 + 版本，AI 可据此判缓存有效性）。
type EnvelopeMeta struct {
	Command string `json:"command,omitempty"`
	Version string `json:"version,omitempty"`
}

// OKEnv 构造成功封套（data 可为 nil；warnings 非 nil 时附带非致命告警）。
func OKEnv(typ string, data any) *Envelope {
	return &Envelope{SchemaVersion: ContractVersion, Type: typ, OK: true, Data: data, Meta: EnvelopeMeta{Version: Version}}
}

// ErrEnv 构造失败封套（data 恒为空；错误走 errors）。
func ErrEnv(typ string, errs []AppError) *Envelope {
	if errs == nil {
		errs = []AppError{}
	}
	return &Envelope{SchemaVersion: ContractVersion, Type: typ, OK: false, Errors: errs, Meta: EnvelopeMeta{Version: Version}}
}

// Emit 以 --output 指定模式输出封套：
//   - table：不做任何处理（人类输出由调用方直接写 stdout）；
//   - json：单封套（含换行，兼容逐行读取）；
//   - ndjson：封套内 data 必须是可迭代列表 —— 每个元素逐行一条封套，当前封套仅作头/尾。
//
// 返回 false 表示调用方应自行按 table 输出。
func Emit(w io.Writer, mode OutputMode, env *Envelope) error {
	switch mode {
	case OutputJSON:
		return writeJSON(w, env)
	case OutputNDJSON:
		return writeNDJSON(w, env)
	default:
		return nil
	}
}

// writeJSON 输出单个封套（标准 json，字段顺序稳定，不转义 HTML）。
func writeJSON(w io.Writer, env *Envelope) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(env)
}

// writeNDJSON 把封套的 data 展开为逐行封套：
//
//	{"type":"x.list.row","ok":true,"data":{...}}  ← 每元素一行
//
// data 为非列表（单对象）时退回单封套输出（调用方应避免这种用法）。
func writeNDJSON(w io.Writer, env *Envelope) error {
	if env.Data == nil {
		return nil
	}
	rv := reflect.ValueOf(env.Data)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		for i := 0; i < rv.Len(); i++ {
			row := *env
			row.Type = env.Type + ".row"
			row.Data = rv.Index(i).Interface()
			row.Errors = nil
			row.Meta = EnvelopeMeta{} // 行级精简：命令身份由首行 data 携带即可
			if err := writeJSON(w, &row); err != nil {
				return err
			}
		}
		return nil
	}
	return writeJSON(w, env)
}

// Classify 把底层错误映射为结构化契约错误。
// 判定规则基于可观测条件（上下文取消 / H-3 回环边界 / 已知前缀），不依赖猜测。
// command 用于构造 next_actions 里的可复制纠错命令。
func Classify(err error, command string) AppError {
	if err == nil {
		return AppError{Code: CodeInternal, Retryable: false, Message: "unknown error"}
	}
	if errors.Is(err, context.Canceled) {
		return AppError{Code: CodeCancelled, Retryable: false, Message: err.Error()}
	}
	// 确定性字符串模式优先（可观测条件明确）；其余按类型结构判重试语义
	s := err.Error()
	switch {
	case strings.Contains(s, "loopback"): // H-3 审计边界：非回环目标
		return AppError{Code: CodePermission, Retryable: false, Message: s,
			NextActions: []NextAction{
				{Command: command + " -proxy http://host:port", Description: "CLI 以 -proxy 作为公网唯一放行通道（设置代理即显式允许出站）"},
				{Command: "porter-mcp -allow-remote", Description: "MCP 服务端另有 -allow-remote 产品开关"},
			}}
	case strings.Contains(s, "429"):
		return AppError{Code: CodeRateLimited, Retryable: true, Message: s,
			NextActions: []NextAction{{Command: command, Description: "已尊重 Retry-After 退避；稍后重试更稳妥"}}}
	case strings.Contains(s, "参数错误") || strings.Contains(s, "非法") ||
		strings.Contains(s, "不支持的") || strings.Contains(s, "无效") ||
		strings.Contains(s, "未提供") || strings.Contains(s, "缺失") ||
		strings.Contains(s, "目录不存在"): // 输出目录缺失属用法错误
		return AppError{Code: CodeInvalidArgument, Retryable: false, Message: s}
	case strings.Contains(s, "未找到") || strings.Contains(s, "not found") ||
		strings.Contains(s, "no such file"):
		return AppError{Code: CodeNotFound, Retryable: false, Message: s}
	}
	if network.Retryable(err) {
		return AppError{Code: CodeTransient, Retryable: true, Message: err.Error(),
			NextActions: []NextAction{{Command: command, Description: "瞬时错误已指数退避重试，可再次执行原命令（断点续传保护）"}}}
	}
	return AppError{Code: CodeInternal, Retryable: false, Message: s}
}