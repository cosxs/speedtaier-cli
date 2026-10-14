package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

func newTestApp(t *testing.T, args ...string) *app {
	t.Helper()
	a := &app{cfg: config{family: familyFlag{runner.FamilyAuto}}}
	if err := a.newRootCmd().ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFlags(t *testing.T) {
	a := newTestApp(t, "--family", "ipv6", "--province", "shandong", "--interface", "en0", "--operator", "all")
	c := a.cfg
	if c.family.mode != runner.Family6 || c.province != "shandong" || c.iface != "en0" || c.operator != runner.OperatorAll {
		t.Errorf("parsed config = %+v", c)
	}
	// 已移除的参数按未知参数报错。
	for _, flag := range []string{"--json", "--imei", "--latency-count", "--non-interactive", "--bandwidth"} {
		if err := a.newRootCmd().ParseFlags([]string{flag, "1"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s: err = %v, want unknown flag", flag, err)
		}
	}
	if err := a.newRootCmd().ParseFlags([]string{"--family", "x"}); err == nil {
		t.Error("--family x: want error")
	}
	if d := newTestApp(t).cfg; d.iface != runner.InterfaceAuto || d.family.mode != runner.FamilyAuto ||
		d.operator != runner.OperatorAuto {
		t.Errorf("defaults = %+v", d)
	}
}

func TestTestOptionsTimeout(t *testing.T) {
	run := func(args ...string) (taier.Options, error) { return newTestApp(t, args...).testOptions() }

	// 默认 0：由 RunTest 按 Budget 自动计算。
	if o, err := run(); err != nil || o.Timeout != 0 || o.Seconds != 10 || !o.SingleThread {
		t.Errorf("defaults: %+v, %v", o, err)
	}
	// 显式指定时须容纳各阶段的预热(下载 1.5s、上传 3s)、10 秒计量与 20 秒余量。
	// 默认单线程、多线程各测一次：20 + 2×(1.5+10) + 2×(3+10) = 69 秒。
	if _, err := run("--timeout", "68"); err == nil {
		t.Error("--timeout 68: want error")
	}
	if o, err := run("--timeout", "69"); err != nil || o.Timeout != 69*time.Second {
		t.Errorf("--timeout 69: %v, %v", o.Timeout, err)
	}
	// 只测多线程：20 + (1.5+10) + (3+10) = 44.5 秒。
	if _, err := run("--timeout", "44", "--no-single"); err == nil {
		t.Error("--timeout 44 --no-single: want error")
	}
	if _, err := run("--timeout", "45", "--no-single"); err != nil {
		t.Errorf("--timeout 45 --no-single: %v", err)
	}
	if _, err := run("--timeout", "45", "--threads", "1"); err != nil {
		t.Errorf("--timeout 45 --threads 1: %v", err)
	}
	if _, err := run("--timeout", "32", "--no-upload", "--no-single"); err != nil {
		t.Errorf("--timeout 32 --no-upload --no-single: %v", err)
	}
	if _, err := run("--threads", "0"); err == nil {
		t.Error("--threads 0: want error")
	}
}

func TestUpdateCheckEnabled(t *testing.T) {
	for _, tt := range []struct {
		name             string
		noFlag           bool
		env              string
		stderrIsTerminal bool
		want             bool
	}{
		{"interactive terminal", false, "", true, true},
		{"stderr redirected (cron, pipes)", false, "", false, false},
		{"flag", true, "", true, false},
		{"environment variable", false, "1", true, false},
	} {
		if got := updateCheckEnabled(tt.noFlag, tt.env, tt.stderrIsTerminal); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}

	a := newTestApp(t, "--no-update-check", "upgrade")
	if !a.cfg.noUpdateCheck {
		t.Error("--no-update-check not parsed")
	}
	// 测试环境中 stderr 不是终端，任何情况下都不检查，也不联网。
	if got := newTestApp(t).startUpdateCheck("v0.6.1")(); got != "" {
		t.Errorf("update check ran without a terminal: %q", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1500: "1.5 KB", 1_800_000_000: "1.8 GB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
