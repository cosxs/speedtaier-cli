package taier

import (
	"context"
	"slices"
	"strings"
	"time"
)

// 测速阶段，依次为空载时延、下载、上传。
const (
	PhaseLatency  = "空载时延"
	PhaseDownload = "下载"
	PhaseUpload   = "上传"
)

// ProgressUpdate 是测速阶段的进度回调。空载时延阶段每完成一次采样回调一次；
// 传输阶段约每 250 毫秒回调一次，Warmup 期间的速率包含慢启动，不计入结果。
type ProgressUpdate struct {
	Phase    string
	Threads  int     // 传输阶段使用的连接数
	Fraction float64 // 阶段进度 0~1；传输阶段按最长时长估算，吞吐收敛后会提前结束
	Final    bool
	Mbps     float64
	Warmup   bool
	Bytes    int64         // 传输阶段已传输的字节数(含预热)
	Total    time.Duration // 传输阶段从开始到现在的用时(含预热)，与 Bytes 口径一致
	Elapsed  time.Duration // 计量时长(不含预热)，仅传输阶段 Final 时有效
	Stable   bool          // 吞吐收敛后提前结束，仅传输阶段 Final 时有效

	// Latency 是截至目前的时延统计：PhaseLatency 为空载时延；传输阶段为负载时延。
	// 负载时延在预热期间包含到目前为止的全部样本(让数据尽早出现)，预热结束后只统计预热之后的样本，
	// Final 时为最终结果，与 Transfer.Latency 一致。
	Latency LatencyStats
}

type Options struct {
	Threads      int  // 多线程测速的并发连接数
	SingleThread bool // 另用单条连接各测一次下载与上传(Threads 为 1 时已是单线程，不再重复)
	Seconds      int  // 每个传输阶段的最长计量秒数(不含预热)，吞吐收敛后提前结束
	LatencyCount int
	Timeout      time.Duration // 单节点总超时，0 表示按 Budget 自动计算
	SkipDownload bool
	SkipUpload   bool
	OnProgress   func(ProgressUpdate)
	Dialer       Dialer // 连接节点使用的网卡，零值按系统路由

	// 预热时长：期间的数据不计入结果。下载等待 TCP 窗口增长，上传还需等待发送缓冲区填满。
	DownloadGrace time.Duration
	UploadGrace   time.Duration
}

// WithDefaults 返回为零值字段填充默认值后的副本。
func (o Options) WithDefaults() Options {
	if o.Threads < 1 {
		o.Threads = 8
	}
	if o.Seconds < 1 {
		o.Seconds = 10
	}
	if o.LatencyCount < 1 {
		o.LatencyCount = defaultLatencyCount
	}
	if o.DownloadGrace <= 0 {
		o.DownloadGrace = 1500 * time.Millisecond
	}
	if o.UploadGrace <= 0 {
		o.UploadGrace = 3 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = o.budget()
	}
	return o
}

// Budget 返回按当前参数完成一次测速所需的最长时间，可作为单节点总超时的下限。
func (o Options) Budget() time.Duration {
	return o.WithDefaults().budget()
}

// budget 假定各字段已填充默认值。
func (o Options) budget() time.Duration {
	budget := 20 * time.Second // 建连、鉴权、空载时延采样与连接收尾
	limit := time.Duration(o.Seconds) * time.Second
	for _, st := range o.stages() {
		budget += o.grace(st.Phase) + limit
	}
	return budget
}

func (o Options) grace(phase string) time.Duration {
	if phase == PhaseUpload {
		return o.UploadGrace
	}
	return o.DownloadGrace
}

// Stage 是一个传输阶段：方向与所用连接数。
type Stage struct {
	Phase   string // PhaseDownload 或 PhaseUpload
	Threads int
}

// Stages 返回按顺序执行的传输阶段：先下载后上传，每个方向先单线程再多线程。
func (o Options) Stages() []Stage { return o.WithDefaults().stages() }

// stages 假定各字段已填充默认值。
func (o Options) stages() []Stage {
	var out []Stage
	for _, phase := range []string{PhaseDownload, PhaseUpload} {
		if phase == PhaseDownload && o.SkipDownload || phase == PhaseUpload && o.SkipUpload {
			continue
		}
		if o.SingleThread && o.Threads > 1 {
			out = append(out, Stage{phase, 1})
		}
		out = append(out, Stage{phase, o.Threads})
	}
	return out
}

