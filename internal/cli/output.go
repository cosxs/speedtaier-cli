package cli

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/pterm/pterm"
	"golang.org/x/term"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// ui 是终端界面，实现 runner.Reporter：过程信息写 stderr，结果表格写 stdout。
type ui struct {
	showProgress bool
	surveyOut    io.Writer // list 命令的节点表是结果，写 stdout；测速时只是过程信息，写 stderr

	mu      sync.Mutex
	spinner *pterm.SpinnerPrinter
}

var _ runner.Reporter = (*ui)(nil)

func newUI(noProgress, surveyOnly bool) *ui {
	pterm.SetDefaultOutput(os.Stderr)
	// 日志在 stderr、表格可能在 stdout，任一端不是终端都关闭颜色，避免转义序列写进文件或管道。
	if os.Getenv("NO_COLOR") != "" || !isTerminal(os.Stdout) || !isTerminal(os.Stderr) {
		pterm.DisableColor()
	}
	u := &ui{showProgress: !noProgress && isTerminal(os.Stderr), surveyOut: os.Stderr}
	if surveyOnly {
		u.surveyOut = os.Stdout
	}
	return u
}

// isTerminal 不能用 ModeCharDevice 判断：/dev/null 也是字符设备。
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func (u *ui) Info(msg string) { pterm.Info.Println(msg) }

func (u *ui) Warn(w runner.Warning) {
	if w.Level == runner.LevelWarn {
		pterm.Warning.Println(w.Message)
	} else {
		pterm.Info.Println(w.Message)
	}
}

// Step 在进度可见时显示 spinner。
func (u *ui) Step(msg string) func() {
	if !u.showProgress {
		return func() {}
	}
	s, err := pterm.DefaultSpinner.WithRemoveWhenDone().Start(msg)
	if err != nil {
		return func() {}
	}
	return func() { _ = s.Stop() }
}

func (u *ui) Testing(n runner.NodeRef, family string) {
	pterm.Info.Printfln("测试 %s %s %s (%s)", displayOperator(n.Operator), familyLabel(family), n.Name, n.Address)
}

func (u *ui) Progress(p taier.ProgressUpdate) {
	if !u.showProgress {
		return
	}
	text := fmt.Sprintf("%s %.1f Mbps", p.Phase, p.Mbps)
	switch {
	case p.Final && p.Stable:
		text += fmt.Sprintf(" · %.1f 秒后吞吐稳定", p.Elapsed.Seconds())
	case p.Final:
		text += fmt.Sprintf(" · 计量 %.1f 秒", p.Elapsed.Seconds())
	case p.Warmup:
		text += " (预热中，不计入结果)"
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	if u.spinner == nil {
		if p.Final {
			pterm.Success.Println(text)
			return
		}
		spinner, err := pterm.DefaultSpinner.Start(p.Phase + " ...")
		if err != nil {
			return
		}
		u.spinner = spinner
	}
	if p.Final {
		u.spinner.Success(text)
		u.spinner = nil
		return
	}
	u.spinner.UpdateText(text)
}

// Survey 列出省份内全部节点及连通性(runner 已按城市、运营商排好序)。
func (u *ui) Survey(res *runner.Result) {
	data := make([][]string, 0, len(res.Candidates)+1)
	data = append(data, []string{"城市", "运营商", "节点", "IPv4", "IPv6"})
	withV6 := 0
	for _, c := range res.Candidates {
		if c.IPv6.Status != runner.ReachNoAddress {
			withV6++
		}
		data = append(data, []string{c.City, displayOperator(c.Operator), c.Name, formatReach(c.IPv4), formatReach(c.IPv6)})
	}
	_ = pterm.DefaultTable.WithHasHeader().WithData(data).WithWriter(u.surveyOut).Render()

	local := "本机 IPv6 可用"
	if !res.Network.IPv6 {
		local = "本机无 IPv6 路由，未探测 IPv6"
	}
	pterm.Info.Printfln("%s 共 %d 个节点，其中 %d 个提供 IPv6 · %s", res.Province, len(res.Candidates), withV6, local)
}

// finish 输出最终结果：致命错误，或测速结果表与流量统计。
func (u *ui) finish(res *runner.Result) {
	if res.Error != "" {
		pterm.Error.Println(res.Error)
	}
	if len(res.Tests) == 0 {
		return
	}
	data := make([][]string, 0, len(res.Tests)+1)
	data = append(data, []string{"运营商", "协议", "节点", "下载", "上传", "空载时延", "抖动", "失败率", "负载时延 ↓/↑"})
	for _, t := range res.Tests {
		op, fam := displayOperator(t.Operator), familyLabel(t.Family)
		if t.Status != runner.TestOK {
			name := "-"
			if n := len(t.Attempts); n > 0 {
				name = t.Attempts[n-1].Node.Name
			}
			data = append(data, []string{op, fam, name, "失败: " + t.Error, "", "", "", "", ""})
			continue
		}
		idle := t.IdleLatency
		data = append(data, []string{
			op, fam, t.Node.Name,
			formatMbps(t.Download), formatMbps(t.Upload),
			formatLatency(idle, func(l *runner.Latency) float64 { return l.MedianMs }),
			formatLatency(idle, func(l *runner.Latency) float64 { return l.JitterMs }),
			formatFailRate(idle),
			formatLoaded(t.Download) + " / " + formatLoaded(t.Upload),
		})
	}
	_ = pterm.DefaultTable.WithHasHeader().WithData(data).WithWriter(os.Stdout).Render()
	pterm.Info.Printfln("时延为中位数；抖动为相邻样本差的平均；失败率为时延请求失败的比例(非丢包率)。本次消耗流量 %s",
		formatBytes(res.DataUsageBytes))
}

func formatReach(r runner.Reach) string {
	switch r.Status {
	case runner.ReachOK:
		return pterm.FgGreen.Sprintf("%.1f ms", *r.RTTMs)
	case runner.ReachUnreachable:
		return pterm.FgRed.Sprint("不可达")
	case runner.ReachSkipped:
		return pterm.FgGray.Sprint("未探测")
	default:
		return pterm.FgGray.Sprint("-")
	}
}

func formatMbps(t *runner.Transfer) string {
	if t == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f Mbps", t.Mbps)
}

// formatLatency 格式化时延统计中的某一项；没有样本时显示 "-"。
func formatLatency(l *runner.Latency, field func(*runner.Latency) float64) string {
	if l == nil || l.Samples == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f ms", field(l))
}

func formatFailRate(l *runner.Latency) string {
	if l == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", l.FailRatePct)
}

func formatLoaded(t *runner.Transfer) string {
	if t == nil {
		return "-"
	}
	return formatLatency(t.LoadedLatency, func(l *runner.Latency) float64 { return l.MedianMs })
}

func formatBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, suffix := float64(n), "KMGT"
	i := -1
	for v >= unit && i < len(suffix)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %cB", v, suffix[i])
}

func displayOperator(op string) string {
	if op == "" {
		return "其他"
	}
	return op
}

func familyLabel(family string) string {
	if family == "ipv6" {
		return "IPv6"
	}
	return "IPv4"
}
