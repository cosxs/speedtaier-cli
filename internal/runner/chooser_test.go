package runner

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/fakenode"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// fakeChooser 记录收到的参数，并按名字选择节点。
type fakeChooser struct {
	province    string // Province 返回的省份，空则返回 home
	pick        string // Node 选择的节点名
	pickIndex   *int   // 非 nil 时直接返回该下标
	errProvince error
	errNode     error

	provinceCalls, nodeCalls int
	gotProvinces             []string
	gotHome                  string
	gotProvince              string
	gotNodes                 []Candidate
	gotDef                   int
}

func (c *fakeChooser) Province(provinces []string, home string) (string, error) {
	c.provinceCalls++
	c.gotProvinces, c.gotHome = provinces, home
	if c.errProvince != nil {
		return "", c.errProvince
	}
	return cmpOr(c.province, home), nil
}

func (c *fakeChooser) Node(province string, nodes []Candidate, def int) (int, error) {
	c.nodeCalls++
	c.gotProvince, c.gotNodes, c.gotDef = province, nodes, def
	if c.errNode != nil {
		return 0, c.errNode
	}
	if c.pickIndex != nil {
		return *c.pickIndex, nil
	}
	return slices.IndexFunc(nodes, func(n Candidate) bool { return n.Name == c.pick }), nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// selectConfig 返回交互选择模式的配置：济南电信、济南联通(最快)、青岛联通；归属地为济南联通；只测时延以加快测试。
func selectConfig(t *testing.T, ch Chooser) Config {
	t.Helper()
	cfg := testConfig(t,
		spec{name: "济南电信", city: "济南", operator: "电信", opts: fakenode.Options{PingDelay: 15 * time.Millisecond}},
		spec{name: "济南联通", city: "济南", operator: "联通"},
		spec{name: "青岛联通", city: "青岛", operator: "联通", opts: fakenode.Options{PingDelay: 30 * time.Millisecond}},
	)
	cfg.Province, cfg.Chooser = "", ch
	cfg.Test.SkipDownload, cfg.Test.SkipUpload = true, true
	return cfg
}

func TestRunChooser(t *testing.T) {
	ch := &fakeChooser{pick: "济南电信"}
	cfg := selectConfig(t, ch)
	cfg.Operator = "yidong" // 交互选择时忽略 --operator(省内没有移动节点，否则会报错)
	res := Run(context.Background(), cfg, Discard)

	if res.ExitCode != ExitOK || res.Error != "" {
		t.Fatalf("exit=%d error=%q warnings=%v", res.ExitCode, res.Error, res.Warnings)
	}
	// 省份：收到可选省份与归属地省份(作默认)。
	if ch.provinceCalls != 1 || !slices.Equal(ch.gotProvinces, []string{"山东"}) || ch.gotHome != "山东" || res.Province != "山东" {
		t.Errorf("province step: calls=%d provinces=%v home=%q result=%q", ch.provinceCalls, ch.gotProvinces, ch.gotHome, res.Province)
	}
	// 节点：全省节点按城市、运营商排序，默认选项是自动模式会选的节点(本机运营商联通中本市最快的济南联通)。
	var names []string
	for _, n := range ch.gotNodes {
		names = append(names, n.Name)
	}
	if ch.nodeCalls != 1 || ch.gotProvince != "山东" || !slices.Equal(names, []string{"济南电信", "济南联通", "青岛联通"}) {
		t.Errorf("node step: calls=%d province=%q nodes=%v", ch.nodeCalls, ch.gotProvince, names)
	}
	if ch.gotDef != 1 {
		t.Errorf("default = %d, want 1 (济南联通)", ch.gotDef)
	}
	for _, n := range ch.gotNodes {
		if n.IPv4.Status != ReachOK || n.IPv4.RTTMs == nil {
			t.Errorf("node %s was not probed: %+v", n.Name, n.IPv4)
		}
	}
	// 只测所选节点，即使它不是本机运营商。
	if len(res.Tests) != 1 || res.Tests[0].Status != TestOK || res.Tests[0].Node == nil || res.Tests[0].Node.Name != "济南电信" ||
		res.Tests[0].Operator != "电信" {
		t.Errorf("tests = %+v", res.Tests)
	}
	if len(warningCodes(res)) != 0 {
		t.Errorf("warnings = %v, want none (no operator hints in select mode)", res.Warnings)
	}
}

// 指定了 --province 时跳过省份选择；没有归属地时 home 为空，仍可交互选择。
func TestRunChooserProvince(t *testing.T) {
	ch := &fakeChooser{pick: "青岛联通"}
	cfg := selectConfig(t, ch)
	cfg.Province = "shandong"
	res := Run(context.Background(), cfg, Discard)
	if res.ExitCode != ExitOK || ch.provinceCalls != 0 || ch.nodeCalls != 1 || res.Tests[0].Node.Name != "青岛联通" {
		t.Errorf("exit=%d provinceCalls=%d nodeCalls=%d tests=%+v", res.ExitCode, ch.provinceCalls, ch.nodeCalls, res.Tests)
	}

	ch = &fakeChooser{pick: "济南联通", province: "山东"}
	cfg = selectConfig(t, ch)
	cfg.Sources.Geo = func(context.Context) (taier.Geo, error) { return taier.Geo{}, errors.New("offline") }
	res = Run(context.Background(), cfg, Discard)
	if res.ExitCode != ExitOK || ch.gotHome != "" || ch.provinceCalls != 1 || len(res.Tests) != 1 {
		t.Errorf("no geo: exit=%d home=%q error=%q", res.ExitCode, ch.gotHome, res.Error)
	}
	// 没有归属地时没有默认节点运营商，也没有本市：默认选项是全省时延最低的可达节点。
	if ch.gotDef < 0 || ch.gotNodes[ch.gotDef].Name != "济南联通" {
		t.Errorf("default node = %d in %+v", ch.gotDef, ch.gotNodes)
	}
}

// 用户在任一步中断，都按"已中断"处理，不开始测速。
func TestRunChooserInterrupted(t *testing.T) {
	for name, ch := range map[string]*fakeChooser{
		"province": {errProvince: ErrInterrupted},
		"node":     {errNode: ErrInterrupted},
	} {
		res := Run(context.Background(), selectConfig(t, ch), Discard)
		if res.ExitCode != ExitInterrupted || len(res.Tests) != 0 || res.Error != "" || !slices.Contains(warningCodes(res), CodeInterrupted) {
			t.Errorf("%s: exit=%d tests=%d error=%q warnings=%v", name, res.ExitCode, len(res.Tests), res.Error, res.Warnings)
		}
	}
}

func TestRunChooserFailures(t *testing.T) {
	bad := 7
	for name, tt := range map[string]struct {
		ch   *fakeChooser
		want string
	}{
		"province error": {&fakeChooser{errProvince: errors.New("交互选择失败")}, "交互选择失败"},
		"node error":     {&fakeChooser{errNode: errors.New("终端不可用")}, "终端不可用"},
		"index too big":  {&fakeChooser{pickIndex: &bad}, "超出范围"},
		"nothing picked": {&fakeChooser{pick: "不存在的节点"}, "超出范围"}, // 下标 -1
	} {
		res := Run(context.Background(), selectConfig(t, tt.ch), Discard)
		if res.ExitCode != ExitFailed || !strings.Contains(res.Error, tt.want) || len(res.Tests) != 0 {
			t.Errorf("%s: exit=%d error=%q tests=%d", name, res.ExitCode, res.Error, len(res.Tests))
		}
	}
}

// list 命令只列出节点，不交互。
func TestRunChooserIgnoredBySurvey(t *testing.T) {
	ch := &fakeChooser{}
	cfg := selectConfig(t, ch)
	cfg.Province, cfg.SurveyOnly = "shandong", true
	res := Run(context.Background(), cfg, Discard)
	if res.ExitCode != ExitOK || ch.provinceCalls+ch.nodeCalls != 0 || len(res.Candidates) != 3 {
		t.Errorf("exit=%d calls=%d/%d candidates=%d", res.ExitCode, ch.provinceCalls, ch.nodeCalls, len(res.Candidates))
	}
}

// 所选节点没有当前协议族的地址时报错，而不是什么都不测。
func TestRunChooserNodeWithoutAddress(t *testing.T) {
	ch := &fakeChooser{pick: "仅IPv6节点"}
	cfg := selectConfig(t, ch)
	cfg.Sources.Nodes = func(context.Context) ([]taier.Node, error) {
		return []taier.Node{{ID: "x", Name: "仅IPv6节点", Province: "山东", City: "济南", Operator: "联通",
			IPv6: netip.MustParseAddr("::1"), Port: "65499"}}, nil
	}
	res := Run(context.Background(), cfg, Discard)
	if res.ExitCode != ExitFailed || !strings.Contains(res.Error, "没有可测速的地址") || len(res.Tests) != 0 {
		t.Errorf("exit=%d error=%q tests=%d", res.ExitCode, res.Error, len(res.Tests))
	}
}

func TestDefaultChoice(t *testing.T) {
	ms := time.Millisecond
	ok := func(d time.Duration) taier.Reach { return taier.Reach{State: taier.ReachOK, RTT: d} }
	down := taier.Reach{State: taier.ReachFailed}
	none := taier.Reach{State: taier.ReachNoAddr}
	cand := func(city, op string, v4, v6 taier.Reach) taier.Candidate {
		return taier.Candidate{Node: taier.Node{City: city, Operator: op}, V4: v4, V6: v6}
	}
	sorted := []taier.Candidate{
		cand("济南", "电信", ok(10*ms), none), // 0
		cand("济南", "联通", ok(8*ms), none),  // 1
		cand("济南", "移动", down, ok(5*ms)),  // 2 IPv4 不可达，只有 IPv6 可用
		cand("青岛", "联通", ok(3*ms), none),  // 3 全省最快，但不在本市
		cand("烟台", "电信", down, down),      // 4 不可达
	}
	for _, tt := range []struct {
		name, operator, city string
		want                 int
	}{
		{"local operator, local city first", "联通", "济南", 1},
		{"no local node of that operator: fastest in province", "联通", "烟台", 3},
		{"any operator, local city first", "", "济南", 2}, // 本市内最快：移动 5ms(IPv6)
		{"no city: fastest overall", "", "", 3},
		{"operator without reachable nodes", "广电", "济南", -1},
		{"city name with suffix", "联通", "济南市", 1},
	} {
		if got := defaultChoice(sorted, tt.operator, tt.city); got != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, got, tt.want)
		}
	}
	if got := defaultChoice(nil, "联通", "济南"); got != -1 {
		t.Errorf("empty list: got %d", got)
	}
	if got := defaultChoice([]taier.Candidate{cand("济南", "联通", down, down)}, "", "济南"); got != -1 {
		t.Errorf("all unreachable: got %d", got)
	}
}
