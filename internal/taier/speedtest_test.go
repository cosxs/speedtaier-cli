package taier

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/fakenode"
)

func fakeNode(t *testing.T, busy bool) (Node, *fakenode.Node) {
	fn := fakenode.Start(t, fakenode.Options{Busy: busy})
	return Node{Name: "fake", IPv4: netip.MustParseAddr(fn.Host), Port: fn.Port}, fn
}

func TestRunTestAgainstFakeNode(t *testing.T) {
	node, fn := fakeNode(t, false)
	var (
		mu      sync.Mutex
		finals  []ProgressUpdate
		updates = map[string][]ProgressUpdate{}
	)
	opts := Options{
		Threads:       2,
		Seconds:       1, // 小于 minMeasure，按上限 1 秒结束
		LatencyCount:  3,
		DownloadGrace: 300 * time.Millisecond,
		UploadGrace:   300 * time.Millisecond,
		OnProgress: func(u ProgressUpdate) {
			mu.Lock()
			defer mu.Unlock()
			updates[u.Phase] = append(updates[u.Phase], u)
			if u.Final {
				finals = append(finals, u)
			}
		},
	}
	res := RunTest(context.Background(), node, IPv4, opts)

	if !res.OK || res.Error != "" {
		t.Fatalf("RunTest failed: %+v", res)
	}
	if res.Idle.Samples != 3 || res.Idle.Failed != 0 {
		t.Errorf("idle latency = %+v, want 3 samples", res.Idle)
	}
	for name, tr := range map[string]Transfer{"download": res.Download, "upload": res.Upload} {
		if tr.Mbps <= 0 || tr.Bytes <= 0 || len(tr.Errors) != 0 {
			t.Errorf("%s = %+v", name, tr)
		}
		if tr.Duration < time.Second || tr.Duration > time.Second+2*sampleInterval {
			t.Errorf("%s measured %v, want about 1s", name, tr.Duration)
		}
		if tr.Latency.Samples == 0 {
			t.Errorf("%s loaded latency has no samples", name)
		}
	}
	if res.Bytes() != res.Download.Bytes+res.Upload.Bytes {
		t.Errorf("Bytes() = %d", res.Bytes())
	}
	if len(finals) != 3 || finals[0].Phase != PhaseLatency || finals[0].Latency.Samples != 3 ||
		finals[1].Phase != PhaseDownload || finals[2].Phase != PhaseUpload {
		t.Errorf("final progress updates = %+v", finals)
	}
	if n := fn.Released.Load(); n != 1 {
		t.Errorf("released %d times, want 1", n)
	}

	// 空载时延每完成一次采样回调一次(3 次采样 + 1 次最终结果)，进度单调递增。
	if lat := updates[PhaseLatency]; len(lat) != 4 || lat[0].Latency.Samples != 1 || lat[2].Latency.Samples != 3 ||
		lat[2].Fraction != 1 || !lat[3].Final {
		t.Errorf("latency updates = %+v", lat)
	}
	for phase, final := range map[string]Transfer{PhaseDownload: res.Download, PhaseUpload: res.Upload} {
		ups := updates[phase]
		if len(ups) < 3 {
			t.Fatalf("%s: got %d updates, want several", phase, len(ups))
		}
		// 累计字节数与总用时(含预热，与字节数口径一致)只增不减，最终值等于结果中的值；
		// 计量时长(不含预热)只在最终事件中给出。
		var prevBytes int64
		var prevTotal time.Duration
		for _, u := range ups {
			if u.Bytes < prevBytes || u.Total < prevTotal || (!u.Final && u.Elapsed != 0) {
				t.Errorf("%s: bytes %d (after %d), total %v (after %v), elapsed %v", phase, u.Bytes, prevBytes, u.Total, prevTotal, u.Elapsed)
			}
			prevBytes, prevTotal = u.Bytes, u.Total
		}
		if last := ups[len(ups)-1]; last.Bytes != final.Bytes || last.Total != final.Total || last.Elapsed != final.Duration {
			t.Errorf("%s: final update bytes %d total %v elapsed %v; result bytes %d total %v duration %v",
				phase, last.Bytes, last.Total, last.Elapsed, final.Bytes, final.Total, final.Duration)
		}
		if final.Total <= final.Duration {
			t.Errorf("%s: total %v should include the warmup, but duration is %v", phase, final.Total, final.Duration)
		}
		prev, seen := 0.0, false
		for _, u := range ups {
			if u.Fraction < prev || u.Fraction > 1 {
				t.Errorf("%s: fraction %v after %v", phase, u.Fraction, prev)
			}
			prev = u.Fraction
			// 负载时延一旦出现就不会再回到空白(预热刚结束、还没有预热之后的样本时沿用全部样本)。
			if seen && u.Latency.Samples == 0 {
				t.Errorf("%s: loaded latency disappeared after it first appeared: %+v", phase, ups)
			}
			seen = seen || u.Latency.Samples > 0
		}
		last := ups[len(ups)-1]
		if !last.Final || last.Fraction != 1 || last.Latency.Samples == 0 {
			t.Errorf("%s: final update = %+v", phase, last)
		}
	}
}

