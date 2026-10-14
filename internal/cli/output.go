package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pterm/pterm"
	"golang.org/x/term"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// ui 是终端界面，实现 runner.Reporter：过程信息(进度、提示)写 stderr，结果写 stdout。
//
// 测速按阶段逐个展示：每个节点先输出一行标题，然后依次是空闲时延、下载、上传的各个阶段。
// 当前阶段在 stderr 原地刷新(进度条、速率、负载时延实时更新)，完成后清除，把定格的区块写到 stdout，
// 再开始下一个阶段。被中断时，已经测到的部分同样定格输出；还没开始的阶段不显示。
// Reporter 的方法都在同一个 goroutine 中调用，实时视图在收到进度事件时刷新，无需加锁。
type ui struct {
	showProgress bool
	showSurvey   bool      // 节点表只在 list 命令中输出，测速时不显示
	out          io.Writer // 结果输出，默认 stdout
	msg          io.Writer // 提示、警告与错误，默认 stderr

	cur    *testView // 正在测试的节点；未开始或已结束时为 nil
	active int       // 正在进行的阶段在 cur.stages 中的下标
	area   *pterm.AreaPrinter
}

var _ runner.Reporter = (*ui)(nil)

func newUI(noProgress, surveyOnly bool) *ui {
	pterm.SetDefaultOutput(os.Stderr)
	// 日志在 stderr、结果在 stdout，任一端不是终端都关闭颜色，避免转义序列写进文件或管道。
	if os.Getenv("NO_COLOR") != "" || !isTerminal(os.Stdout) || !isTerminal(os.Stderr) {
		pterm.DisableColor()
	}
	return &ui{showProgress: !noProgress && isTerminal(os.Stderr), showSurvey: surveyOnly, out: os.Stdout, msg: os.Stderr}
}

// isTerminal 不能用 ModeCharDevice 判断：/dev/null 也是字符设备。
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// terminalSize 返回 f 的终端宽度与高度；不是终端时宽度按 80 列、高度为 0(未知)。
func terminalSize(f *os.File) (width, height int) {
	if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
		return w, h
	}
	return 80, 0
}

// minLiveRows 是实时区域所需的最小终端高度：一个阶段的区块约 9 行，加上提示行。
const minLiveRows = 12

// liveRenderer 返回实时视图的渲染器；终端太矮放不下实时区域时 ok 为 false(放不下的区域无法原地刷新)。
func (u *ui) liveRenderer() (r renderer, ok bool) {
	w, h := terminalSize(os.Stderr)
	return renderer{width: w}, h == 0 || h >= minLiveRows
}

// resultRenderer 返回输出结果的渲染器，宽度取自结果输出端。
func (u *ui) resultRenderer() renderer {
	w := 80
	if f, ok := u.out.(*os.File); ok {
		w, _ = terminalSize(f)
	}
	return renderer{width: w}
}

// banner 输出程序名与版本。
func (u *ui) banner(version string) {
	if version == "dev" {
		version = "开发版"
	}
	fmt.Fprintf(os.Stderr, "\n  %s %s\n\n", styleAccent.Sprint("SpeedTaier"), styleDim.Sprint(version))
}

func (u *ui) Client(c runner.Client) {
	location := strings.Join(nonEmpty(c.Country, c.Province, c.City), " ")
	fmt.Fprint(u.msg, levelInfo.line("本机出口 "+strings.Join(nonEmpty(c.IP, location, c.Operator), partSep)))
}

// Warn 输出提示或警告。被中断时清除进行中的阶段(已测完的阶段留在屏幕上)，
// 并在"已中断"上方空一行，与上面的内容隔开。
func (u *ui) Warn(w runner.Warning) {
	u.stopLive()
	if w.Code == runner.CodeInterrupted {
		fmt.Fprint(u.msg, "\n")
	}
	lv := levelInfo
	if w.Level == runner.LevelWarn {
		lv = levelWarning
	}
	fmt.Fprint(u.msg, lv.line(w.Message))
}

// Step 在进度可见时显示 spinner。
func (u *ui) Step(msg string) func() {
	if !u.showProgress {
		return func() {}
	}
	s, err := pterm.DefaultSpinner.
		WithSequence(spinnerFrames...).
		WithStyle(pterm.NewStyle(pterm.FgCyan)).
		WithMessageStyle(styleDim).
		WithRemoveWhenDone().
		Start(msg)
	if err != nil {
		return func() {}
	}
	return func() { _ = s.Stop() }
}

func (u *ui) Testing(n runner.NodeRef, family string, stages []taier.Stage) {
	u.stopLive()
	u.cur = newTestView(n, family, stages)
	fmt.Fprint(u.out, u.resultRenderer().header(u.cur))
	u.begin(0)
}

