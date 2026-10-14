package taier

import (
	"context"
	"slices"
	"strings"
	"time"
)

// ProgressUpdate 是传输阶段的进度回调。Warmup 期间的速率包含慢启动，不计入结果。
type ProgressUpdate struct {
	Phase   string
	Mbps    float64
	Warmup  bool
	Final   bool
	Elapsed time.Duration // 计量时长(不含预热)，仅 Final 时有效
	Stable  bool          // 吞吐收敛后提前结束，仅 Final 时有效
}

type Options struct {
	Threads      int
	Seconds      int // 每个传输阶段的最长计量秒数(不含预热)，吞吐收敛后提前结束
	Bandwidth    int
	LatencyCount int
	Timeout      time.Duration // 单节点总超时，0 表示按 Budget 自动计算
	IMEI         string
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
	if o.Bandwidth < 1 {
		o.Bandwidth = 200
	}
	if o.LatencyCount < 1 {
		o.LatencyCount = 8
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
	if !o.SkipDownload {
		budget += o.DownloadGrace + limit
	}
	if !o.SkipUpload {
		budget += o.UploadGrace + limit
	}
	return budget
}

// Transfer 是一个传输方向的测量结果。
type Transfer struct {
	Mbps     float64       // 预热结束后的平均吞吐
	Bytes    int64         // 实际传输的总字节数(含预热)，用于统计流量消耗
	Duration time.Duration // 计量时长(不含预热)
	Stable   bool          // 吞吐收敛后提前结束
	Latency  LatencyStats  // 传输期间的负载时延
	Errors   []string      // 各连接遇到的错误(已去重)
}

type Result struct {
	Node     Node
	Family   Family
	OK       bool
	IMEI     string
	Idle     LatencyStats // 空载时延
	Download Transfer
	Upload   Transfer
	Error    string
}

// Bytes 返回本次测速消耗的流量。
func (r Result) Bytes() int64 { return r.Download.Bytes + r.Upload.Bytes }

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
	res.Idle = measureLatency(ctx, opts.Dialer, addr, opts.LatencyCount, time.Second)
	if opts.SkipDownload && opts.SkipUpload {
		res.OK = res.Idle.Samples > 0
		if !res.OK {
			res.Error = "时延采样失败(节点无响应)"
		}
		return res
	}

	res.IMEI = opts.IMEI
	if res.IMEI == "" {
		res.IMEI = newIMEI()
	}
	uuid, err := doValid(ctx, opts.Dialer, addr, res.IMEI, opts.Bandwidth, 4*time.Second)
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

	if !opts.SkipDownload {
		res.Download = measureTransfer(ctx, addr, transferDownload, uuid, opts)
	}
	if !opts.SkipUpload {
		res.Upload = measureTransfer(ctx, addr, transferUpload, uuid, opts)
	}

	switch errs := slices.Concat(res.Download.Errors, res.Upload.Errors); {
	case res.Download.Mbps > 0 || res.Upload.Mbps > 0:
		res.OK = true
	case len(errs) > 0:
		res.Error = strings.Join(errs, "; ")
	default:
		res.Error = "上下行均无数据返回"
	}
	return res
}