// 负载时延在预热期间就开始展示(到目前为止的全部样本)，而不是等预热结束；最终结果仍只统计预热之后的样本。
func TestLoadedLatencyShownDuringWarmup(t *testing.T) {
	node, _ := fakeNode(t, false)
	var updates []ProgressUpdate
	res := RunTest(context.Background(), node, IPv4, Options{
		Threads: 1, Seconds: 1, LatencyCount: 1, SkipUpload: true,
		DownloadGrace: time.Second,
		OnProgress: func(u ProgressUpdate) {
			if u.Phase == PhaseDownload {
				updates = append(updates, u)
			}
		},
	})
	if !res.OK {
		t.Fatalf("RunTest failed: %+v", res)
	}
	warmupWithLatency := 0
	for _, u := range updates {
		if u.Warmup && u.Latency.Samples > 0 {
			warmupWithLatency++
		}
	}
	if warmupWithLatency == 0 {
		t.Errorf("no warmup update carried loaded latency: %+v", updates)
	}
	// 最终结果不包含预热期的样本：样本数少于整个阶段(约每 100 毫秒一个)的采样总数。
	last := updates[len(updates)-1]
	if !last.Final || last.Latency != res.Download.Latency {
		t.Errorf("final update latency %+v, result latency %+v", last.Latency, res.Download.Latency)
	}
	allSamples := 0
	for _, u := range updates {
		if u.Warmup {
			allSamples = max(allSamples, u.Latency.Samples)
		}
	}
	if allSamples == 0 || res.Download.Latency.Samples == 0 {
		t.Errorf("warmup samples %d, final samples %d", allSamples, res.Download.Latency.Samples)
	}
}

