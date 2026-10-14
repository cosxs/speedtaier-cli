package runner

import (
	"math"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/taier"
)

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
	CodeOperatorUnknown  = "operator_unknown"   // 无法按归属地确定本机运营商，测试时延最低的节点
	CodeProxyBypassed    = "proxy_bypassed"     // 检测到透明代理，已绑定物理网卡绕开
	CodeTransparentProxy = "transparent_proxy"  // 测速经过透明代理，结果可能失真
	CodeIPv6LookupFailed = "ipv6_lookup_failed" // 部分运营商的 IPv6 节点获取失败
	CodeNodeFallback     = "node_fallback"      // 节点测速失败，已换下一节点
	CodeInterrupted      = "interrupted"        // 用户中断
)

type Warning struct {
	Code    string
	Level   string
	Message string
}

// Result 是一次运行的完整结果。
type Result struct {
	StartedAt      time.Time
	DurationMs     int64
	Client         *Client
	Province       string
	Operator       string // 测速的运营商，空表示不限运营商
	Network        Network
	Candidates     []Candidate
	Tests          []Test
	DataUsageBytes int64
	Warnings       []Warning
	Degraded       bool
	ExitCode       int
	Error          string
}

// Client 是本机出口 IP 及其归属地(来自官方归属地接口)。
type Client struct {
	IP       string
	Country  string
	Province string
	City     string
	Operator string
}

// Network 描述测速流量的出口。
type Network struct {
	Interface        string // 绑定的网卡，空表示按系统路由
	TransparentProxy string // 检测到的透明代理虚拟网卡
	ProxyBypassed    bool
	IPv6             bool // 测速出口有公网 IPv6 路由
}

// Candidate 是省份内的一个节点及其连通性探测结果。
type Candidate struct {
	ID       string
	Name     string
	City     string
	Operator string
	IPv4     Reach
	IPv6     Reach
}

// 连通性状态。
const (
	ReachOK          = "ok"
	ReachUnreachable = "unreachable"
	ReachSkipped     = "skipped"    // 本机不支持该协议族，未探测
	ReachNoAddress   = "no_address" // 节点没有该协议族的地址
)

type Reach struct {
	Address string
	Status  string
	RTTMs   *float64
}

// NodeRef 标识一个测速节点及所用地址。
type NodeRef struct {
	ID       string
	Name     string
	City     string
	Operator string
	Address  string
}

// 测速状态。
const (
	TestOK     = "ok"
	TestFailed = "failed"
)

// Test 是一个运营商在一个协议族下的测速结果。
type Test struct {
	Operator    string
	Family      string
	Status      string
	Node        *NodeRef
	Attempts    []Attempt // 此前测速失败的节点
	IdleLatency *Latency
	Download    *Transfer // 多线程(config.threads 条连接)
	Upload      *Transfer

	// 单线程结果，仅在 config.single_thread 为 true 时有值。
	DownloadSingle *Transfer
	UploadSingle   *Transfer

	Error string
}

type Attempt struct {
	Node  NodeRef
	Error string
}

// Latency 是一组往返时延的统计，单位毫秒。
type Latency struct {
	Samples     int
	Failed      int
	FailRatePct float64 // 请求失败比例，不等同丢包率
	MinMs       float64
	MedianMs    float64
	MaxMs       float64
	JitterMs    float64 // 按时间顺序相邻样本差的绝对值平均
}

// Transfer 是一个传输方向的结果。
type Transfer struct {
	Threads       int // 使用的连接数
	Mbps          float64
	Bytes         int64
	TotalMs       int64 // 阶段总用时，含预热；与 bytes 口径一致
	DurationMs    int64 // 计量时长，不含预热；mbps 按它计算
	Stable        bool  // 吞吐收敛后提前结束
	LoadedLatency *Latency
	Errors        []string
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
		Threads:       t.Threads,
		Mbps:          round2(t.Mbps),
		Bytes:         t.Bytes,
		TotalMs:       t.Total.Milliseconds(),
		DurationMs:    t.Duration.Milliseconds(),
		Stable:        t.Stable,
		LoadedLatency: latencyOf(t.Latency),
		Errors:        t.Errors,
	}
}

func ms(d time.Duration) float64 { return round2(float64(d) / float64(time.Millisecond)) }

func round2(v float64) float64 { return math.Round(v*100) / 100 }
