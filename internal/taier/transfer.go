package taier

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	uploadBoundary    = "00content0boundary00"
	uploadChunkSize   = 128 << 10
	uploadDeclaredLen = 900_000_000

	sampleInterval    = 250 * time.Millisecond // 采样与进度刷新间隔
	windowDuration    = time.Second            // 收敛判定使用的吞吐窗口
	stableWindows     = 3                      // 连续多少个窗口稳定即提前结束
	stableTolerance   = 0.05                   // 各窗口吞吐相对其均值的允许偏差
	minMeasure        = 3 * time.Second        // 最短计量时长
	streamStagger     = 200 * time.Millisecond // 相邻连接的启动间隔
	streamRetryDelay  = 200 * time.Millisecond
	maxStreamFailures = 3 // 单条连接连续失败多少次后放弃
)

type transferMode uint8

const (
	transferDownload transferMode = iota
	transferUpload
)

func (m transferMode) String() string {
	if m == transferUpload {
		return "上传"
	}
	return "下载"
}

// measureTransfer 用 opts.Threads 条连接持续传输并计量吞吐：
//   - 预热期的数据不计入结果(下载等待 TCP 窗口增长，上传还需等待发送缓冲区填满)；
//   - 预热后每秒统计一次窗口吞吐，连续 stableWindows 个窗口偏差都在 stableTolerance 以内即提前结束，
//     计量时长最短 minMeasure、最长 opts.Seconds 秒；
//   - 传输期间另开一条连接持续采样负载时延。
func measureTransfer(parent context.Context, addr string, mode transferMode, uuid string, opts Options) Transfer {
	grace, transfer := opts.DownloadGrace, runDownloadConn
	if mode == transferUpload {
		grace, transfer = opts.UploadGrace, runUploadConn
	}
	limit := time.Duration(opts.Seconds) * time.Second
	ctx, cancel := context.WithTimeout(parent, grace+limit+5*time.Second)
	defer cancel()

	var (
		counter atomic.Int64
		errs    errorLog
		wg      sync.WaitGroup
	)
	start := time.Now()
	// 错峰启动，避免所有连接同时慢启动、同时结束；全部连接须在预热期内启动完毕。
	stagger := min(streamStagger, grace/time.Duration(opts.Threads))
	for i := range opts.Threads {
		wg.Go(func() {
			runStream(ctx, time.Duration(i)*stagger, &errs, func(ctx context.Context) (int64, error) {
				return transfer(ctx, opts.Dialer, addr, uuid, &counter)
			})
		})
	}
	streamsDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(streamsDone)
	}()
	probe := startLoadedProbe(ctx, opts.Dialer, addr)

	progress := func(u ProgressUpdate) {
		if opts.OnProgress != nil {
			u.Phase = mode.String()
			opts.OnProgress(u)
		}
	}
	m := meter{start: start, grace: grace, min: minMeasure, max: limit}
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ticker.C:
			mbps, warmup, stop := m.observe(time.Now(), counter.Load())
			if stop {
				break loop
			}
			progress(ProgressUpdate{Mbps: mbps, Warmup: warmup})
		case <-streamsDone: // 所有连接都已放弃
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	mbps, measured, stable := m.result(time.Now(), counter.Load())
	cancel()
	<-streamsDone
	loaded := probe.stop(m.measureStart())
	progress(ProgressUpdate{Mbps: mbps, Final: true, Elapsed: measured, Stable: stable})
	return Transfer{
		Mbps:     mbps,
		Bytes:    counter.Load(),
		Duration: measured,
		Stable:   stable,
		Latency:  loaded,
		Errors:   errs.list(),
	}
}

// meter 根据累计字节数的定时采样决定预热结束、窗口吞吐与停止时机。
type meter struct {
	start    time.Time
	grace    time.Duration
	min, max time.Duration

	warm      bool      // 预热已结束
	base      time.Time // 计量起点
	baseBytes int64
	winStart  time.Time
	winBytes  int64
	windows   []float64 // 各窗口吞吐(Mbps)
	stable    bool
}