// 被中断后不再开始新的阶段，被打断的阶段也不报告"完成"，界面据此保留中断时的真实进度。
func TestRunTestInterrupted(t *testing.T) {
	node, _ := fakeNode(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type update struct {
		phase   string
		threads int
		final   bool
	}
	var updates []update
	opts := Options{
		Threads: 2, SingleThread: true, Seconds: 2, LatencyCount: 2,
		DownloadGrace: 300 * time.Millisecond, UploadGrace: 300 * time.Millisecond,
		OnProgress: func(u ProgressUpdate) {
			updates = append(updates, update{u.Phase, u.Threads, u.Final})
			if u.Phase == PhaseDownload && !u.Final && u.Bytes > 0 { // 单线程下载进行中被中断
				cancel()
			}
		},
	}
	RunTest(ctx, node, IPv4, opts)

	for _, u := range updates {
		switch {
		case u.phase == PhaseLatency:
		case u.phase == PhaseDownload && u.threads == 1 && !u.final:
		default:
			t.Errorf("unexpected update after interruption: %+v (all: %+v)", u, updates)
		}
	}

	// 时延采样阶段被中断同样不报告"完成"。
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var latency []ProgressUpdate
	RunTest(ctx2, node, IPv4, Options{Threads: 1, LatencyCount: 5, OnProgress: func(u ProgressUpdate) {
		latency = append(latency, u)
		cancel2()
	}})
	for _, u := range latency {
		if u.Final || u.Phase != PhaseLatency {
			t.Errorf("update after interruption during latency sampling: %+v", u)
		}
	}
}

// 加测单线程时，每个方向先用单条连接、再用多条连接，结果分别记录。
func TestRunTestSingleThread(t *testing.T) {
	node, fn := fakeNode(t, false)
	type stage struct {
		phase   string
		threads int
	}
	var finals []stage
	opts := Options{
		Threads: 3, SingleThread: true, Seconds: 1, LatencyCount: 2,
		DownloadGrace: 200 * time.Millisecond, UploadGrace: 200 * time.Millisecond,
		OnProgress: func(u ProgressUpdate) {
			if u.Final {
				finals = append(finals, stage{u.Phase, u.Threads})
			}
		},
	}
	res := RunTest(context.Background(), node, IPv4, opts)

	if !res.OK {
		t.Fatalf("RunTest failed: %+v", res)
	}
	want := []stage{{PhaseLatency, 0}, {PhaseDownload, 1}, {PhaseDownload, 3}, {PhaseUpload, 1}, {PhaseUpload, 3}}
	if !slices.Equal(finals, want) {
		t.Errorf("final updates = %v, want %v", finals, want)
	}
	for name, tt := range map[string]struct {
		tr      Transfer
		threads int
	}{
		"download": {res.Download, 3}, "upload": {res.Upload, 3},
		"download single": {res.DownloadSingle, 1}, "upload single": {res.UploadSingle, 1},
	} {
		if tt.tr.Threads != tt.threads || tt.tr.Mbps <= 0 || tt.tr.Latency.Samples == 0 {
			t.Errorf("%s = %+v, want %d threads with data", name, tt.tr, tt.threads)
		}
	}
	if want := res.Download.Bytes + res.Upload.Bytes + res.DownloadSingle.Bytes + res.UploadSingle.Bytes; res.Bytes() != want {
		t.Errorf("Bytes() = %d, want %d", res.Bytes(), want)
	}
	if n := fn.Released.Load(); n != 1 {
		t.Errorf("released %d times, want 1 (one session for all stages)", n)
	}
}

func TestRunTestRejectedByNode(t *testing.T) {
	node, _ := fakeNode(t, true)
	res := RunTest(context.Background(), node, IPv4, Options{LatencyCount: 1})
	if res.OK || !strings.Contains(res.Error, errNodeBusy.Error()) || res.Bytes() != 0 {
		t.Errorf("busy node: %+v", res)
	}
}

func TestRunTestWithBoundDialer(t *testing.T) {
	var lo string
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 {
			lo = ifi.Name
		}
	}
	d, err := NewDialer(lo)
	if err != nil {
		t.Fatal(err)
	}
	node, _ := fakeNode(t, false)
	if err := d.Check(node.IPv4); err != nil {
		t.Skipf("binding unavailable here: %v", err)
	}
	res := RunTest(context.Background(), node, IPv4, Options{
		Threads: 1, Seconds: 1, LatencyCount: 2, Dialer: d,
		DownloadGrace: 200 * time.Millisecond, UploadGrace: 200 * time.Millisecond,
	})
	if !res.OK || res.Download.Mbps <= 0 || res.Upload.Mbps <= 0 {
		t.Errorf("bound RunTest: %+v", res)
	}
	if _, err := NewDialer("no-such-interface0"); err == nil {
		t.Error("NewDialer(unknown): want error")
	}
}