// Transfer 是一个传输方向的测量结果。
type Transfer struct {
	Threads  int           // 使用的连接数
	Mbps     float64       // 预热结束后的平均吞吐
	Bytes    int64         // 实际传输的总字节数(含预热)，用于统计流量消耗
	Total    time.Duration // 阶段总用时(含预热)，与 Bytes 口径一致
	Duration time.Duration // 计量时长(不含预热)，Mbps 按它计算
	Stable   bool          // 吞吐收敛后提前结束
	Latency  LatencyStats  // 传输期间的负载时延
	Errors   []string      // 各连接遇到的错误(已去重)
}

type Result struct {
	Node     Node
	Family   Family
	OK       bool
	Idle     LatencyStats // 空载时延
	Download Transfer     // 多线程(Options.Threads 条连接)下载
	Upload   Transfer

	// 单线程结果，仅在 Options.SingleThread 且 Threads > 1 时有值。
	DownloadSingle Transfer
	UploadSingle   Transfer

	Error string
}

// transfers 返回全部传输结果。
func (r Result) transfers() []Transfer {
	return []Transfer{r.Download, r.Upload, r.DownloadSingle, r.UploadSingle}
}

// Bytes 返回本次测速消耗的流量。
func (r Result) Bytes() int64 {
	var n int64
	for _, t := range r.transfers() {
		n += t.Bytes
	}
	return n
}

// RunTest 使用节点在指定协议族下的地址完成一次测速。
func RunTest(ctx context.Context, node Node, family Family, opts Options) Result {
	opts = opts.WithDefaults()
	res := Result{Node: node, Family: family}
	addr := node.Addr(family)
	if addr == "" {
		res.Error = "节点没有 " + family.String() + " 地址"
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	if err := reachable(ctx, opts.Dialer, addr, 1500*time.Millisecond); err != nil {
		res.Error = "端口不可达: " + err.Error()
		return res
	}

	// 先测空载时延：放在上下行之后会被尚未排空的上行队列拖高，并出现超时。
	res.Idle = measureLatency(ctx, opts.Dialer, addr, opts.LatencyCount, time.Second, func(s LatencyStats, fraction float64) {
		if opts.OnProgress != nil {
			opts.OnProgress(ProgressUpdate{Phase: PhaseLatency, Fraction: fraction, Latency: s})
		}
	})
	if opts.OnProgress != nil && ctx.Err() == nil { // 被中断时不报告"完成"
		opts.OnProgress(ProgressUpdate{Phase: PhaseLatency, Fraction: 1, Final: true, Latency: res.Idle})
	}
	if opts.SkipDownload && opts.SkipUpload {
		res.OK = res.Idle.Samples > 0
		if !res.OK {
			res.Error = "时延采样失败(节点无响应)"
		}
		return res
	}

	uuid, err := doValid(ctx, opts.Dialer, addr, newIMEI(), 4*time.Second)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() {
		// 测速被取消或超时后仍需释放节点名额。
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer rcancel()
		_ = release(rctx, opts.Dialer, addr, uuid)
	}()

	for _, st := range opts.stages() {
		if ctx.Err() != nil { // 已被中断或超时，不再开始新的阶段
			break
		}
		o := opts
		o.Threads = st.Threads
		mode, multi, single := transferDownload, &res.Download, &res.DownloadSingle
		if st.Phase == PhaseUpload {
			mode, multi, single = transferUpload, &res.Upload, &res.UploadSingle
		}
		if st.Threads == opts.Threads {
			*multi = measureTransfer(ctx, addr, mode, uuid, o)
		} else {
			*single = measureTransfer(ctx, addr, mode, uuid, o)
		}
	}

	var errs []string
	for _, t := range res.transfers() {
		errs = append(errs, t.Errors...)
		res.OK = res.OK || t.Mbps > 0
	}
	switch {
	case res.OK:
	case len(errs) > 0:
		res.Error = strings.Join(slices.Compact(errs), "; ")
	default:
		res.Error = "上下行均无数据返回"
	}
	return res
}
