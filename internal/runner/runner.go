package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/netx"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// Reporter 接收运行过程中的事件以驱动界面。所有方法都在调用 Run 的 goroutine 中同步调用。
type Reporter interface {
	Client(c Client)                                           // 本机出口与归属地
	Warn(w Warning)                                            // 已记入 Result.Warnings 的警告
	Step(msg string) (done func())                             // 耗时步骤开始，返回结束回调
	Survey(res *Result)                                        // 节点探测完成，res.Candidates 已填充
	Testing(node NodeRef, family string, stages []taier.Stage) // 开始测一个节点，stages 为将依次执行的传输阶段
	Progress(u taier.ProgressUpdate)                           // 当前节点的测速进度
	Tested(t Test)                                             // 一个分组测试完成，成功或所有节点都失败
}

// Discard 丢弃所有事件，用于测试等无界面场景。
var Discard Reporter = discard{}

type discard struct{}

func (discard) Client(Client)                          {}
func (discard) Warn(Warning)                           {}
func (discard) Step(string) func()                     { return func() {} }
func (discard) Survey(*Result)                         {}
func (discard) Testing(NodeRef, string, []taier.Stage) {}
func (discard) Progress(taier.ProgressUpdate)          {}
func (discard) Tested(Test)                            {}

// Run 执行一次完整运行。返回的 Result 总可以序列化，ExitCode 给出建议的进程退出码。
func Run(ctx context.Context, cfg Config, rep Reporter) *Result {
	if cfg.Family == "" {
		cfg.Family = FamilyAuto
	}
	if cfg.Interface == "" {
		cfg.Interface = InterfaceAuto
	}
	if cfg.Operator == "" {
		cfg.Operator = OperatorAuto
	}
	cfg.Test = cfg.Test.WithDefaults()
	r := &run{ctx: ctx, cfg: cfg, rep: rep, src: cfg.Sources.withDefaults()}
	r.res = r.newResult()
	return r.finish(r.execute())
}

type run struct {
	ctx context.Context
	cfg Config
	rep Reporter
	src Sources
	res *Result
}

func (r *run) newResult() *Result {
	return &Result{
		StartedAt:  time.Now().Truncate(time.Millisecond),
		Candidates: []Candidate{},
		Tests:      []Test{},
		Warnings:   []Warning{},
	}
}

func (r *run) warn(code, level, format string, args ...any) {
	w := Warning{Code: code, Level: level, Message: fmt.Sprintf(format, args...)}
	r.res.Warnings = append(r.res.Warnings, w)
	r.rep.Warn(w)
}

func (r *run) execute() error {
	// 节点库与归属地并发获取；归属地用于确定省份、运营商与本市，失败时须用 --province 指定省份。
	geoCh := make(chan *taier.Geo, 1)
	go func() {
		g, err := r.src.Geo(r.ctx)
		if err != nil {
			geoCh <- nil
			return
		}
		geoCh <- &g
	}()
	done := r.rep.Step("获取节点库")
	all, err := r.src.Nodes(r.ctx)
	geo := <-geoCh
	done()
	if geo != nil {
		r.res.Client = &Client{IP: geo.IP, Country: geo.Country, Province: geo.Province, City: geo.City, Operator: geo.Operator}
		r.rep.Client(*r.res.Client)
	}
	if err != nil {
		return fmt.Errorf("获取官方节点库失败: %w", err)
	}

	province, err := r.resolveProvince(taier.Provinces(all), geo)
	if err != nil {
		return err
	}
	r.res.Province = province
	nodes := taier.InProvince(all, province)
	// 测速只探测选定运营商的节点，list 命令列出全省节点。
	operator, city := "", ""
	if !r.cfg.SurveyOnly && r.cfg.Chooser == nil {
		if operator, err = r.resolveOperator(province, nodes, geo); err != nil {
			return err
		}
		r.res.Operator = operator
		if operator != "" {
			nodes = slices.DeleteFunc(nodes, func(n taier.Node) bool { return !taier.SameOperator(n.Operator, operator) })
		}
	}
	if geo != nil && taier.NormalizeProvince(geo.Province) == province {
		city = geo.City
	}
	dialer, err := r.selectDialer(firstIPv4(nodes))
	if err != nil {
		return err
	}

	done = r.rep.Step(fmt.Sprintf("探测 %s %d 个节点的连通性",
		strings.TrimSpace(province+" "+operator), len(nodes)))
	v6err := r.src.IPv6(r.ctx, nodes)
	localV6 := taier.HasIPv6Route(dialer)
	probe := []taier.Family{taier.IPv4}
	if localV6 {
		probe = append(probe, taier.IPv6)
	}
	cands := taier.Probe(r.ctx, dialer, nodes, probe...)
	done()
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if v6err != nil {
		r.warn(CodeIPv6LookupFailed, LevelWarn, "部分运营商的 IPv6 节点获取失败: %v", v6err)
	}
	r.res.Network.IPv6 = localV6
	r.res.Candidates = candidatesOf(sortCandidates(cands))
	r.rep.Survey(r.res)
	if r.cfg.SurveyOnly {
		return nil
	}

	families, err := r.cfg.Family.families(localV6)
	if err != nil {
		return err
	}
	opts := r.cfg.Test
	opts.Dialer, opts.OnProgress = dialer, r.rep.Progress
	if r.cfg.Chooser != nil {
		chosen, err := r.chooseNode(province, cands, geo, city)
		if err != nil {
			return err
		}
		groups := taier.PlanTests([]taier.Candidate{chosen}, city, families...)
		if len(groups) == 0 { // 所选节点没有当前协议族的地址
			return fmt.Errorf("所选节点 %s 没有可测速的地址(协议: %s)", chosen.Node.Name, r.cfg.Family)
		}
		return r.runTests(groups, opts)
	}
	return r.runTests(r.plan(cands, operator, city, families), opts)
}

