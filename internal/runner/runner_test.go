package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/fakenode"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// spec 描述一个模拟节点。
type spec struct {
	name, city, operator string
	opts                 fakenode.Options
}

// testConfig 为 specs 启动模拟节点，并返回使用这些节点、离线运行的 Config。
// 默认测试全部运营商，按归属地选择运营商的行为见 TestRunOperator。
func testConfig(t *testing.T, specs ...spec) Config {
	t.Helper()
	var nodes []taier.Node
	for i, s := range specs {
		fn := fakenode.Start(t, s.opts)
		nodes = append(nodes, taier.Node{
			ID: string(rune('a' + i)), Name: s.name, Province: "山东", City: s.city, Operator: s.operator,
			IPv4: netip.MustParseAddr(fn.Host), Port: fn.Port,
		})
	}
	return Config{
		Province:  "shandong",
		Family:    Family4,
		Interface: InterfaceNone,
		Operator:  OperatorAll,
		Test: taier.Options{
			Threads: 1, Seconds: 1, LatencyCount: 2,
			DownloadGrace: 200 * time.Millisecond, UploadGrace: 200 * time.Millisecond,
		},
		Sources: Sources{
			Nodes: func(context.Context) ([]taier.Node, error) { return nodes, nil },
			Geo: func(context.Context) (taier.Geo, error) {
				return taier.Geo{IP: "203.0.113.5", Country: "中国", Province: "山东", City: "济南", Operator: "联通"}, nil
			},
			IPv6: func(context.Context, []taier.Node) error { return nil },
		},
	}
}

func warningCodes(res *Result) []string {
	var codes []string
	for _, w := range res.Warnings {
		codes = append(codes, w.Code)
	}
	return codes
}

func TestRunPerOperator(t *testing.T) {
	cfg := testConfig(t,
		spec{name: "济南电信", city: "济南", operator: "电信"},
		spec{name: "济南联通", city: "济南", operator: "联通"},
	)
	res := Run(context.Background(), cfg, Discard)

	if res.ExitCode != ExitOK || res.Degraded || res.Error != "" {
		t.Fatalf("exit=%d degraded=%v error=%q warnings=%v", res.ExitCode, res.Degraded, res.Error, res.Warnings)
	}
	if res.Province != "山东" || res.Client == nil || res.Client.IP != "203.0.113.5" {
		t.Errorf("province=%q client=%+v", res.Province, res.Client)
	}
	if len(res.Candidates) != 2 || res.Candidates[0].IPv4.Status != ReachOK || res.Candidates[0].IPv6.Status != ReachNoAddress {
		t.Errorf("candidates = %+v", res.Candidates)
	}
	if len(res.Tests) != 2 || res.Tests[0].Operator != "电信" || res.Tests[1].Operator != "联通" {
		t.Fatalf("tests = %+v", res.Tests)
	}
	for _, tt := range res.Tests {
		if tt.Status != TestOK || tt.Node == nil || tt.Download == nil || tt.Upload == nil ||
			tt.Download.Mbps <= 0 || tt.IdleLatency == nil || tt.IdleLatency.Samples != 2 {
			t.Errorf("test = %+v", tt)
		}
	}
	if res.DataUsageBytes <= 0 || res.Config.MaxSeconds != 1 || res.Config.TimeoutSeconds <= 0 {
		t.Errorf("usage=%d config=%+v", res.DataUsageBytes, res.Config)
	}
}

