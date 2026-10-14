package taier

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"time"
)

// pinger 在一条 keep-alive 连接上反复发送轻量请求(GET /，节点返回 404)，
// 以"发出请求 → 收到响应头"的耗时作为应用层往返时延。
//
// 不用 TCP 建连耗时：经过透明代理(如 Surge 增强模式)时，握手在本地完成，任何地址都会在 1ms 内"连上"。
// 第一个请求只作预热：代理要等到首个请求才向上游建连，其耗时约为两倍往返。
type pinger struct {
	conn net.Conn
	br   *bufio.Reader
	req  []byte
	stop func() bool
}

func dialPinger(ctx context.Context, d Dialer, addr string, timeout time.Duration) (*pinger, error) {
	conn, err := d.dial(ctx, "tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	p := &pinger{
		conn: conn,
		br:   bufio.NewReader(conn),
		req:  []byte("GET / HTTP/1.1\r\nHost: " + addr + "\r\nUser-Agent: " + uaAndroid + "\r\n\r\n"),
		stop: context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) }),
	}
	_, reusable, err := p.ping(timeout)
	if err == nil && !reusable {
		err = errors.New("节点不支持长连接")
	}
	if err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

// ping 发送一次请求。err 为 nil 表示收到响应、rtt 有效；reusable 为 false 表示连接不能继续使用。
// 成功与否不能用 rtt > 0 判断：Windows 时钟精度较低，本机回环的往返可能测得 0。
func (p *pinger) ping(timeout time.Duration) (rtt time.Duration, reusable bool, err error) {
	_ = p.conn.SetDeadline(time.Now().Add(timeout))
	start := time.Now()
	if _, err := p.conn.Write(p.req); err != nil {
		return 0, false, err
	}
	resp, err := http.ReadResponse(p.br, nil)
	if err != nil {
		return 0, false, err
	}
	rtt = time.Since(start)
	_, drainErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
	_ = resp.Body.Close()
	return rtt, drainErr == nil && !resp.Close, nil
}

func (p *pinger) close() {
	p.stop()
	_ = p.conn.Close()
}

// sampler 在长连接上逐次测量往返时延；连接失效后自动重连(重连的预热请求不计入样本)。
type sampler struct {
	dialer  Dialer
	addr    string
	timeout time.Duration
	p       *pinger
}

// next 返回一次往返时延；ok 为 false 表示本次采样失败。
func (s *sampler) next(ctx context.Context) (rtt time.Duration, ok bool) {
	if s.p == nil {
		p, err := dialPinger(ctx, s.dialer, s.addr, s.timeout)
		if err != nil {
			return 0, false
		}
		s.p = p
	}
	rtt, reusable, err := s.p.ping(s.timeout)
	if err != nil || !reusable {
		s.close()
	}
	return rtt, err == nil
}

func (s *sampler) close() {
	if s.p != nil {
		s.p.close()
		s.p = nil
	}
}

// LatencyStats 汇总一组往返时延样本。
type LatencyStats struct {
	Samples int // 成功样本数
	Failed  int // 失败次数(超时、连接断开等)
	Min     time.Duration
	Median  time.Duration
	Max     time.Duration
	Jitter  time.Duration // 按时间顺序相邻样本差的绝对值平均
}

// FailRate 返回失败请求百分比。它不等同于丢包率：失败可能来自超时、连接断开等多种原因。
func (s LatencyStats) FailRate() float64 {
	total := s.Samples + s.Failed
	if total == 0 {
		return 0
	}
	return float64(s.Failed) / float64(total) * 100
}

// newLatencyStats 计算统计量；samples 须按采样时间排列，抖动依赖时间顺序。
func newLatencyStats(samples []time.Duration, failed int) LatencyStats {
	s := LatencyStats{Samples: len(samples), Failed: failed}
	if len(samples) == 0 {
		return s
	}
	if len(samples) > 1 {
		var sum time.Duration
		for i := 1; i < len(samples); i++ {
			sum += (samples[i] - samples[i-1]).Abs()
		}
		s.Jitter = sum / time.Duration(len(samples)-1)
	}
	sorted := slices.Sorted(slices.Values(samples))
	mid := len(sorted) / 2
	s.Min, s.Median, s.Max = sorted[0], sorted[mid], sorted[len(sorted)-1]
	if len(sorted)%2 == 0 {
		s.Median = (sorted[mid-1] + sorted[mid]) / 2
	}
	return s
}

// measureLatency 采样 count 次空载往返时延。
func measureLatency(ctx context.Context, d Dialer, addr string, count int, timeout time.Duration) LatencyStats {
	if count < 1 {
		count = 8
	}
	s := sampler{dialer: d, addr: addr, timeout: timeout}
	defer s.close()
	var samples []time.Duration
	failed := 0
	for i := range count {
		rtt, ok := s.next(ctx)
		switch {
		case ok:
			samples = append(samples, rtt)
		case ctx.Err() == nil:
			failed++
		}
		if i < count-1 && !sleepCtx(ctx, 80*time.Millisecond) {
			break
		}
	}
	return newLatencyStats(samples, failed)
}

const (
	loadedInterval   = 100 * time.Millisecond
	loadedTimeout    = 2 * time.Second
	maxLoadedSamples = 512
)

// loadedProbe 在传输期间持续测量往返时延(负载时延)，反映链路排队与缓冲膨胀的程度。
type loadedProbe struct {
	cancel  context.CancelFunc
	done    chan struct{}
	samples []timedSample // 仅由采样 goroutine 写入，done 关闭后读取
}

type timedSample struct {
	at  time.Time
	rtt time.Duration
	ok  bool
}

func startLoadedProbe(ctx context.Context, d Dialer, addr string) *loadedProbe {
	ctx, cancel := context.WithCancel(ctx)
	lp := &loadedProbe{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(lp.done)
		s := sampler{dialer: d, addr: addr, timeout: loadedTimeout}
		defer s.close()
		for len(lp.samples) < maxLoadedSamples {
			at := time.Now()
			rtt, ok := s.next(ctx)
			if ctx.Err() != nil {
				return // 结束时被打断的采样不计入
			}
			lp.samples = append(lp.samples, timedSample{at: at, rtt: rtt, ok: ok})
			if !sleepCtx(ctx, loadedInterval-time.Since(at)) {
				return
			}
		}
	}()
	return lp
}

// stop 结束采样，只统计 since(预热结束)之后发起的样本。
func (lp *loadedProbe) stop(since time.Time) LatencyStats {
	lp.cancel()
	<-lp.done
	var samples []time.Duration
	failed := 0
	for _, s := range lp.samples {
		switch {
		case s.at.Before(since):
		case s.ok:
			samples = append(samples, s.rtt)
		default:
			failed++
		}
	}
	return newLatencyStats(samples, failed)
}