// chooseNode 让 Chooser 在已探测的节点中选一个。默认选项是自动模式会选的节点：
// 本机运营商(查不到或该省没有时不限运营商)中，本市优先、时延最低的可达节点。
func (r *run) chooseNode(province string, cands []taier.Candidate, geo *taier.Geo, city string) (taier.Candidate, error) {
	sorted := sortCandidates(cands)
	operator := ""
	if geo != nil && geo.Operator != "" {
		if op := taier.LookupOperator(geo.Operator); slices.ContainsFunc(sorted, func(c taier.Candidate) bool { return taier.SameOperator(c.Node.Operator, op) }) {
			operator = op
		}
	}
	i, err := r.cfg.Chooser.Node(province, candidatesOf(sorted), defaultChoice(sorted, operator, city))
	if err != nil {
		return taier.Candidate{}, err
	}
	if i < 0 || i >= len(sorted) {
		return taier.Candidate{}, fmt.Errorf("选择的节点序号 %d 超出范围", i)
	}
	return sorted[i], nil
}

// defaultChoice 返回自动模式会选的节点在 sorted 中的下标：operator 非空时只看该运营商，
// 本市(city)的可达节点优先，其余按时延(IPv4 可达用 IPv4，否则用 IPv6)；没有可达节点时为 -1。
func defaultChoice(sorted []taier.Candidate, operator, city string) int {
	best, bestRemote, bestRTT := -1, false, time.Duration(0)
	for i, c := range sorted {
		if operator != "" && !taier.SameOperator(c.Node.Operator, operator) {
			continue
		}
		reach := c.V4
		if reach.State != taier.ReachOK {
			reach = c.V6
		}
		if reach.State != taier.ReachOK {
			continue
		}
		remote := !taier.SameCity(c.Node.City, city)
		if best < 0 || (!remote && bestRemote) || (remote == bestRemote && reach.RTT < bestRTT) {
			best, bestRemote, bestRTT = i, remote, reach.RTT
		}
	}
	return best
}

// plan 生成测速分组：--operator all 时每个运营商一组；否则只测一组(选定运营商，或运营商未知时不区分运营商)。
// 组内本市节点优先，本市没有可达节点时测本省其他城市时延最低的节点。
func (r *run) plan(cands []taier.Candidate, operator, city string, families []taier.Family) []taier.TestGroup {
	if operator != "" || r.cfg.Operator == OperatorAll {
		return taier.PlanTests(cands, city, families...)
	}
	return taier.PlanFastest(cands, city, families...)
}

