// Command porter-tui-proto 是 TUI 原型三步法的第 2 步工具：把多个布局变体
// 用同一份 fixture 渲染后横向并排输出，供肉眼对比选优（第 3 步定稿）。
//
// 用法：
//
//	porter-tui-proto [-out DIR]
//
// 产出（DIR 缺省当前目录）：
//   - proto_cmp_ansi.txt   带颜色的并排对比（真终端查看）
//   - proto_cmp_plain.txt  去 ANSI 的并排对比（任何编辑器/工具查看）
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Mao-jh/porter/tui/proto"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func main() {
	outDir := flag.String("out", "", "输出目录（缺省当前目录）")
	flag.Parse()

	ts := proto.Fixture()
	cols := []struct {
		name string
		view string
	}{
		{"A 现状基线", proto.VariantA(ts)},
		{"B 协议差异化+底栏按钮", proto.VariantB(ts)},
		{"C 双行任务卡", proto.VariantC(ts)},
	}
	widths := map[string]int{"A 现状基线": 84, "B 协议差异化+底栏按钮": 96, "C 双行任务卡": 80}
	var views []string
	var headers []string
	for _, c := range cols {
		views = append(views, c.view)
		headers = append(headers, c.name)
	}
	side := proto.SideBySide(views, widths[cols[0].name])

	// 栏名标题行
	titleRow := ""
	for i, h := range headers {
		titleRow += h
		titleRow += strings.Repeat(" ", widths[cols[0].name]-len(h))
		titleRow += " │ "
		_ = i
	}
	side = titleRow + "\n" + side

	dir := "."
	if *outDir != "" {
		dir = *outDir
	}
	_ = os.MkdirAll(dir, 0o755)
	ansiPath := filepath.Join(dir, "proto_cmp_ansi.txt")
	plainPath := filepath.Join(dir, "proto_cmp_plain.txt")
	if err := os.WriteFile(ansiPath, []byte(side+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	plain := ansiRe.ReplaceAllString(side, "")
	if err := os.WriteFile(plainPath, []byte(plain+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	// 同时打印各变体单独渲染的行数（对比信息密度）
	for _, c := range cols {
		lines := strings.Split(c.view, "\n")
		pl := ansiRe.ReplaceAllString(c.view, "")
		fmt.Printf("%-28s 行数=%-3d 信息量=%-5d 字符\n", c.name, len(lines), len(pl))
	}
	fmt.Println("ANSI 并排对比:", ansiPath)
	fmt.Println("Plain 并排对比:", plainPath)
}
