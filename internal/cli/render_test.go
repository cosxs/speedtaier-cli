package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/pterm/pterm"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

func TestMain(m *testing.M) {
	pterm.DisableColor()
	os.Exit(m.Run())
}

// wideAmbiguous 让模糊宽度字符(━、─ 等)占两格，模拟东亚语言环境下的终端。
func wideAmbiguous(t *testing.T, wide bool) {
	t.Helper()
	old := runewidth.DefaultCondition.EastAsianWidth
	runewidth.DefaultCondition.EastAsianWidth = wide
	t.Cleanup(func() { runewidth.DefaultCondition.EastAsianWidth = old })
}

func TestRenderBlocks(t *testing.T) {
	wideAmbiguous(t, false)
	v := newTestView(node, "ipv4", stages)
	r := renderer{width: 100}
	v.update(idleDone)
	v.update(downOne)
	v.update(downMid)

	for name, tt := range map[string]struct{ got, want string }{
		"header": {r.header(v), `
  INFO    测试 电信 IPv4 济南电信 (58.56.118.126)
`},
		"idle latency": {r.block(&v.stages[0]), `
          空闲时延                      ━━━━━━━━━━━━━━━━━━━━
          时延 5.9 ms           采样 20 次
          抖动 2.8 ms           最低 4.6 ms           最高 13.7 ms
`},
		"finished stage": {r.block(&v.stages[1]), `
          下载 · 单线程 · 1 条连接      ━━━━━━━━━━━━━━━━━━━━
          速率 971.7 Mbps       传输 619.8 MB         用时 5.3 秒
          负载时延 14.6 ms      负载抖动 4.1 ms       负载最高 26.9 ms
`},
		"running stage": {r.block(&v.stages[2]), `
          下载 · 多线程 · 8 条连接      ━━━━━━━━────────────
          速率 801.2 Mbps       传输 300.0 MB         用时 3.0 秒
          负载时延 15.4 ms      负载抖动 1.8 ms       负载最高 20.0 ms
`},
		"stage not started": {r.block(&v.stages[3]), `
          上传 · 单线程 · 1 条连接      ────────────────────
          速率 -                传输 -                用时 -
          负载时延 -            负载抖动 -            负载最高 -
`},
		"narrow terminal": {renderer{width: 60}.block(&v.stages[2]), `
          下载 · 多线程 · 8 条连接      ━━━━━━━━───────────
          速率 801.2 Mbps  传输 300.0 MB    用时 3.0 秒
          负载时延 15.4 ms 负载抖动 1.8 ms  负载最高 20.0 ms
`},
	} {
		if tt.got != tt.want {
			t.Errorf("%s mismatch\n got:%s\nwant:%s", name, tt.got, tt.want)
		}
	}
}

// 区块不带 INFO、SUCCESS 之类的状态标签：标题与数据都与状态行的正文对齐，进行中与完成时的排版一致。
func TestRenderNoBadge(t *testing.T) {
	wideAmbiguous(t, false)
	v := newTestView(node, "ipv4", stages)
	r := renderer{width: 100}
	before := r.block(&v.stages[1])
	v.update(downOne)
	for name, block := range map[string]string{"pending": before, "finished": r.block(&v.stages[1])} {
		for _, word := range []string{"INFO", "SUCCESS", "WARNING", "ERROR"} {
			if strings.Contains(block, word) {
				t.Errorf("%s block contains the %s badge:\n%s", name, word, block)
			}
		}
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		for i, line := range lines {
			if !strings.HasPrefix(line, strings.Repeat(" ", bodyIndent)) || strings.HasPrefix(line, strings.Repeat(" ", bodyIndent+1)) {
				t.Errorf("%s: line %d does not start at column %d: %q", name, i, bodyIndent, line)
			}
		}
	}
	// 状态行的正文也从同一列开始，区块与它们上下对齐。
	for _, lv := range []level{levelInfo, levelSuccess, levelWarning, levelError} {
		if w := runewidth.StringWidth(lv.badge()); w != badgeWidth {
			t.Errorf("%q badge is %d wide, want %d", strings.TrimSpace(lv.printer.Prefix.Text), w, badgeWidth)
		}
	}
}

