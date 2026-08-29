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
