package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/cosxs/speedtaier-cli/internal/runner"
)

func reachOK(addr string, ms float64) runner.Reach {
	return runner.Reach{Address: addr, Status: runner.ReachOK, RTTMs: new(ms)}
}

func TestProvinceLabels(t *testing.T) {
	got := provinceLabels([]string{"山东", "陕西", "内蒙古"})
	if want := []string{"山东 shandong", "陕西 shaanxi", "内蒙古 neimenggu"}; !slices.Equal(got, want) {
		t.Errorf("provinceLabels = %v, want %v", got, want)
	}
}

func TestNodeLabels(t *testing.T) {
	wideAmbiguous(t, false)
	nodes := []runner.Candidate{
		{Name: "济南电信", City: "济南", Operator: "电信", IPv4: reachOK("58.56.118.126:65499", 5.0), IPv6: reachOK("[240e:4e:4000::3]:65499", 4.4)},
		{Name: "济南联通2", City: "济南", Operator: "联通", IPv4: reachOK("119.164.225.30:65499", 6.8), IPv6: runner.Reach{Status: runner.ReachNoAddress}},
		{Name: "滨州电信2", City: "滨州", Operator: "电信", IPv4: runner.Reach{Status: runner.ReachUnreachable}, IPv6: runner.Reach{Status: runner.ReachSkipped}},
		{Name: "某节点", IPv4: runner.Reach{Status: runner.ReachSkipped}, IPv6: runner.Reach{Status: runner.ReachNoAddress}},
	}
	want := []string{
		"济南电信   济南 · 电信  IPv4 5.0 ms  IPv6 4.4 ms",
		"济南联通2  济南 · 联通  IPv4 6.8 ms",
		"滨州电信2  滨州 · 电信  IPv4 不可达",
		"某节点     其他",
	}
	got := nodeLabels(nodes)
	if !slices.Equal(got, want) {
		t.Errorf("nodeLabels =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// 城市、运营商这一列从同一位置开始(按显示宽度对齐，汉字占两格)。
	starts := make([]int, 0, len(got)-1)
	for i, l := range got[:3] {
		before, _, _ := strings.Cut(l, nodes[i].City)
		starts = append(starts, runewidth.StringWidth(before))
	}
	if starts[0] != starts[1] || starts[1] != starts[2] {
		t.Errorf("misaligned columns: %v", starts)
	}
}

// 选择结果按选项文本对应回下标，所以选项必须互不相同。
func TestNodeLabelsUnique(t *testing.T) {
	same := runner.Candidate{Name: "节点", City: "济南", Operator: "电信"}
	a, b := same, same
	a.IPv4 = reachOK("1.1.1.1:65499", 5)
	b.IPv4 = reachOK("1.1.1.1:65499", 5)
	b.IPv4.Address = "2.2.2.2:65499"
	labels := nodeLabels([]runner.Candidate{a, b})
	if labels[0] == labels[1] {
		t.Errorf("duplicate labels: %q", labels)
	}
	if !strings.HasSuffix(labels[1], "2.2.2.2") {
		t.Errorf("duplicate label should be told apart by address: %q", labels[1])
	}
}

func TestSelectFlag(t *testing.T) {
	if a := newTestApp(t, "--select"); !a.cfg.selectNode {
		t.Error("--select not parsed")
	}
	if newTestApp(t).cfg.selectNode {
		t.Error("--select should default to false")
	}
	// list 命令只列出节点，没有 --select。
	list, _, err := newTestApp(t).newRootCmd().Find([]string{"list"})
	if err != nil || list.Flags().Lookup("select") != nil {
		t.Errorf("list command has --select: %v", err)
	}
}

// 标准输入输出被重定向时(脚本、管道)拒绝交互选择，而不是卡住。
func TestSelectNeedsTerminal(t *testing.T) {
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) && isTerminal(os.Stderr) {
		t.Skip("running in a terminal")
	}
	a := newTestApp(t, "--select")
	if err := a.run(context.Background(), false); !errors.Is(err, errNoTerminal) {
		t.Errorf("run with --select and no terminal: %v", err)
	}
}