// observe 记录一次采样，返回用于展示的速率、是否仍在预热、是否应停止。
func (m *meter) observe(now time.Time, bytes int64) (mbps float64, warmup, stop bool) {
	if !m.warm {
		if now.Sub(m.start) >= m.grace {
			// 预热结束，以此刻为计量起点；至今未收到任何数据(极慢的连接)时从头计量。
			m.warm = true
			m.base, m.baseBytes = now, bytes
			if bytes == 0 {
				m.base = m.start
			}
			m.winStart, m.winBytes = m.base, m.baseBytes
		}
		return rateMbps(bytes, now.Sub(m.start)), true, false
	}
	if now.Sub(m.winStart) >= windowDuration {
		m.windows = append(m.windows, rateMbps(bytes-m.winBytes, now.Sub(m.winStart)))
		m.winStart, m.winBytes = now, bytes
	}
	measured := now.Sub(m.base)
	m.stable = measured >= m.min && converged(m.windows)
	return rateMbps(bytes-m.baseBytes, measured), false, m.stable || measured >= m.max
}

// result 返回计量起点到 now 的平均吞吐；预热未结束(如连接全部失败)时按全程计算。
func (m *meter) result(now time.Time, bytes int64) (mbps float64, measured time.Duration, stable bool) {
	if !m.warm {
		return rateMbps(bytes, now.Sub(m.start)), now.Sub(m.start), false
	}
	measured = now.Sub(m.base)
	return rateMbps(bytes-m.baseBytes, measured), measured, m.stable
}

// measureStart 返回计量起点，用于筛选预热结束后的负载时延样本。
func (m *meter) measureStart() time.Time {
	if m.warm {
		return m.base
	}
	return m.start
}

// converged 判断最近 stableWindows 个窗口的吞吐是否都在其均值的 ±stableTolerance 以内。
func converged(windows []float64) bool {
	if len(windows) < stableWindows {
		return false
	}
	last := windows[len(windows)-stableWindows:]
	var sum float64
	for _, w := range last {
		sum += w
	}
	mean := sum / float64(len(last))
	if mean <= 0 {
		return false
	}
	for _, w := range last {
		if math.Abs(w-mean) > stableTolerance*mean {
			return false
		}
	}
	return true
}

func rateMbps(bytes int64, elapsed time.Duration) float64 {
	if bytes <= 0 || elapsed <= 0 {
		return 0
	}
	return float64(bytes) * 8 / elapsed.Seconds() / 1e6
}

