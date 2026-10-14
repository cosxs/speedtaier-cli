package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// newTestUI 返回把结果写入 out、把提示与警告写入 msg 的界面，不显示实时进度(实时区域写 stderr，这里只检查定格的结果)。
func newTestUI() (u *ui, out, msg *bytes.Buffer) {
	out, msg = new(bytes.Buffer), new(bytes.Buffer)
	return &ui{out: out, msg: msg}, out, msg
}

// expected 按顺序渲染给定事件完成后各阶段区块，作为期望输出：标题加上每个有数据的阶段。
func expected(events []taier.ProgressUpdate, upTo int) string {
	v := newTestView(node, "ipv4", stages)
	r := renderer{width: 80}
	out := r.header(v)
	for _, e := range events {
		v.update(e)
	}
	for i := range upTo {
		out += r.block(&v.stages[i])
	}
	return out
}

// 测速的输出是逐步增长的：每个阶段完成时输出它的区块，进行中的进度不写入结果，没开始的阶段不出现。
func TestUIProgressiveOutput(t *testing.T) {
	u, buf, _ := newTestUI()
	u.Testing(node, "ipv4", stages)
	if got, want := buf.String(), (renderer{width: 80}).header(newTestView(node, "ipv4", stages)); got != want {
		t.Fatalf("after Testing: got %q, want the header only %q", got, want)
	}

	downEight := downAll
	events := []taier.ProgressUpdate{idleDone, downOne, downEight, upOne}
	upEight := taier.ProgressUpdate{Phase: taier.PhaseUpload, Threads: 8, Fraction: 1, Final: true, Mbps: 113.6,
		Bytes: 88_700_000, Total: 6500 * 1000 * 1000, Latency: delay(30, 8.0, 7, 125.8, 28.9)}
	events = append(events, upEight)

	steps := 0
	for i, e := range events {
		partial := e
		partial.Final, partial.Fraction = false, 0.5
		before := buf.Len()
		u.Progress(partial)
		if buf.Len() != before {
			t.Errorf("event %d: partial progress wrote to the result: %q", i, buf.String()[before:])
		}
		u.Progress(e)
		steps++
		if got, want := buf.String(), expected(events[:i+1], i+1); got != want {
			t.Fatalf("after stage %d completed\n got:%s\nwant:%s", i, got, want)
		}
	}

	u.Tested(runner.Test{Status: runner.TestOK})
	u.finish(&runner.Result{DataUsageBytes: 1_500_000_000, DurationMs: 26_400})
	out := buf.String()
	if !strings.HasSuffix(out, "\n"+levelInfo.line("消耗流量 1.5 GB · 总用时 26 秒")+"\n") {
		t.Errorf("footer missing:\n%s", out)
	}
	// 区块之间恰好空一行，不出现连续的空行。
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("consecutive blank lines in the output:\n%s", out)
	}
	for _, want := range []string{"空闲时延", "下载 · 单线程", "下载 · 多线程", "上传 · 单线程", "上传 · 多线程"} {
		if strings.Count(out, want) != 1 {
			t.Errorf("%q appears %d times, want once", want, strings.Count(out, want))
		}
	}
	if u.cur != nil {
		t.Error("node view not cleared after Tested")
	}
}

