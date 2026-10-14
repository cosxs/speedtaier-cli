package runner

import (
	"math"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// SchemaVersion 是 Result JSON 结构的版本；字段含义变化时递增。
const SchemaVersion = 1

// 建议的进程退出码。
const (
	ExitOK          = 0
	ExitFailed      = 1   // 启动失败或参数错误
	ExitDegraded    = 2   // 部分测试失败或存在需要关注的警告
	ExitInterrupted = 130 // 用户中断
)

// 警告级别：info 仅作说明，warn 会使结果标记为 degraded。
const (
	LevelInfo = "info"
	LevelWarn = "warn"
)

// 警告编码，供脚本按编码判断，不依赖文案。
const (
	CodeProvinceFromGeo  = "province_from_geo"  // 未指定省份，按归属地选择
	CodeProxyBypassed    = "proxy_bypassed"     // 检测到透明代理，已绑定物理网卡绕开
	CodeTransparentProxy = "transparent_proxy"  // 测速经过透明代理，结果可能失真
	CodeIPv6LookupFailed = "ipv6_lookup_failed" // 部分运营商的 IPv6 节点获取失败
	CodeNodeFallback     = "node_fallback"      // 节点测速失败，已换下一节点
	CodeInterrupted      = "interrupted"        // 用户中断
)

type Warning struct {
	Code    string `json:"code"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Result 是一次运行的完整结果，字段即 --json 输出的结构。
type Result struct {
	SchemaVersion  int         `json:"schema_version"`
	Version        string      `json:"version,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	DurationMs     int64       `json:"duration_ms"`
	Config         RunConfig   `json:"config"`
	Client         *Client     `json:"client,omitempty"`
	Province       string      `json:"province,omitempty"`
	Network        Network     `json:"network"`
	Candidates     []Candidate `json:"candidates"`
	Tests          []Test      `json:"tests"`
	DataUsageBytes int64       `json:"data_usage_bytes"`
	Warnings       []Warning   `json:"warnings"`
	Degraded       bool        `json:"degraded"`
	ExitCode       int         `json:"exit_code"`
	Error          string      `json:"error,omitempty"`
}

// RunConfig 记录实际生效的参数(已填充默认值)。
type RunConfig struct {
	Province       string  `json:"province,omitempty"`
	Family         string  `json:"family"`
	Interface      string  `json:"interface"`
	Threads        int     `json:"threads"`
	MaxSeconds     int     `json:"max_seconds"`
	Bandwidth      int     `json:"bandwidth"`
	LatencyCount   int     `json:"latency_count"`
	TimeoutSeconds float64 `json:"timeout_seconds"`
	Download       bool    `json:"download"`
	Upload         bool    `json:"upload"`
	SurveyOnly     bool    `json:"survey_only"`
	Interactive    bool    `json:"interactive"`
}

// Client 是本机出口 IP 及其归属地(来自官方归属地接口)。
type Client struct {
	IP       string `json:"ip"`
	Country  string `json:"country,omitempty"`
	Province string `json:"province,omitempty"`
	City     string `json:"city,omitempty"`
	Operator string `json:"operator,omitempty"`
}

// Network 描述测速流量的出口。
type Network struct {
	Interface        string `json:"interface,omitempty"`         // 绑定的网卡，空表示按系统路由
	TransparentProxy string `json:"transparent_proxy,omitempty"` // 检测到的透明代理虚拟网卡
	ProxyBypassed    bool   `json:"proxy_bypassed"`
	IPv6             bool   `json:"ipv6"` // 测速出口有公网 IPv6 路由
}

// Candidate 是省份内的一个节点及其连通性探测结果。
type Candidate struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	City     string `json:"city,omitempty"`
	Operator string `json:"operator,omitempty"`
	IPv4     Reach  `json:"ipv4"`
	IPv6     Reach  `json:"ipv6"`
}

// 连通性状态。
const (
	ReachOK          = "ok"
	ReachUnreachable = "unreachable"
	ReachSkipped     = "skipped"    // 本机不支持该协议族，未探测
	ReachNoAddress   = "no_address" // 节点没有该协议族的地址
)

