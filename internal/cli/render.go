package cli

import (
	"fmt"
	"math"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/pterm/pterm"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// 配色：次要信息与数据标签用灰色，数据用白色；阶段标题与进度条按类型固定颜色(空闲时延绿、下载青、上传洋红)；
// 状态沿用 pterm 的 INFO、SUCCESS、WARNING、ERROR 彩色标签。关闭颜色时 Sprint 返回纯文本。
var (
	styleDim    = pterm.NewStyle(pterm.FgGray)
	styleAccent = pterm.NewStyle(pterm.FgCyan, pterm.Bold)
	styleValue  = pterm.NewStyle(pterm.FgLightWhite) // 数据一律用白色

	// 阶段的标题与进度条按类型固定颜色，不随状态变化；状态只体现在标签上。
	styleLatency = pterm.NewStyle(pterm.FgGreen, pterm.Bold)
	styleDown    = pterm.NewStyle(pterm.FgCyan, pterm.Bold)
	styleUp      = pterm.NewStyle(pterm.FgMagenta, pterm.Bold)
)

// level 是消息的状态级别，决定标签与文字的颜色。
type level struct{ printer pterm.PrefixPrinter }

var (
	levelInfo    = level{pterm.Info}
	levelSuccess = level{pterm.Success}
	levelWarning = level{pterm.Warning}
	levelError   = level{pterm.Error}
)

// 标签宽度：pterm 的标签文字统一为 7 格，两侧各一个空格；正文与续行都从标签后空一格开始。
const (
	badgeWidth = 9
	bodyIndent = badgeWidth + 1
)

func (l level) badge() string        { return l.printer.GetFormattedPrefix() }
func (l level) style() *pterm.Style  { return l.printer.MessageStyle }
func (l level) text(s string) string { return l.style().Sprint(s) }

// line 渲染一行带标签的消息，如 " INFO   本机出口 …\n"。
func (l level) line(msg string) string { return l.badge() + " " + l.text(msg) + "\n" }

// spinnerFrames 是盲文点阵转圈动画，每个字符都是单宽度。
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// 版式(宽度单位为终端单元格)：测速按阶段逐个展示，每个阶段是一个小区块，数据按列对齐。
// 当前阶段在原地刷新，测完定格，再出现下一个阶段；区块不带 INFO、SUCCESS 之类的标签：
//
//	下载 · 单线程 · 1 条连接     ━━━━━━━━━━━━━━━━━━━━
//	速率 971.7 Mbps       传输 619.8 MB         用时 5.3 秒
//	负载时延 14.6 ms      负载抖动 4.1 ms       负载最高 26.9 ms
const (
	titleWidth  = 28 // 标题列，进度条接在它后面
	barGap      = 2
	barLen      = 20 // 进度条最多占用的字符数
	minBarLen   = 8  // 终端太窄放不下这么多时不显示进度条
	maxColWidth = 22 // 数据行里每一项(最后一项除外)占的最大列宽，使两行的各项上下对齐
	minColWidth = 17 // 终端窄时压缩到的最小列宽，仍容得下最长的一项(如 "负载时延 14.6 ms")
	cellWidth   = 18 // 最长一项的宽度，用于估算列宽

	barFilled = "━"
	barEmpty  = "─"
	partSep   = " · "
)

type viewState uint8

const (
	statePending viewState = iota // 尚未开始
	stateRunning                  // 进行中
	stateDone                     // 已完成
)

// delayStats 是一组往返时延的统计，单位毫秒。
type delayStats struct {
	medianMs, minMs, maxMs, jitterMs float64
	failPct                          float64
	samples, failed                  int // 成功与失败的次数
}

func delayOf(s taier.LatencyStats) delayStats {
	return delayStats{
		medianMs: millis(s.Median), minMs: millis(s.Min), maxMs: millis(s.Max), jitterMs: millis(s.Jitter),
		failPct: s.FailRate(), samples: s.Samples, failed: s.Failed,
	}
}

// stageView 是一个测速阶段的展示状态：空闲时延，或某个方向、某种连接数的传输。
type stageView struct {
	phase    string // taier.PhaseLatency、PhaseDownload 或 PhaseUpload
	threads  int    // 传输阶段使用的连接数
	state    viewState
	fraction float64 // 进度 0~1
	mbps     float64
	bytes    int64         // 已传输的数据量(含预热)
	total    time.Duration // 已用时间(含预热)，与 bytes 口径一致，二者相除才与速率大致吻合
	delay    delayStats    // 空闲阶段为空闲时延，传输阶段为负载时延
}

// testView 是一个节点的展示状态。
type testView struct {
	operator string
	family   string
	node     runner.NodeRef
	stages   []stageView // 按测速顺序：空闲时延、下载(单线程、多线程)、上传(单线程、多线程)
}

// newTestView 按将要执行的阶段构造视图，各阶段先显示为未开始。
func newTestView(n runner.NodeRef, family string, stages []taier.Stage) *testView {
	v := &testView{operator: n.Operator, family: familyLabel(family), node: n}
	v.stages = append(v.stages, stageView{phase: taier.PhaseLatency})
	for _, st := range stages {
		v.stages = append(v.stages, stageView{phase: st.Phase, threads: st.Threads})
	}
	return v
}

// update 用进度事件更新对应阶段，返回该阶段的下标；找不到对应阶段(如已跳过的方向)时返回 -1。
func (v *testView) update(p taier.ProgressUpdate) int {
	for i := range v.stages {
		s := &v.stages[i]
		if s.phase != p.Phase || (s.phase != taier.PhaseLatency && s.threads != p.Threads) {
			continue
		}
		s.state, s.fraction, s.delay = stateRunning, p.Fraction, delayOf(p.Latency)
		if s.phase != taier.PhaseLatency {
			s.mbps, s.bytes, s.total = p.Mbps, p.Bytes, p.Total
		}
		if p.Final {
			s.state = stateDone
		}
		return i
	}
	return -1
}

// renderer 渲染 testView；width 为可用的终端宽度。
type renderer struct{ width int }

// barCells 返回进度条的字符数，终端太窄时为 0(不显示进度条)。
func (r renderer) barCells() int {
	cell := runewidth.RuneWidth([]rune(barFilled)[0])
	n := min(barLen, (r.width-bodyIndent-titleWidth-barGap-1)/cell)
	if n < minBarLen {
		return 0
	}
	return n
}

// header 渲染节点标题行，沿用最初的写法："测试 电信 IPv4 济南电信 (58.56.118.126)"；
// 终端太窄时省略括号里的地址。前面空一行。
func (r renderer) header(v *testView) string {
	msg := fmt.Sprintf("测试 %s %s %s", displayOperator(v.operator), v.family, v.node.Name)
	if host := hostOf(v.node.Address); host != "" && bodyIndent+runewidth.StringWidth(msg)+runewidth.StringWidth(host)+3 <= r.width {
		msg += " (" + host + ")"
	}
	return "\n" + levelInfo.line(msg)
}

// hostOf 去掉 host:port 中的端口，节点地址的端口对用户没有意义。
func hostOf(addr string) string {
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap.Addr().String()
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// block 渲染一个阶段的区块：前面空一行，然后是标题(右侧是进度条)和各行数据，不带状态标签。
// 标题与进度条按阶段类型固定颜色，数据用白色；标题与数据都与"测试 …"等状态行的正文对齐。
func (r renderer) block(s *stageView) string {
	kind := stageStyle(s)
	title, lines := r.stageText(s)
	head := kind.Sprint(title)
	if bar := r.bar(s.fraction, kind); bar != "" { // 进度条对齐在标题列之后；没有进度条时标题后不补空格
		head += strings.Repeat(" ", max(titleWidth-runewidth.StringWidth(title), 0)+barGap) + bar
	}
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(strings.Repeat(" ", bodyIndent) + head + "\n")
	for _, l := range lines {
		if l != "" {
			b.WriteString(strings.Repeat(" ", bodyIndent) + l + "\n")
		}
	}
	return b.String()
}

// stageStyle 返回阶段标题与进度条的固定颜色。
func stageStyle(s *stageView) *pterm.Style {
	switch s.phase {
	case taier.PhaseDownload:
		return styleDown
	case taier.PhaseUpload:
		return styleUp
	}
	return styleLatency
}

// stageText 返回阶段的标题(纯文本)与数据行。每行把相关的几项并在一起，尚无数据的项显示 -。
func (r renderer) stageText(s *stageView) (title string, lines []string) {
	avail := r.width - bodyIndent
	d := s.delay
	if s.phase == taier.PhaseLatency {
		have, haveJitter := d.samples >= 1, d.samples >= 2 // 抖动至少要两个样本
		first := []part{
			{"时延 ", pick(have, value(d.medianMs, "ms"), "")},
			{"采样 ", pick(d.samples+d.failed > 0, styleValue.Sprint(d.samples+d.failed)+styleDim.Sprint(" 次"), "")},
		}
		if d.failPct > 0 {
			first = append(first, part{"失败率 ", value(d.failPct, "%")})
		}
		second := []part{
			{"抖动 ", pick(haveJitter, value(d.jitterMs, "ms"), "")},
			{"最低 ", pick(have, value(d.minMs, "ms"), "")},
			{"最高 ", pick(have, value(d.maxMs, "ms"), "")},
		}
		return "空闲时延", []string{joinParts(first, avail), joinParts(second, avail)}
	}

	direction := "下载"
	if s.phase == taier.PhaseUpload {
		direction = "上传"
	}
	mode := "多线程"
	if s.threads == 1 {
		mode = "单线程"
	}
	title = fmt.Sprintf("%s · %s · %d 条连接", direction, mode, s.threads)
	started := s.state != statePending
	size := ""
	if s.bytes > 0 {
		v, unit := splitBytes(s.bytes)
		size = value(v, unit)
	}
	first := []part{
		{"速率 ", pick(started, value(s.mbps, "Mbps"), "")},
		{"传输 ", size},
		{"用时 ", pick(s.total > 0, value(s.total.Seconds(), "秒"), "")},
	}
	second := []part{
		{"负载时延 ", pick(d.samples >= 1, value(d.medianMs, "ms"), "")},
		{"负载抖动 ", pick(d.samples >= 2, value(d.jitterMs, "ms"), "")},
		{"负载最高 ", pick(d.samples >= 1, value(d.maxMs, "ms"), "")},
	}
	return title, []string{joinParts(first, avail), joinParts(second, avail)}
}

// value 格式化"数值 单位"：数值用白色，单位用灰色。
func value(v float64, unit string) string {
	return styleValue.Sprintf("%.1f", v) + styleDim.Sprint(" "+unit)
}

func pick(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// part 是"标签 数值"形式的一项信息，标签用灰色；数值为空表示尚无数据，显示 -。
type part struct{ label, value string }

func (p part) valueText() string {
	if p.value == "" {
		return styleDim.Sprint("-")
	}
	return p.value
}

func (p part) render() string { return styleDim.Sprint(p.label) + p.valueText() }

// plainWidth 返回去掉样式后占用的宽度。
func (p part) plainWidth() int { return runewidth.StringWidth(p.label) + visibleWidth(p.valueText()) }

// visibleWidth 返回 s 去掉样式转义序列后占用的宽度。
func visibleWidth(s string) int { return runewidth.StringWidth(pterm.RemoveColorFromString(s)) }

// colWidth 返回数据行里每一项(最后一项除外)占的列宽：宽终端宽松些，窄终端压缩，尽量让三项都放得下。
func colWidth(avail int) int { return min(max((avail-cellWidth)/2, minColWidth), maxColWidth) }

// joinParts 把各项排成一行：每项占固定列宽(最后一项除外)，上下两行的各项因此对齐；
// 总宽度超过 avail 时从最后一项开始省略。
func joinParts(parts []part, avail int) string {
	col := colWidth(avail)
	for len(parts) > 0 && col*(len(parts)-1)+parts[len(parts)-1].plainWidth() > avail {
		parts = parts[:len(parts)-1]
	}
	var b strings.Builder
	for i, p := range parts {
		b.WriteString(p.render())
		if i < len(parts)-1 {
			b.WriteString(strings.Repeat(" ", max(col-p.plainWidth(), 1)))
		}
	}
	return b.String()
}

// bar 渲染进度条，fraction 为 0~1 的完成比例；终端太窄时返回空串。
func (r renderer) bar(fraction float64, st *pterm.Style) string {
	n := r.barCells()
	if n == 0 {
		return ""
	}
	filled := int(math.Round(min(max(fraction, 0), 1) * float64(n)))
	return st.Sprint(strings.Repeat(barFilled, filled)) + styleDim.Sprint(strings.Repeat(barEmpty, n-filled))
}

// padRight 按显示宽度(汉字占两格)补空格；s 不能带样式。
func padRight(s string, width int) string {
	if pad := width - runewidth.StringWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func nonEmpty(items ...string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