// 空闲时延刚开始时只有一个样本，算不出抖动；采样有失败时在第一行多一项失败率。
func TestRenderLatencyEdgeCases(t *testing.T) {
	wideAmbiguous(t, false)
	r := renderer{width: 100}
	v := newTestView(node, "ipv4", nil)
	v.update(taier.ProgressUpdate{Phase: taier.PhaseLatency, Fraction: 0.05, Latency: delay(1, 7, 7, 7, 0)})
	got := r.block(&v.stages[0])
	for _, want := range []string{"时延 7.0 ms", "采样 1 次", "抖动 -", "最低 7.0 ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("one sample: missing %q in\n%s", want, got)
		}
	}

	lat := delay(15, 8, 7, 12, 2)
	lat.Failed = 5
	v.update(taier.ProgressUpdate{Phase: taier.PhaseLatency, Fraction: 1, Final: true, Latency: lat})
	got = r.block(&v.stages[0])
	if !strings.Contains(got, "采样 20 次") || !strings.Contains(got, "失败率 25.0 %") {
		t.Errorf("with failures:\n%s", got)
	}

	// 一个样本都没有时全部显示 -，不出现 0.0。
	v = newTestView(node, "ipv4", nil)
	v.update(taier.ProgressUpdate{Phase: taier.PhaseLatency, Fraction: 0.1, Latency: taier.LatencyStats{Failed: 2}})
	if got := r.block(&v.stages[0]); strings.Contains(got, "0.0 ms") || !strings.Contains(got, "时延 -") || !strings.Contains(got, "采样 2 次") {
		t.Errorf("no successful samples:\n%s", got)
	}
}

func TestRenderHeader(t *testing.T) {
	wideAmbiguous(t, false)
	for _, tt := range []struct {
		name   string
		node   runner.NodeRef
		family string
		width  int
		want   string
	}{
		{"original style", node, "ipv4", 80, "\n  INFO    测试 电信 IPv4 济南电信 (58.56.118.126)\n"},
		{"ipv6 address without port", runner.NodeRef{Name: "济南电信", Operator: "电信", Address: "[240e:4e:4000::3]:65499"}, "ipv6", 80,
			"\n  INFO    测试 电信 IPv6 济南电信 (240e:4e:4000::3)\n"},
		{"unknown operator", runner.NodeRef{Name: "某节点", Address: "1.2.3.4:65499"}, "ipv4", 80, "\n  INFO    测试 其他 IPv4 某节点 (1.2.3.4)\n"},
		{"narrow terminal drops the address", node, "ipv4", 30, "\n  INFO    测试 电信 IPv4 济南电信\n"},
	} {
		v := newTestView(tt.node, tt.family, nil)
		if got := (renderer{width: tt.width}).header(v); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

// 进度事件按(方向, 连接数)找到对应阶段；事件里有负载时延就立即展示；没有对应阶段的事件被忽略。
func TestStageUpdate(t *testing.T) {
	v := newTestView(node, "ipv4", stages)
	if len(v.stages) != 5 || v.stages[0].phase != taier.PhaseLatency {
		t.Fatalf("stages = %+v", v.stages)
	}
	if i := v.update(idleDone); i != 0 || v.stages[0].state != stateDone || v.stages[0].delay.samples != 20 {
		t.Errorf("latency final: index %d, %+v", i, v.stages[0])
	}
	// 预热期间：速率、传输量与负载时延都立即展示；事件还没有时延样本时才显示 -。
	warm := taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 1, Fraction: 0.1, Mbps: 100, Bytes: 5_000_000, Total: 500 * ms,
		Warmup: true, Latency: delay(2, 22.5, 20, 25, 5)}
	if i := v.update(warm); i != 1 || v.stages[1].state != stateRunning || v.stages[1].bytes != 5_000_000 || v.stages[1].delay.samples != 2 {
		t.Errorf("warmup: index %d, %+v", i, v.stages[1])
	}
	if got := (renderer{width: 100}).block(&v.stages[1]); !strings.Contains(got, "负载时延 22.5 ms") || !strings.Contains(got, "速率 100.0 Mbps") {
		t.Errorf("warmup block:\n%s", got)
	}
	noSample := taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 1, Fraction: 0.05, Mbps: 10, Bytes: 100_000, Total: 250 * ms}
	if v.update(noSample); !strings.Contains((renderer{width: 100}).block(&v.stages[1]), "负载时延 -") {
		t.Error("an update without latency samples should show -")
	}
	if i := v.update(downOne); i != 1 || v.stages[1].state != stateDone || v.stages[1].delay.medianMs != 14.6 || v.stages[1].total != 5300*ms {
		t.Errorf("single final: index %d, %+v", i, v.stages[1])
	}
	// 多线程阶段与单线程阶段互不影响。
	if i := v.update(downMid); i != 2 || v.stages[1].mbps != 971.7 || v.stages[2].mbps != 801.2 || v.stages[3].state != statePending {
		t.Errorf("multi: index %d, %+v", i, v.stages)
	}
	// 没有对应阶段(如 --no-upload 跳过的上传)的事件被忽略。
	only := newTestView(node, "ipv4", []taier.Stage{{Phase: taier.PhaseDownload, Threads: 8}})
	if i := only.update(upOne); i != -1 {
		t.Errorf("update for a stage that is not planned returned %d", i)
	}
}