// 被中断时，已测完的阶段留在屏幕上，进行中和没开始的阶段都不显示；"已中断"与上面的内容之间空一行。
func TestUIInterrupted(t *testing.T) {
	interrupt := runner.Warning{Code: runner.CodeInterrupted, Level: runner.LevelWarn, Message: "已中断"}
	wantMsg := "\n" + levelWarning.line("已中断")

	t.Run("during a transfer", func(t *testing.T) {
		u, out, msg := newTestUI()
		u.Testing(node, "ipv4", stages)
		u.Progress(idleDone)
		u.Progress(downOne)
		u.Progress(downMid)
		u.Warn(interrupt)
		// 只有已完成的两个阶段(空闲时延、下载单线程)，进行中的多线程下载不显示。
		if got, want := out.String(), expected([]taier.ProgressUpdate{idleDone, downOne}, 2); got != want {
			t.Fatalf("got:%s\nwant:%s", got, want)
		}
		if strings.Contains(out.String(), "多线程") || strings.Contains(out.String(), "WARNING") {
			t.Errorf("the interrupted stage was printed:\n%s", out)
		}
		if msg.String() != wantMsg {
			t.Errorf("message = %q, want %q", msg, wantMsg)
		}
	})

	t.Run("during latency sampling", func(t *testing.T) {
		u, out, msg := newTestUI()
		u.Testing(node, "ipv4", stages)
		u.Progress(taier.ProgressUpdate{Phase: taier.PhaseLatency, Fraction: 0.4, Latency: delay(8, 7.5, 5.5, 15.7, 3.3)})
		u.Warn(interrupt)
		if got, want := out.String(), (renderer{width: 80}).header(newTestView(node, "ipv4", stages)); got != want {
			t.Errorf("got %q, want only the header %q", got, want)
		}
		if msg.String() != wantMsg {
			t.Errorf("message = %q, want %q", msg, wantMsg)
		}
	})

	t.Run("between nodes", func(t *testing.T) {
		u, out, msg := newTestUI()
		u.Warn(interrupt)
		if out.Len() != 0 || msg.String() != wantMsg {
			t.Errorf("out = %q, message = %q", out, msg)
		}
	})

	t.Run("other warnings print nothing extra", func(t *testing.T) {
		u, out, msg := newTestUI()
		u.Testing(node, "ipv4", stages)
		u.Progress(idleDone)
		u.Progress(downMid)
		before := out.String()
		u.Warn(runner.Warning{Code: runner.CodeNodeFallback, Level: runner.LevelInfo, Message: "改测下一节点"})
		if out.String() != before {
			t.Errorf("a fallback warning printed the unfinished stage: %q", out.String()[len(before):])
		}
		if got, want := msg.String(), levelInfo.line("改测下一节点"); got != want {
			t.Errorf("message = %q, want %q (no blank line for ordinary warnings)", got, want)
		}
	})
}

// 节点测速失败改测下一个时，前一个节点已经完成的阶段保留，各节点各有标题。
func TestUIFallbackAndFailure(t *testing.T) {
	u, buf, _ := newTestUI()
	other := runner.NodeRef{Name: "青岛电信", Operator: "电信", Address: "1.2.3.4:65499"}
	u.Testing(node, "ipv4", stages)
	u.Progress(idleDone)
	u.Warn(runner.Warning{Code: runner.CodeNodeFallback, Level: runner.LevelInfo, Message: "济南电信 IPv4 测速失败，改测下一节点"})
	u.Testing(other, "ipv4", stages)
	u.Progress(idleDone)

	out := buf.String()
	if strings.Count(out, "测试 ") != 2 || strings.Count(out, "空闲时延") != 2 ||
		strings.Index(out, "济南电信") > strings.Index(out, "青岛电信") {
		t.Errorf("fallback output:\n%s", out)
	}

	// 全部节点失败：只补最后一个节点的原因(之前的已通过警告说明)。
	u.Tested(runner.Test{Status: runner.TestFailed, Error: "服务繁忙", Attempts: []runner.Attempt{
		{Node: node, Error: "端口不可达"}, {Node: other, Error: "服务繁忙"},
	}})
	if tail := out[len(out):]; tail != "" {
		t.Fatal("unreachable")
	}
	if got, want := buf.String()[len(out):], "\n"+levelError.line("青岛电信  服务繁忙"); got != want {
		t.Errorf("failure line = %q", got)
	}

	u2, buf2, _ := newTestUI()
	u2.Testing(node, "ipv4", stages)
	u2.Tested(runner.Test{Status: runner.TestFailed, Error: "没有可达节点"})
	if !strings.HasSuffix(buf2.String(), "\n"+levelError.line("没有可达节点")) {
		t.Errorf("failure without attempts: %q", buf2.String())
	}
}

// 没有对应阶段的事件(如已跳过的方向)不产生输出。
func TestUIIgnoresUnplannedStages(t *testing.T) {
	u, buf, _ := newTestUI()
	u.Testing(node, "ipv4", []taier.Stage{{Phase: taier.PhaseDownload, Threads: 8}})
	before := buf.String()
	u.Progress(upOne)
	if buf.String() != before {
		t.Errorf("event for an unplanned stage wrote %q", buf.String()[len(before):])
	}
	u.Progress(taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 1, Final: true})
	if buf.String() != before {
		t.Errorf("event for a stage with another thread count wrote output")
	}
	var idle ui
	idle.Progress(upOne) // 没有节点在测速时不应崩溃
}
