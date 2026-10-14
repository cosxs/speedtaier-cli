package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/pterm/pterm"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// errNoTerminal 表示 --select 需要在终端里交互运行。
var errNoTerminal = errors.New("--select 需要在终端中交互运行(标准输入输出都不能被重定向)")

// promptChooser 用 pterm 的交互选择实现 runner.Chooser：先选省份，再选节点，输入文字即可过滤。
type promptChooser struct{}

var _ runner.Chooser = promptChooser{}

func (promptChooser) Province(provinces []string, home string) (string, error) {
	labels := provinceLabels(provinces)
	i, err := selectOne("选择省份(可输入拼音过滤)", labels, slices.Index(provinces, home))
	if err != nil {
		return "", err
	}
	return provinces[i], nil
}

func (promptChooser) Node(province string, nodes []runner.Candidate, def int) (int, error) {
	return selectOne("选择 "+province+" 的测速节点(可输入名称、城市、运营商过滤)", nodeLabels(nodes), def)
}

// selectOne 显示选择列表，返回所选选项的下标；def 为默认选项的下标(越界则不指定)。
// labels 必须互不相同；用户按 Ctrl-C 时返回 runner.ErrInterrupted。
func selectOne(prompt string, labels []string, def int) (int, error) {
	interrupted := false
	sel := pterm.DefaultInteractiveSelect.
		WithOptions(labels).
		WithMaxHeight(12).
		WithOnInterruptFunc(func() { interrupted = true })
	if def >= 0 && def < len(labels) {
		sel = sel.WithDefaultOption(labels[def])
	}
	label, err := sel.Show(prompt)
	switch {
	case interrupted:
		return 0, runner.ErrInterrupted
	case err != nil:
		return 0, fmt.Errorf("交互选择失败: %w", err)
	}
	return slices.Index(labels, label), nil
}

// provinceLabels 的选项同时显示中文和拼音(如 "山东 shandong")，可直接输入拼音过滤。
func provinceLabels(provinces []string) []string {
	labels := make([]string, len(provinces))
	for i, name := range provinces {
		labels[i] = strings.TrimSpace(name + " " + taier.ProvincePinyin(name))
	}
	return labels
}

// nodeLabels 为每个节点生成一行选项：节点名、城市与运营商、IPv4/IPv6 时延，各列对齐。
// 选项必须互不相同(选择结果按文本对应回下标)，同名同况的节点追加地址区分。
func nodeLabels(nodes []runner.Candidate) []string {
	nameWidth, whereWidth := 0, 0
	wheres := make([]string, len(nodes))
	for i, n := range nodes {
		wheres[i] = strings.Join(nonEmpty(n.City, displayOperator(n.Operator)), partSep)
		nameWidth = max(nameWidth, runewidth.StringWidth(n.Name))
		whereWidth = max(whereWidth, runewidth.StringWidth(wheres[i]))
	}
	labels := make([]string, len(nodes))
	for i, n := range nodes {
		labels[i] = strings.TrimRight(padRight(n.Name, nameWidth)+"  "+padRight(wheres[i], whereWidth)+"  "+reachText(n), " ")
	}
	// 极端情况下两个节点的整行文字完全相同，追加地址区分。
	for i, l := range labels {
		if slices.Index(labels, l) != i {
			labels[i] = l + "  " + hostOf(cmpFirst(nodes[i].IPv4.Address, nodes[i].IPv6.Address))
		}
	}
	return labels
}

func cmpFirst(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// reachText 返回节点的时延：IPv4 可达时显示时延，不可达显示"不可达"；本机支持 IPv6 且节点有 IPv6 地址时一并显示。
func reachText(n runner.Candidate) string {
	var parts []string
	for _, r := range []struct {
		name  string
		reach runner.Reach
	}{{"IPv4", n.IPv4}, {"IPv6", n.IPv6}} {
		switch r.reach.Status {
		case runner.ReachOK:
			parts = append(parts, fmt.Sprintf("%s %.1f ms", r.name, *r.reach.RTTMs))
		case runner.ReachUnreachable:
			parts = append(parts, r.name+" 不可达")
		}
	}
	return strings.Join(parts, "  ")
}
