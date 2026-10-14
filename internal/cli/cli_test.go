package cli

import (
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
	a := newTestApp(t, "--family", "ipv6", "--province", "shandong", "--interface", "en0", "--json", "--non-interactive")
	c := a.cfg
	if c.family.mode != runner.Family6 || c.province != "shandong" || c.iface != "en0" || !c.json || !c.nonInteractive {
		t.Errorf("parsed config = %+v", c)
	}
	if err := a.newRootCmd().ParseFlags([]string{"--family", "x"}); err == nil {
		t.Error("--family x: want error")
	}
	if d := newTestApp(t).cfg; d.iface != runner.InterfaceAuto || d.family.mode != runner.FamilyAuto {
		t.Errorf("defaults = %+v", d)
	}
}

func TestTestOptionsTimeout(t *testing.T) {
	run := func(args ...string) (taier.Options, error) { return newTestApp(t, args...).testOptions() }

	// 默认 0：由 RunTest 按 Budget 自动计算。
	if o, err := run(); err != nil || o.Timeout != 0 || o.Seconds != 10 {
		t.Errorf("defaults: %+v, %v", o, err)
	}
	// 显式指定时须容纳预热(1.5s+3s)、两个 10 秒阶段与 20 秒余量。
	if _, err := run("--timeout", "40"); err == nil {
		t.Error("--timeout 40: want error")
	}
	if o, err := run("--timeout", "45"); err != nil || o.Timeout != 45*time.Second {
		t.Errorf("--timeout 45: %v, %v", o.Timeout, err)
	}
	if _, err := run("--timeout", "32", "--no-upload"); err != nil {
		t.Errorf("--timeout 32 --no-upload: %v", err)
	}
	if _, err := run("--threads", "0"); err == nil {
		t.Error("--threads 0: want error")
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1500: "1.5 KB", 1_800_000_000: "1.8 GB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