// 无论终端宽窄、模糊宽度字符占一格还是两格，即使数值大到极端，区块的每一行也不能超过终端宽度。
func TestRenderFitsWidth(t *testing.T) {
	v := newTestView(node, "ipv6", stages)
	v.update(taier.ProgressUpdate{Phase: taier.PhaseLatency, Fraction: 1, Final: true, Latency: delay(20, 123.4, 100.5, 999.9, 99.9)})
	v.update(taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 1, Fraction: 0.37, Mbps: 12345.6,
		Bytes: 12_345_678_901, Total: 100 * 1000 * ms, Latency: delay(40, 1234.5, 1, 9999.9, 999.9)})

	for _, wide := range []bool{false, true} {
		for _, width := range []int{40, 50, 60, 70, 80, 120} {
			wideAmbiguous(t, wide)
			r := renderer{width: width}
			for i := range v.stages {
				for line := range strings.SplitSeq(r.block(&v.stages[i]), "\n") {
					if w := runewidth.StringWidth(line); w > width {
						t.Errorf("wide=%v width=%d stage %d: line is %d wide: %q", wide, width, i, w, line)
					}
				}
			}
		}
	}
}

// 数据行的各项按列对齐：上下两行里第 n 项的起点相同。
func TestRenderColumnsAligned(t *testing.T) {
	v := newTestView(node, "ipv4", stages)
	v.update(idleDone)
	v.update(downOne)

	startOf := func(line, label string) int {
		before, _, ok := strings.Cut(line, label)
		if !ok {
			return -1
		}
		return runewidth.StringWidth(before)
	}
	for _, wide := range []bool{false, true} {
		for _, width := range []int{50, 60, 70, 80, 120} {
			wideAmbiguous(t, wide)
			r := renderer{width: width}
			for i, pairs := range [][][2]string{
				{{"采样", "最低"}, {"", ""}}, // 空闲时延：第一行 时延、采样，第二行 抖动、最低、最高
				{{"传输", "负载抖动"}, {"用时", "负载最高"}},
			} {
				lines := strings.Split(strings.TrimSpace(r.block(&v.stages[i])), "\n")
				for _, pair := range pairs {
					if pair[0] == "" {
						continue
					}
					a, b := startOf(lines[1], pair[0]), startOf(lines[2], pair[1])
					if a < 0 && b < 0 { // 终端太窄，该项在两行里都被省略
						continue
					}
					if a != b {
						t.Errorf("wide=%v width=%d stage %d: %q starts at %d but %q at %d\n%s", wide, width, i, pair[0], a, pair[1], b, r.block(&v.stages[i]))
					}
				}
			}
		}
	}
}