func TestRunOperator(t *testing.T) {
	cfg := testConfig(t,
		spec{name: "济南电信", city: "济南", operator: "电信", opts: fakenode.Options{PingDelay: 15 * time.Millisecond}},
		spec{name: "济南联通", city: "济南", operator: "联通"},
		spec{name: "青岛联通", city: "青岛", operator: "联通", opts: fakenode.Options{PingDelay: 30 * time.Millisecond}},
	)
	cfg.Test.SkipDownload, cfg.Test.SkipUpload = true, true // 只测时延，加快测试

	tests := []struct {
		name, operator string
		geo            *taier.Geo // nil 表示归属地查询失败
		wantNodes      []string   // 各测试实际使用的节点
		wantOperator   string
		wantCands      int
		wantCodes      []string
	}{
		{"auto 只测本机运营商", OperatorAuto, &taier.Geo{Province: "山东", City: "济南", Operator: "联通"},
			[]string{"济南联通"}, "联通", 2, nil},
		{"本市优先于时延", OperatorAuto, &taier.Geo{Province: "山东省", City: "青岛市", Operator: "中国联通"},
			[]string{"青岛联通"}, "联通", 2, nil},
		{"本市没有节点时测本省", "", &taier.Geo{Province: "山东", City: "东营", Operator: "联通"},
			[]string{"济南联通"}, "联通", 2, nil},
		{"外省归属地不按城市优先", "", &taier.Geo{Province: "江苏", City: "青岛", Operator: "联通"},
			[]string{"济南联通"}, "联通", 2, nil},
		{"本省没有本机运营商时测最快节点", OperatorAuto, &taier.Geo{Province: "山东", City: "济南", Operator: "移动"},
			[]string{"济南联通"}, "", 3, []string{CodeOperatorUnknown}},
		{"归属地查询失败时测最快节点", OperatorAuto, nil,
			[]string{"济南联通"}, "", 3, []string{CodeOperatorUnknown}},
		{"all", OperatorAll, &taier.Geo{Province: "山东", City: "青岛", Operator: "联通"},
			[]string{"济南电信", "青岛联通"}, "", 3, nil},
		{"拼音指定", "dianxin", &taier.Geo{Province: "山东", City: "济南", Operator: "联通"},
			[]string{"济南电信"}, "电信", 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cfg
			c.Operator = tt.operator
			c.Sources.Geo = func(context.Context) (taier.Geo, error) {
				if tt.geo == nil {
					return taier.Geo{}, errors.New("offline")
				}
				return *tt.geo, nil
			}
			res := Run(context.Background(), c, Discard)

			var nodes []string
			for _, test := range res.Tests {
				if test.Node != nil && test.Node.Operator == test.Operator {
					nodes = append(nodes, test.Node.Name)
				}
			}
			if res.ExitCode != ExitOK || !slices.Equal(nodes, tt.wantNodes) || res.Operator != tt.wantOperator ||
				len(res.Candidates) != tt.wantCands || !slices.Equal(warningCodes(res), tt.wantCodes) {
				t.Errorf("exit=%d error=%q nodes=%v operator=%q candidates=%d warnings=%v",
					res.ExitCode, res.Error, nodes, res.Operator, len(res.Candidates), res.Warnings)
			}
		})
	}

	bad := cfg
	bad.Operator = "yidong"
	if res := Run(context.Background(), bad, Discard); res.ExitCode != ExitFailed ||
		!strings.Contains(res.Error, "可选: 电信 联通 all") || len(res.Candidates) != 0 {
		t.Errorf("unknown operator: exit=%d error=%q", res.ExitCode, res.Error)
	}
}