// Progress 更新当前阶段的状态并刷新实时视图；阶段完成时定格输出，并开始下一个阶段。
func (u *ui) Progress(p taier.ProgressUpdate) {
	if u.cur == nil {
		return
	}
	i := u.cur.update(p)
	if i < 0 {
		return
	}
	if i != u.active { // 事件跳过了前面的阶段
		u.stopLive()
		u.begin(i)
	}
	if p.Final {
		u.freeze(i)
		u.begin(i + 1)
		return
	}
	if u.area != nil {
		r, _ := u.liveRenderer()
		u.area.Update(r.block(&u.cur.stages[i]))
	}
}

// Tested 结束一个节点：各阶段已经逐个输出；测速失败时补一行失败原因。
func (u *ui) Tested(t runner.Test) {
	u.stopLive()
	u.cur = nil
	if t.Status == runner.TestOK {
		return
	}
	// 此前失败的节点已经通过警告说明过，这里只给出最后一个节点的原因。
	reason, name := t.Error, ""
	if n := len(t.Attempts); n > 0 {
		name, reason = t.Attempts[n-1].Node.Name, t.Attempts[n-1].Error
	}
	fmt.Fprint(u.out, "\n"+levelError.line(strings.TrimSpace(name+"  "+reason)))
}

// begin 开始显示第 i 个阶段：进度可见时在 stderr 开一个实时区域，显示该阶段的区块。
func (u *ui) begin(i int) {
	u.active = i
	if u.cur == nil || i >= len(u.cur.stages) || !u.showProgress {
		return
	}
	r, ok := u.liveRenderer()
	if !ok {
		return
	}
	area := pterm.DefaultArea.WithRemoveWhenDone()
	area.SetWriter(os.Stderr)
	_, _ = area.Start(r.block(&u.cur.stages[i]))
	u.area = area
}

// freeze 清除实时区域，把已完成的第 i 个阶段定格输出到结果。
func (u *ui) freeze(i int) {
	u.stopLive()
	fmt.Fprint(u.out, u.resultRenderer().block(&u.cur.stages[i]))
}

// stopLive 清除实时区域。
func (u *ui) stopLive() {
	if u.area != nil {
		_ = u.area.Stop()
		u.area = nil
	}
}

// Survey 列出省份内全部节点及连通性(runner 已按城市、运营商排好序)。
func (u *ui) Survey(res *runner.Result) {
	if !u.showSurvey {
		return
	}
	data := make([][]string, 0, len(res.Candidates)+1)
	data = append(data, []string{"城市", "运营商", "节点", "IPv4", "IPv6"})
	withV6 := 0
	for _, c := range res.Candidates {
		if c.IPv6.Status != runner.ReachNoAddress {
			withV6++
		}
		data = append(data, []string{c.City, displayOperator(c.Operator), c.Name, formatReach(c.IPv4), formatReach(c.IPv6)})
	}
	_ = pterm.DefaultTable.WithHasHeader().WithData(data).WithWriter(os.Stdout).Render()

	local := "本机 IPv6 可用"
	if !res.Network.IPv6 {
		local = "本机无 IPv6 路由，未探测 IPv6"
	}
	fmt.Fprint(os.Stdout, levelInfo.line(fmt.Sprintf("%s 共 %d 个节点，其中 %d 个提供 IPv6 · %s",
		res.Province, len(res.Candidates), withV6, local)))
}

// finish 收尾：清除未结束的实时视图，输出致命错误或流量统计。
func (u *ui) finish(res *runner.Result) {
	u.stopLive()
	if res.Error != "" {
		fmt.Fprint(u.msg, levelError.line(res.Error))
	}
	if res.DataUsageBytes > 0 {
		fmt.Fprint(u.out, "\n"+levelInfo.line(fmt.Sprintf("消耗流量 %s%s总用时 %s",
			formatBytes(res.DataUsageBytes), partSep, formatDuration(time.Duration(res.DurationMs)*time.Millisecond)))+"\n")
	}
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

// formatDuration 把时长格式化为 "48 秒" 或 "1 分 05 秒"。
func formatDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s < 60 {
		return fmt.Sprintf("%d 秒", s)
	}
	return fmt.Sprintf("%d 分 %02d 秒", s/60, s%60)
}

// splitBytes 把字节数拆成数值与单位(十进制，1 MB = 10^6 字节)，如 722_800_000 → 722.8、"MB"。
func splitBytes(n int64) (float64, string) {
	const unit = 1000
	if n < unit {
		return float64(n), "B"
	}
	v, suffix := float64(n), "KMGT"
	i := -1
	for v >= unit && i < len(suffix)-1 {
		v /= unit
		i++
	}
	return v, string(suffix[i]) + "B"
}

func formatBytes(n int64) string {
	v, unit := splitBytes(n)
	if unit == "B" {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", v, unit)
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