// resolveProvince 依次使用指定省份；交互选择时由 Chooser 选择(归属地省份为默认)；否则使用归属地省份。
func (r *run) resolveProvince(provinces []string, geo *taier.Geo) (string, error) {
	if r.cfg.Province != "" {
		if p, ok := taier.LookupProvince(r.cfg.Province); ok && slices.Contains(provinces, p) {
			return p, nil
		}
		valid := make([]string, len(provinces))
		for i, name := range provinces {
			valid[i] = cmp.Or(taier.ProvincePinyin(name), name)
		}
		return "", fmt.Errorf("没有省份 %q，可选: %s", r.cfg.Province, strings.Join(valid, " "))
	}

	home := ""
	if geo != nil {
		if p := taier.NormalizeProvince(geo.Province); slices.Contains(provinces, p) {
			home = p
		}
	}
	if r.cfg.Chooser != nil {
		return r.cfg.Chooser.Province(provinces, home)
	}
	if home == "" {
		return "", errors.New("无法按归属地确定省份(海外网络?)，请用 --province 指定，例如 --province jiangsu")
	}
	return home, nil
}

// resolveOperator 确定测速的运营商，空串表示不限运营商。auto 模式按归属地只测本机运营商：
// 跨运营商测速受网间互联带宽限制，反映不了本机线路；无法确定本机运营商时测时延最低的节点。
func (r *run) resolveOperator(province string, nodes []taier.Node, geo *taier.Geo) (string, error) {
	switch mode := r.cfg.Operator; mode {
	case OperatorAll:
		return "", nil
	case OperatorAuto:
		if geo == nil || geo.Operator == "" {
			r.warn(CodeOperatorUnknown, LevelInfo, "无法按归属地确定本机运营商，测试时延最低的节点(可用 --operator 指定)")
			return "", nil
		}
		op := taier.LookupOperator(geo.Operator)
		if !hasOperator(nodes, op) {
			r.warn(CodeOperatorUnknown, LevelInfo, "%s 没有本机运营商 %s 的节点，测试时延最低的节点(可用 --operator 指定)", province, op)
			return "", nil
		}
		return op, nil
	default:
		op := taier.LookupOperator(mode)
		if !hasOperator(nodes, op) {
			return "", fmt.Errorf("%s 没有运营商 %q 的节点，可选: %s all", province, mode, strings.Join(operators(nodes), " "))
		}
		return op, nil
	}
}

// selectDialer 按 Interface 决定测速流量的出口。auto 模式下，若到节点的路由进入透明代理(TUN)，
// 则绑定主物理网卡绕开代理：代理会在本地完成握手、大量缓冲上传数据，使时延与上传结果失真。
func (r *run) selectDialer(target netip.Addr) (taier.Dialer, error) {
	iface := r.cfg.Interface
	if iface != InterfaceAuto && iface != InterfaceNone {
		d, err := taier.NewDialer(iface)
		if err == nil {
			err = d.Check(target)
		}
		if err != nil {
			return taier.Dialer{}, fmt.Errorf("--interface %s 不可用: %w", iface, err)
		}
		r.res.Network.Interface = iface
		return d, nil
	}

	tun, viaTUN := netx.DetectTUN(target)
	if !viaTUN {
		return taier.Dialer{}, nil
	}
	r.res.Network.TransparentProxy = tun
	if iface == InterfaceNone {
		r.warn(CodeTransparentProxy, LevelWarn, "测速经过透明代理(%s)，时延与上传结果可能失真", tun)
		return taier.Dialer{}, nil
	}
	d, err := bindPrimary(target)
	if err != nil {
		r.warn(CodeTransparentProxy, LevelWarn,
			"检测到透明代理(%s)，但无法绑定物理网卡: %v；测速将经过代理，时延与上传结果可能失真", tun, err)
		return taier.Dialer{}, nil
	}
	r.res.Network.Interface, r.res.Network.ProxyBypassed = d.Interface(), true
	r.warn(CodeProxyBypassed, LevelInfo,
		"检测到透明代理(%s)，测速流量改由物理网卡 %s 直接发出(--interface none 可关闭)", tun, d.Interface())
	return d, nil
}

func bindPrimary(target netip.Addr) (taier.Dialer, error) {
	name, err := netx.PrimaryInterface()
	if err != nil {
		return taier.Dialer{}, err
	}
	d, err := taier.NewDialer(name)
	if err != nil {
		return taier.Dialer{}, err
	}
	return d, d.Check(target)
}