// 加测单线程：每个方向先单线程、后多线程，结果分别记录；通知界面的阶段列表与实际执行一致。
func TestRunSingleThread(t *testing.T) {
	cfg := testConfig(t, spec{name: "济南电信", city: "济南", operator: "电信"})
	cfg.Test.Threads, cfg.Test.SingleThread, cfg.Test.SkipUpload = 2, true, true
	var stages []taier.Stage
	rep := &stageReporter{Reporter: Discard, stages: &stages}
	res := Run(context.Background(), cfg, rep)

	if res.ExitCode != ExitOK || len(res.Tests) != 1 {
		t.Fatalf("exit=%d error=%q tests=%+v", res.ExitCode, res.Error, res.Tests)
	}
	tt := res.Tests[0]
	if tt.Download == nil || tt.Download.Threads != 2 || tt.DownloadSingle == nil || tt.DownloadSingle.Threads != 1 ||
		tt.DownloadSingle.Mbps <= 0 || tt.Upload != nil || tt.UploadSingle != nil {
		t.Errorf("test = %+v", tt)
	}
	if want := []taier.Stage{{Phase: taier.PhaseDownload, Threads: 1}, {Phase: taier.PhaseDownload, Threads: 2}}; !slices.Equal(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
	if !res.Config.SingleThread || res.Config.Threads != 2 {
		t.Errorf("config = %+v", res.Config)
	}
	if want := tt.Download.Bytes + tt.DownloadSingle.Bytes; res.DataUsageBytes != want {
		t.Errorf("data usage = %d, want %d (both stages)", res.DataUsageBytes, want)
	}

	// Threads 为 1 时已是单线程，不重复测，也不标记 single_thread。
	cfg.Test.Threads, stages = 1, nil
	res = Run(context.Background(), cfg, rep)
	if tt := res.Tests[0]; tt.Download == nil || tt.Download.Threads != 1 || tt.DownloadSingle != nil || res.Config.SingleThread {
		t.Errorf("threads=1: test=%+v config=%+v", tt, res.Config)
	}
}

// stageReporter 记录 Testing 事件中的阶段列表。
type stageReporter struct {
	Reporter
	stages *[]taier.Stage
}

func (r *stageReporter) Testing(n NodeRef, family string, stages []taier.Stage) {
	*r.stages = append(*r.stages, stages...)
}

func TestRunFallbackToNextNode(t *testing.T) {
	cfg := testConfig(t,
		spec{name: "繁忙节点", city: "济南", operator: "电信", opts: fakenode.Options{Busy: true}},
		// 探测时延更高，排在繁忙节点之后。
		spec{name: "备用节点", city: "青岛", operator: "电信", opts: fakenode.Options{PingDelay: 30 * time.Millisecond}},
	)
	res := Run(context.Background(), cfg, Discard)

	if res.ExitCode != ExitOK || len(res.Tests) != 1 {
		t.Fatalf("exit=%d tests=%+v", res.ExitCode, res.Tests)
	}
	tt := res.Tests[0]
	if tt.Status != TestOK || tt.Node.Name != "备用节点" || len(tt.Attempts) != 1 || tt.Attempts[0].Node.Name != "繁忙节点" {
		t.Errorf("test = %+v", tt)
	}
	if !slices.Equal(warningCodes(res), []string{CodeNodeFallback}) {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestRunAllNodesFail(t *testing.T) {
	cfg := testConfig(t,
		spec{name: "繁忙1", city: "济南", operator: "移动", opts: fakenode.Options{Busy: true}},
		spec{name: "繁忙2", city: "青岛", operator: "移动", opts: fakenode.Options{Busy: true}},
	)
	res := Run(context.Background(), cfg, Discard)

	tt := res.Tests[0]
	if res.ExitCode != ExitDegraded || !res.Degraded || tt.Status != TestFailed || len(tt.Attempts) != 2 ||
		!strings.Contains(tt.Error, "繁忙") {
		t.Errorf("exit=%d degraded=%v test=%+v", res.ExitCode, res.Degraded, tt)
	}
}

func TestRunProvinceFromGeo(t *testing.T) {
	cfg := testConfig(t, spec{name: "济南电信", city: "济南", operator: "电信"})
	cfg.Province, cfg.SurveyOnly = "", true
	res := Run(context.Background(), cfg, Discard)

	if res.ExitCode != ExitOK || res.Province != "山东" || len(res.Tests) != 0 ||
		len(res.Warnings) != 0 {
		t.Errorf("exit=%d province=%q tests=%d warnings=%v", res.ExitCode, res.Province, len(res.Tests), res.Warnings)
	}
}

func TestRunStartupFailures(t *testing.T) {
	cfg := testConfig(t, spec{name: "济南电信", city: "济南", operator: "电信"})

	bad := cfg
	bad.Province = "mars"
	if res := Run(context.Background(), bad, Discard); res.ExitCode != ExitFailed || !strings.Contains(res.Error, "shandong") {
		t.Errorf("unknown province: exit=%d error=%q", res.ExitCode, res.Error)
	}

	noGeo := cfg
	noGeo.Province = ""
	noGeo.Sources.Geo = func(context.Context) (taier.Geo, error) { return taier.Geo{}, errors.New("offline") }
	if res := Run(context.Background(), noGeo, Discard); res.ExitCode != ExitFailed || res.Client != nil {
		t.Errorf("no geo: exit=%d client=%+v", res.ExitCode, res.Client)
	}

	noNodes := cfg
	noNodes.Sources.Nodes = func(context.Context) ([]taier.Node, error) { return nil, errors.New("offline") }
	if res := Run(context.Background(), noNodes, Discard); res.ExitCode != ExitFailed || !strings.Contains(res.Error, "节点库") {
		t.Errorf("no nodes: exit=%d error=%q", res.ExitCode, res.Error)
	}
}

func TestRunInterrupted(t *testing.T) {
	cfg := testConfig(t, spec{name: "济南电信", city: "济南", operator: "电信"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := Run(ctx, cfg, Discard); res.ExitCode != ExitInterrupted || !slices.Contains(warningCodes(res), CodeInterrupted) {
		t.Errorf("canceled: exit=%d warnings=%v", res.ExitCode, res.Warnings)
	}
}

func TestCandidatesOrder(t *testing.T) {
	cfg := testConfig(t,
		spec{name: "济南移动", city: "济南", operator: "移动"},
		spec{name: "青岛电信", city: "青岛", operator: "电信"},
		spec{name: "济南电信", city: "济南", operator: "电信"},
	)
	cfg.SurveyOnly = true
	res := Run(context.Background(), cfg, Discard)
	var names []string
	for _, c := range res.Candidates {
		names = append(names, c.Name)
	}
	// 按城市首次出现的顺序分组，同城按运营商排序。
	if want := []string{"济南电信", "济南移动", "青岛电信"}; !slices.Equal(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
}

func TestResultJSON(t *testing.T) {
	cfg := testConfig(t, spec{name: "济南电信", city: "济南", operator: "电信"})
	cfg.Version = "test"
	data, err := json.Marshal(Run(context.Background(), cfg, Discard))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if cfg := doc["config"].(map[string]any); cfg["operator"] != OperatorAll {
		t.Errorf("config.operator = %v", cfg["operator"])
	}
	for _, key := range []string{"schema_version", "version", "started_at", "duration_ms", "config", "client",
		"province", "network", "candidates", "tests", "data_usage_bytes", "warnings", "degraded", "exit_code"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("JSON missing %q: %s", key, data)
		}
	}
	if doc["schema_version"] != float64(SchemaVersion) {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}
	// 空列表输出为 []，而不是 null，方便 jq 等工具直接遍历。
	if w, ok := doc["warnings"].([]any); !ok || len(w) != 0 {
		t.Errorf("warnings = %#v", doc["warnings"])
	}
	test := doc["tests"].([]any)[0].(map[string]any)
	for _, key := range []string{"operator", "family", "status", "node", "idle_latency", "download", "upload"} {
		if _, ok := test[key]; !ok {
			t.Errorf("test JSON missing %q", key)
		}
	}
	// 传输量(bytes)含预热，total_ms 是同口径的总用时，duration_ms 是不含预热的计量时长。
	dl := test["download"].(map[string]any)
	for _, key := range []string{"threads", "mbps", "bytes", "total_ms", "duration_ms", "stable", "loaded_latency"} {
		if _, ok := dl[key]; !ok {
			t.Errorf("download JSON missing %q", key)
		}
	}
	if dl["total_ms"].(float64) < dl["duration_ms"].(float64) {
		t.Errorf("total_ms %v < duration_ms %v", dl["total_ms"], dl["duration_ms"])
	}
}

func TestParseFamilyMode(t *testing.T) {
	for _, in := range []string{"x", "", "46"} {
		if _, err := ParseFamilyMode(in); err == nil {
			t.Errorf("ParseFamilyMode(%q): want error", in)
		}
	}
	tests := []struct {
		in      string
		localV6 bool
		want    []taier.Family
		wantErr bool
	}{
		{"auto", true, []taier.Family{taier.IPv4, taier.IPv6}, false},
		{"auto", false, []taier.Family{taier.IPv4}, false},
		{"IPv4", true, []taier.Family{taier.IPv4}, false},
		{"6", true, []taier.Family{taier.IPv6}, false},
		{"v6", false, nil, true},
	}
	for _, tt := range tests {
		m, err := ParseFamilyMode(tt.in)
		if err != nil {
			t.Fatalf("ParseFamilyMode(%q): %v", tt.in, err)
		}
		got, err := m.families(tt.localV6)
		if (err != nil) != tt.wantErr || !slices.Equal(got, tt.want) {
			t.Errorf("%s/localV6=%v: got %v, %v", tt.in, tt.localV6, got, err)
		}
	}
}
