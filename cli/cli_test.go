package cli

import (
	"testing"

	"github.com/Mao-jh/porter/hash"
	"github.com/Mao-jh/porter/scheduler"
)

// TestParse_DefaultMode 默认模式 = ModeDefault(≤60%)。
func TestParse_DefaultMode(t *testing.T) {
	opt, err := Parse([]string{"http://127.0.0.1/x", "-mode", "default"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opt.Mode != scheduler.ModeDefault {
		t.Errorf("默认模式应为 ModeDefault, got %v", opt.Mode)
	}
	if opt.Verify != hash.SHA256 {
		t.Errorf("默认校验应为 sha256, got %v", opt.Verify)
	}
}

// TestParse_MaxPerf 显式 max → ModeMaxPerf（R-3）。
func TestParse_MaxPerf(t *testing.T) {
	opt, err := Parse([]string{"http://127.0.0.1/x", "-mode", "max"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if opt.Mode != scheduler.ModeMaxPerf {
		t.Errorf("max 模式应为 ModeMaxPerf")
	}
}

// TestParse_RejectsBadScheme 拒绝非 http/https/ftp URL。
func TestParse_RejectsBadScheme(t *testing.T) {
	if _, err := Parse([]string{"gopher://127.0.0.1/x"}); err == nil {
		t.Error("应拒绝 gopher")
	}
	if _, err := Parse([]string{}); err == nil {
		t.Error("空参数应报错")
	}
}

// TestParse_VerifyAlgo 校验算法透传。
func TestParse_VerifyAlgo(t *testing.T) {
	cases := []struct {
		flag string
		want hash.Algorithm
	}{
		{"sha256", hash.SHA256},
		{"md5", hash.MD5},
		{"none", ""},
	}
	for _, c := range cases {
		opt, err := Parse([]string{"http://127.0.0.1/x", "-verify", c.flag})
		if err != nil {
			t.Fatalf("Parse(%s): %v", c.flag, err)
		}
		if opt.Verify != c.want {
			t.Errorf("-verify %s -> %v want %v", c.flag, opt.Verify, c.want)
		}
	}
}

// TestParse_FTPAccepted 第 12 轮：ftp/ftps 进白名单（协议分发表见 DESIGN §2.3b）。
func TestParse_FTPAccepted(t *testing.T) {
	for _, u := range []string{"ftp://127.0.0.1/f.bin", "ftps://127.0.0.1:990/f.bin"} {
		if _, err := Parse([]string{u}); err != nil {
			t.Errorf("应接受 %s, got %v", u, err)
		}
	}
}

// TestSanitizeFilename 文件名净化（合规：防路径逃逸/Windows 保留名/非法字符）。
func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"file.zip", "file.zip"},
		{"../passwd", ".._passwd"}, // 分隔符替换后为合法纯文件名（Join 后不逃逸目录）
		{"..", ""},                     // 整体父目录 → 空串（回退默认名）
		{".", ""},                      // 整体当前目录 → 空串
		{"a/b/c.txt", "a_b_c.txt"},     // 分隔符替换
		{`a\b.bin`, "a_b.bin"},         // Windows 反斜杠
		{"CON", "_CON"},                // 保留设备名
		{"con.zip", "_con.zip"},        // 保留设备名（不分大小写/带扩展名）
		{"LPT1", "_LPT1"},              // 保留设备名
		{"a<b>:c?.txt", "a_b__c_.txt"}, // Windows 非法字符
		{"name.", "name"},              // 结尾点截断
		{"name . ", "name"},            // 结尾空格+点截断
		{"正常名字-1.tar.gz", "正常名字-1.tar.gz"}, // 多字节保留
		{"", ""}, // 空 → 回退默认名
	}
	for _, c := range cases {
		if got := sanitizeFilename(c.in); got != c.want {
			t.Errorf("sanitize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// TestDeriveOutputs_Sanitized 多任务输出名走净化路径：危险名不落盘。
func TestDeriveOutputs_Sanitized(t *testing.T) {
	outs := deriveOutputs([]string{
		"http://127.0.0.1/CON",
		"http://127.0.0.1/../../evil",
		"http://127.0.0.1/plain.bin",
		"http://127.0.0.1/plain.bin", // 同名去重
	})
	if outs[0] != "_CON" {
		t.Errorf("保留名未净化: %q", outs[0])
	}
	if outs[1] != "evil" {
		t.Errorf("穿越路径未净化: %q", outs[1])
	}
	if outs[2] != "plain.bin" || outs[3] != "plain-2.bin" {
		t.Errorf("普通名/去重异常: %q %q", outs[2], outs[3])
	}
}

// TestOptionsDefaults 默认输出文件名推导逻辑（opt.Output 空时由 Run 填充）。
func TestOptionsDefaults(t *testing.T) {
	opt, _ := Parse([]string{"https://127.0.0.1/file.zip"})
	if opt.Output != "" {
		t.Errorf("Output 应由 Run 推导, 初值应为空, got %q", opt.Output)
	}
	if opt.StateDir == "" {
		t.Error("StateDir 应有默认值")
	}
}