// runStream 在 ctx 结束前反复执行 transfer：正常结束(文件传完)立即重连，失败稍后重试，
// 连续失败 maxStreamFailures 次后放弃这条连接。
func runStream(ctx context.Context, delay time.Duration, errs *errorLog, transfer func(context.Context) (int64, error)) {
	if !sleepCtx(ctx, delay) {
		return
	}
	for failures := 0; failures < maxStreamFailures; {
		n, err := transfer(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil && n > 0 {
			failures = 0
			continue
		}
		if err == nil {
			err = errors.New("连接未传输任何数据")
		}
		errs.add(err)
		failures++
		if !sleepCtx(ctx, streamRetryDelay) {
			return
		}
	}
}

// sleepCtx 等待 d 或 ctx 结束，返回 ctx 是否仍然有效。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// errorLog 汇总各连接的错误，按首次出现顺序去重计数。
type errorLog struct {
	mu     sync.Mutex
	order  []string
	counts map[string]int
}

func (l *errorLog) add(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg := err.Error()
	if l.counts == nil {
		l.counts = make(map[string]int)
	}
	if l.counts[msg] == 0 {
		l.order = append(l.order, msg)
	}
	l.counts[msg]++
}

func (l *errorLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, msg := range l.order {
		if n := l.counts[msg]; n > 1 {
			msg = fmt.Sprintf("%s (×%d)", msg, n)
		}
		out = append(out, msg)
	}
	return out
}

// runDownloadConn 下载一次测速文件，返回本连接收到的字节数；文件读完时 err 为 nil。
func runDownloadConn(ctx context.Context, d Dialer, addr, uuid string, counter *atomic.Int64) (int64, error) {
	conn, err := dialNode(ctx, d, addr)
	if err != nil {
		return 0, fmt.Errorf("下载连接失败: %w", err)
	}
	// 只关闭连接、不调用 resp.Body.Close()：后者会把剩余的测速文件读完。
	defer conn.Close()
	stop := watchContext(ctx, conn)
	defer stop()

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	path := "/speed/File(1G).dl?" + url.Values{"key": {uuid}, "r": {ts}}.Encode()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nAccept: */*\r\nConnection: close\r\nUser-Agent: %s\r\n\r\n",
		path, addr, uaDownload)
	if _, err := io.WriteString(conn, req); err != nil {
		return 0, fmt.Errorf("下载请求发送失败: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReaderSize(conn, 64<<10), nil)
	if err != nil {
		return 0, fmt.Errorf("下载响应读取失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("下载响应异常: HTTP %d", resp.StatusCode)
	}

	buf := make([]byte, 64<<10)
	var total int64
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			counter.Add(int64(n))
		}
		switch {
		case err == io.EOF:
			return total, nil
		case err != nil:
			return total, fmt.Errorf("下载中断: %w", err)
		}
	}
}

// uploadChunk 是上传使用的随机数据块，避免链路压缩影响结果。
var uploadChunk = sync.OnceValue(func() []byte {
	buf := make([]byte, uploadChunkSize)
	_, _ = rand.Read(buf)
	return buf
})

// runUploadConn 上传一次测速数据，返回本连接写出的字节数；写满声明长度时 err 为 nil。
func runUploadConn(ctx context.Context, d Dialer, addr, uuid string, counter *atomic.Int64) (int64, error) {
	conn, err := dialNode(ctx, d, addr)
	if err != nil {
		return 0, fmt.Errorf("上传连接失败: %w", err)
	}
	defer conn.Close()
	stop := watchContext(ctx, conn)
	defer stop()

	fname := "SPEED_" + time.Now().Format("20060102_150405")
	preamble := fmt.Sprintf("--%s\r\nContent-Disposition: form-data; name=\"upload\";filename=\"%s\"\r\n\r\n",
		uploadBoundary, fname)
	header := fmt.Sprintf("POST /speed/doAnalsLoad.do HTTP/1.1\r\n"+
		"Host: %s\r\nConnection: close\r\nCache-Control: no-cache\r\nCharset: UTF-8\r\n"+
		"Key: %s\r\nContent-Type: multipart/form-data;boundary=%s\r\n"+
		"User-Agent: %s\r\nAccept-Encoding: gzip\r\nContent-Length: %d\r\n\r\n",
		addr, uuid, uploadBoundary, uaAndroid, uploadDeclaredLen)
	if _, err := io.WriteString(conn, header+preamble); err != nil {
		return 0, fmt.Errorf("上传请求发送失败: %w", err)
	}

	// 声明的 Content-Length 包含 multipart 前导，文件数据只能写满剩余长度。
	chunk := uploadChunk()
	remaining := int64(uploadDeclaredLen - len(preamble))
	var total int64
	for remaining > 0 {
		n, err := conn.Write(chunk[:min(int64(len(chunk)), remaining)])
		total += int64(n)
		remaining -= int64(n)
		counter.Add(int64(n))
		if err != nil {
			return total, fmt.Errorf("上传中断: %w", err)
		}
	}
	return total, nil
}

// dialNode 建立测速连接，并设置 SO_LINGER=0：关闭时发送 RST、丢弃尚未发出的数据。
// 否则内核发送缓冲区和透明代理(如 Surge 增强模式，可积压数百 MB)会在测速结束后继续上传，
// 长时间占满上行并拖垮后续节点的测速。
func dialNode(ctx context.Context, d Dialer, addr string) (net.Conn, error) {
	conn, err := d.dial(ctx, "tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	return conn, nil
}

// watchContext 让 conn 上阻塞的读写在 ctx 结束时立即返回。
func watchContext(ctx context.Context, conn net.Conn) (stop func() bool) {
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	return context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
}