// runTests 依次测试每个分组：按探测时延从低到高尝试节点，首个成功即止。被中断时返回 ctx 的错误。
func (r *run) runTests(groups []taier.TestGroup, opts taier.Options) error {
	for _, g := range groups {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		t := Test{Operator: g.Operator, Family: familyName(g.Family), Status: TestFailed, Error: "没有可达节点"}
		for i, node := range g.Nodes {
			if g.Operator == "" { // 不区分运营商的分组，记录实际测试节点的运营商
				t.Operator = node.Operator
			}
			ref := nodeRef(node, g.Family)
			r.rep.Testing(ref, t.Family, opts.Stages())
			res := taier.RunTest(r.ctx, node, g.Family, opts)
			r.res.DataUsageBytes += res.Bytes()
			if err := r.ctx.Err(); err != nil {
				return err
			}
			if res.OK {
				t.Status, t.Error, t.Node = TestOK, "", &ref
				t.IdleLatency, t.Download, t.Upload = latencyOf(res.Idle), transferOf(res.Download), transferOf(res.Upload)
				t.DownloadSingle, t.UploadSingle = transferOf(res.DownloadSingle), transferOf(res.UploadSingle)
				break
			}
			t.Attempts = append(t.Attempts, Attempt{Node: ref, Error: res.Error})
			t.Error = res.Error
			if i < len(g.Nodes)-1 {
				r.warn(CodeNodeFallback, LevelInfo, "%s %s 测速失败: %s，改测下一节点", node.Name, g.Family, res.Error)
			}
		}
		r.res.Tests = append(r.res.Tests, t)
		r.rep.Tested(t)
	}
	return nil
}

func (r *run) finish(err error) *Result {
	res := r.res
	switch {
	case r.ctx.Err() != nil || errors.Is(err, ErrInterrupted):
		r.warn(CodeInterrupted, LevelWarn, "已中断")
		res.ExitCode = ExitInterrupted
	case err != nil:
		res.Error, res.ExitCode = err.Error(), ExitFailed
	}
	res.Degraded = res.ExitCode != ExitOK ||
		slices.ContainsFunc(res.Tests, func(t Test) bool { return t.Status != TestOK }) ||
		slices.ContainsFunc(res.Warnings, func(w Warning) bool { return w.Level == LevelWarn })
	if res.ExitCode == ExitOK && res.Degraded {
		res.ExitCode = ExitDegraded
	}
	res.DurationMs = time.Since(res.StartedAt).Milliseconds()
	return res
}

// sortCandidates 按城市分组(保持节点库中的城市顺序，省会在前)，同城按运营商排序。
func sortCandidates(cands []taier.Candidate) []taier.Candidate {
	cityIndex := make(map[string]int)
	for _, c := range cands {
		if _, ok := cityIndex[c.Node.City]; !ok {
			cityIndex[c.Node.City] = len(cityIndex)
		}
	}
	sorted := slices.Clone(cands)
	slices.SortStableFunc(sorted, func(a, b taier.Candidate) int {
		return cmp.Or(
			cmp.Compare(cityIndex[a.Node.City], cityIndex[b.Node.City]),
			taier.CompareOperator(a.Node.Operator, b.Node.Operator),
		)
	})
	return sorted
}

// candidatesOf 把已排好序的候选节点转换为结果中的形式。
func candidatesOf(sorted []taier.Candidate) []Candidate {
	out := make([]Candidate, len(sorted))
	for i, c := range sorted {
		n := c.Node
		out[i] = Candidate{
			ID: n.ID, Name: n.Name, City: n.City, Operator: n.Operator,
			IPv4: reachOf(n, taier.IPv4, c.V4),
			IPv6: reachOf(n, taier.IPv6, c.V6),
		}
	}
	return out
}

func hasOperator(nodes []taier.Node, op string) bool {
	return slices.ContainsFunc(nodes, func(n taier.Node) bool { return taier.SameOperator(n.Operator, op) })
}

// operators 返回节点中出现的运营商，按 taier.CompareOperator 排序。
func operators(nodes []taier.Node) []string {
	var out []string
	for _, n := range nodes {
		if n.Operator != "" && !slices.Contains(out, n.Operator) {
			out = append(out, n.Operator)
		}
	}
	slices.SortStableFunc(out, taier.CompareOperator)
	return out
}

func firstIPv4(nodes []taier.Node) netip.Addr {
	for _, n := range nodes {
		if n.IPv4.IsValid() {
			return n.IPv4
		}
	}
	return netip.Addr{}
}