type Reach struct {
	Address string   `json:"address,omitempty"`
	Status  string   `json:"status"`
	RTTMs   *float64 `json:"rtt_ms,omitempty"`
}

// NodeRef 标识一个测速节点及所用地址。
type NodeRef struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	City     string `json:"city,omitempty"`
	Operator string `json:"operator,omitempty"`
	Address  string `json:"address"`
}

// 测速状态。
const (
	TestOK     = "ok"
	TestFailed = "failed"
)

// Test 是一个运营商在一个协议族下的测速结果。
type Test struct {
	Operator    string    `json:"operator"`
	Family      string    `json:"family"`
	Status      string    `json:"status"`
	Node        *NodeRef  `json:"node,omitempty"`
	Attempts    []Attempt `json:"attempts,omitempty"` // 此前测速失败的节点
	IdleLatency *Latency  `json:"idle_latency,omitempty"`
	Download    *Transfer `json:"download,omitempty"`
	Upload      *Transfer `json:"upload,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type Attempt struct {
	Node  NodeRef `json:"node"`
	Error string  `json:"error"`
}

// Latency 是一组往返时延的统计，单位毫秒。
type Latency struct {
	Samples     int     `json:"samples"`
	Failed      int     `json:"failed"`
	FailRatePct float64 `json:"fail_rate_pct"` // 请求失败比例，不等同丢包率
	MinMs       float64 `json:"min_ms"`
	MedianMs    float64 `json:"median_ms"`
	MaxMs       float64 `json:"max_ms"`
	JitterMs    float64 `json:"jitter_ms"` // 按时间顺序相邻样本差的绝对值平均
}

// Transfer 是一个传输方向的结果。
type Transfer struct {
	Mbps          float64  `json:"mbps"`
	Bytes         int64    `json:"bytes"`
	DurationMs    int64    `json:"duration_ms"` // 计量时长，不含预热
	Stable        bool     `json:"stable"`      // 吞吐收敛后提前结束
	LoadedLatency *Latency `json:"loaded_latency,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

func familyName(f taier.Family) string {
	if f == taier.IPv6 {
		return "ipv6"
	}
	return "ipv4"
}

func nodeRef(n taier.Node, f taier.Family) NodeRef {
	return NodeRef{ID: n.ID, Name: n.Name, City: n.City, Operator: n.Operator, Address: n.Addr(f)}
}

func reachOf(n taier.Node, f taier.Family, r taier.Reach) Reach {
	out := Reach{Address: n.Addr(f)}
	switch r.State {
	case taier.ReachOK:
		out.Status, out.RTTMs = ReachOK, new(ms(r.RTT))
	case taier.ReachFailed:
		out.Status = ReachUnreachable
	case taier.ReachSkipped:
		out.Status = ReachSkipped
	default:
		out.Status = ReachNoAddress
	}
	return out
}

// latencyOf 在既无样本也无失败(未测量)时返回 nil。
func latencyOf(s taier.LatencyStats) *Latency {
	if s.Samples == 0 && s.Failed == 0 {
		return nil
	}
	return &Latency{
		Samples:     s.Samples,
		Failed:      s.Failed,
		FailRatePct: round2(s.FailRate()),
		MinMs:       ms(s.Min),
		MedianMs:    ms(s.Median),
		MaxMs:       ms(s.Max),
		JitterMs:    ms(s.Jitter),
	}
}

// transferOf 在该方向未执行时返回 nil。
func transferOf(t taier.Transfer) *Transfer {
	if t.Bytes == 0 && t.Duration == 0 && len(t.Errors) == 0 {
		return nil
	}
	return &Transfer{
		Mbps:          round2(t.Mbps),
		Bytes:         t.Bytes,
		DurationMs:    t.Duration.Milliseconds(),
		Stable:        t.Stable,
		LoadedLatency: latencyOf(t.Latency),
		Errors:        t.Errors,
	}
}

func ms(d time.Duration) float64 { return round2(float64(d) / float64(time.Millisecond)) }

func round2(v float64) float64 { return math.Round(v*100) / 100 }