// 阶段的标题与进度条按类型固定颜色(空闲时延绿、下载青、上传洋红)，数据用白色，不随状态变化；
// 同一个阶段在 INFO、SUCCESS、WARNING 下渲染，只有最前面的状态标签不同。
func TestRenderFixedColors(t *testing.T) {
	pterm.EnableColor()
	defer pterm.DisableColor()
	v := newTestView(node, "ipv4", []taier.Stage{{Phase: taier.PhaseDownload, Threads: 1}, {Phase: taier.PhaseUpload, Threads: 1}})
	v.update(idleDone)
	v.update(downOne)
	v.update(upOne)
	r := renderer{width: 100}

	const (
		green, cyan, magenta = "\x1b[32;1m", "\x1b[36;1m", "\x1b[35;1m"
		white                = "\x1b[97m"
	)
	for i, kind := range []struct{ name, color string }{{"空闲时延", green}, {"下载", cyan}, {"上传", magenta}} {
		block := r.block(&v.stages[i])
		if !strings.Contains(block, kind.color+kind.name) {
			t.Errorf("%s title is not %q:\n%q", kind.name, kind.color, block)
		}
		if !strings.Contains(block, kind.color+"━") {
			t.Errorf("%s progress bar is not %q:\n%q", kind.name, kind.color, block)
		}
		if n := strings.Count(block, white); n < 5 {
			t.Errorf("%s: only %d white values, want the data in white:\n%q", kind.name, n, block)
		}
	}
	if got := r.block(&v.stages[1]); !strings.Contains(got, white+"971.7") || !strings.Contains(got, white+"619.8") {
		t.Errorf("speed and size are not white:\n%q", got)
	}

	// 进行中与完成时颜色一致：同一阶段的标题与进度条颜色不随状态变化。
	running := newTestView(node, "ipv4", []taier.Stage{{Phase: taier.PhaseDownload, Threads: 1}})
	running.update(taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 1, Fraction: 0.5, Mbps: 500, Bytes: 1_000_000, Total: ms})
	if got := r.block(&running.stages[1]); !strings.Contains(got, cyan+"下载") || !strings.Contains(got, cyan+"━") {
		t.Errorf("running stage lost its fixed color:\n%q", got)
	}
}

// 带颜色的渲染去掉转义序列后，必须与无色渲染逐字节相同；否则说明某处把转义序列算进了宽度，
// 在彩色终端里各列会错位(关闭颜色的测试看不出这类问题)。
func TestRenderColorDoesNotAffectLayout(t *testing.T) {
	v := newTestView(node, "ipv4", stages)
	v.update(idleDone)
	v.update(downOne)
	v.update(downMid)
	lat := delay(15, 8, 7, 12, 2)
	lat.Failed = 5
	failed := newTestView(node, "ipv4", nil)
	failed.update(taier.ProgressUpdate{Phase: taier.PhaseLatency, Fraction: 1, Final: true, Latency: lat})

	for _, wide := range []bool{false, true} {
		for _, width := range []int{40, 60, 80, 100} {
			wideAmbiguous(t, wide)
			r := renderer{width: width}
			render := func() string {
				var b strings.Builder
				b.WriteString(r.header(v))
				for i := range v.stages {
					b.WriteString(r.block(&v.stages[i]))
				}
				b.WriteString(r.block(&failed.stages[0]))
				b.WriteString(levelError.line("青岛电信  服务繁忙"))
				return b.String()
			}
			want := render()
			pterm.EnableColor()
			colored := render()
			pterm.DisableColor()
			if !strings.Contains(colored, "\x1b[") {
				t.Fatal("color is not enabled in this test")
			}
			if plain := pterm.RemoveColorFromString(colored); plain != want {
				t.Errorf("wide=%v width=%d: colored layout differs\n got:%s\nwant:%s", wide, width, plain, want)
			}
		}
	}
}

