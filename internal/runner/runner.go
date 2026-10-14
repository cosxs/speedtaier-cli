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
	Info(msg string)               // 仅供展示的说明，不进入结果
	Warn(w Warning)                // 已记入 Result.Warnings 的警告
	Step(msg string) (done func()) // 耗时步骤开始，返回结束回调
	Survey(res *Result)            // 节点探测完成，res.Candidates 已填充
	Testing(node NodeRef, family string)
	Progress(u taier.ProgressUpdate)
}

// Discard 丢弃所有事件，用于 JSON 输出等无界面场景。
var Discard Reporter = discard{}

type discard struct{}

func (discard) Info(string)                   {}
func (discard) Warn(Warning)                  {}
func (discard) Step(string) func()            { return func() {} }
func (discard) Survey(*Result)                {}
func (discard) Testing(NodeRef, string)       {}
func (discard) Progress(taier.ProgressUpdate) {}

// Run 执行一次完整运行。返回的 Result 总可以序列化，ExitCode 给出建议的进程退出码。
func Run(ctx context.Context, cfg Config, rep Reporter) *Result {
	if cfg.Family == "" {
		cfg.Family = FamilyAuto
	}
	if cfg.Interface == "" {
		cfg.Interface = InterfaceAuto
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
	o := r.cfg.Test
	return &Result{
		SchemaVersion: SchemaVersion,
		Version:       r.cfg.Version,
		StartedAt:     time.Now().Truncate(time.Millisecond),
		Config: RunConfig{
			Province:       r.cfg.Province,
			Family:         string(r.cfg.Family),
			Interface:      r.cfg.Interface,
			Threads:        o.Threads,
			MaxSeconds:     o.Seconds,
			Bandwidth:      o.Bandwidth,
			LatencyCount:   o.LatencyCount,
			TimeoutSeconds: o.Timeout.Seconds(),
			Download:       !o.SkipDownload,
			Upload:         !o.SkipUpload,
			SurveyOnly:     r.cfg.SurveyOnly,
			Interactive:    r.cfg.ChooseProvince != nil,
		},
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
	// 节点库与归属地并发获取；归属地只用于默认省份与出口信息，失败不影响测速。
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
		r.rep.Info(fmt.Sprintf("本机出口 %s · %s", geo.IP,
			strings.Join(slices.DeleteFunc([]string{geo.Country, geo.Province, geo.City, geo.Operator},
				func(s string) bool { return s == "" }), " ")))
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
	dialer, err := r.selectDialer(firstIPv4(nodes))
	if err != nil {
		return err
	}

	done = r.rep.Step(fmt.Sprintf("探测 %s %d 个节点的 IPv4/IPv6 连通性", province, len(nodes)))
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
	r.res.Candidates = candidatesOf(cands)
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
	return r.runTests(taier.PlanTests(cands, families...), opts)
}

// resolveProvince 依次使用指定省份、交互选择(默认归属地)、归属地确定省份。
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
	if r.cfg.ChooseProvince != nil {
		return r.cfg.ChooseProvince(provinces, home)
	}
	if home == "" {
		return "", errors.New("无法按归属地确定省份(海外网络?)，请用 --province 指定，例如 --province jiangsu")
	}
	r.warn(CodeProvinceFromGeo, LevelInfo, "未指定省份，按归属地使用 %s(可用 --province 指定)", home)
	return home, nil
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
			ref := nodeRef(node, g.Family)
			r.rep.Testing(ref, t.Family)
			res := taier.RunTest(r.ctx, node, g.Family, opts)
			r.res.DataUsageBytes += res.Bytes()
			if err := r.ctx.Err(); err != nil {
				return err
			}
			if res.OK {
				t.Status, t.Error, t.Node = TestOK, "", &ref
				t.IdleLatency, t.Download, t.Upload = latencyOf(res.Idle), transferOf(res.Download), transferOf(res.Upload)
				break
			}
			t.Attempts = append(t.Attempts, Attempt{Node: ref, Error: res.Error})
			t.Error = res.Error
			if i < len(g.Nodes)-1 {
				r.warn(CodeNodeFallback, LevelInfo, "%s %s 测速失败: %s，改测下一节点", node.Name, g.Family, res.Error)
			}
		}
		r.res.Tests = append(r.res.Tests, t)
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

// candidatesOf 按城市分组(保持节点库中的城市顺序，省会在前)，同城按运营商排序。
func candidatesOf(cands []taier.Candidate) []Candidate {
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

func firstIPv4(nodes []taier.Node) netip.Addr {
	for _, n := range nodes {
		if n.IPv4.IsValid() {
			return n.IPv4
		}
	}
	return netip.Addr{}
}