func TestRenderBar(t *testing.T) {
	wideAmbiguous(t, false)
	r := renderer{width: 100}
	for _, tt := range []struct {
		fraction float64
		want     string
	}{
		{0, "────────────────────"},
		{0.5, "━━━━━━━━━━──────────"},
		{0.04, "━───────────────────"},
		{1, "━━━━━━━━━━━━━━━━━━━━"},
		{1.7, "━━━━━━━━━━━━━━━━━━━━"}, // 越界按满格
		{-1, "────────────────────"},
	} {
		if got := r.bar(tt.fraction, pterm.NewStyle()); got != tt.want {
			t.Errorf("bar(%v) = %q, want %q", tt.fraction, got, tt.want)
		}
	}
	// 进度条在标题右侧，80 列放得下满长的进度条；窄终端缩短，太窄时不显示。
	if got := (renderer{width: 80}).barCells(); got != barLen {
		t.Errorf("barCells at 80 columns = %d, want %d", got, barLen)
	}
	if got := (renderer{width: 60}).barCells(); got < minBarLen || got >= barLen {
		t.Errorf("barCells at 60 columns = %d, want shortened", got)
	}
	narrow := renderer{width: 40}
	if narrow.barCells() != 0 || narrow.bar(0.5, pterm.NewStyle()) != "" {
		t.Errorf("narrow bar = %q", narrow.bar(0.5, pterm.NewStyle()))
	}
	v := newTestView(node, "ipv4", stages)
	v.update(downOne)
	if got := narrow.block(&v.stages[1]); strings.Contains(got, "━") || !strings.Contains(got, "速率 971.7 Mbps") {
		t.Errorf("narrow block:\n%s", got)
	}
	// 模糊宽度字符占两格时，进度条按单元格数计算，仍放得下。
	wideAmbiguous(t, true)
	if got := runewidth.StringWidth(r.bar(0.5, pterm.NewStyle())); got > 100-bodyIndent-titleWidth-barGap {
		t.Errorf("wide bar occupies %d cells", got)
	}
}

func TestColWidth(t *testing.T) {
	for avail, want := range map[int]int{200: maxColWidth, 70: 22, 62: 22, 56: 19, 50: 17, 20: minColWidth} {
		if got := colWidth(avail); got != want {
			t.Errorf("colWidth(%d) = %d, want %d", avail, got, want)
		}
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"119.164.225.30:65499":    "119.164.225.30",
		"[240e:4e:4000::3]:65499": "240e:4e:4000::3",
		"example.com:65499":       "example.com",
		"119.164.225.30":          "119.164.225.30",
		"":                        "",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPadRight(t *testing.T) {
	for in, want := range map[string]string{"ab": "ab    ", "时延": "时延  ", "空闲时延": "空闲时延", "toolong": "toolong"} {
		if got := padRight(in, 6); in != "空闲时延" && got != want {
			t.Errorf("padRight(%q) = %q, want %q", in, got, want)
		}
	}
	if got := padRight("空闲时延", 8); got != "空闲时延" {
		t.Errorf("padRight(空闲时延, 8) = %q", got)
	}
}

func TestSplitBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 999: "999 B", 1500: "1.5 KB", 722_800_000: "722.8 MB", 1_800_000_000: "1.8 GB", 2_500_000_000_000: "2.5 TB",
	} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
	if v, unit := splitBytes(722_800_000); v != 722.8 || unit != "MB" {
		t.Errorf("splitBytes = %v %q", v, unit)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                        "0 秒",
		47600 * time.Millisecond: "48 秒",
		65 * time.Second:         "1 分 05 秒",
		600 * time.Second:        "10 分 00 秒",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
