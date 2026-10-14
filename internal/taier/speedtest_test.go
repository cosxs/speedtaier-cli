package taier

import (
	"context"
	"net"
	"net/netip"
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
		mu     sync.Mutex
		finals []ProgressUpdate
	)
	opts := Options{
		Threads:       2,
		Seconds:       1, // 小于 minMeasure，按上限 1 秒结束
		LatencyCount:  3,
		DownloadGrace: 300 * time.Millisecond,
		UploadGrace:   300 * time.Millisecond,
		OnProgress: func(u ProgressUpdate) {
			if u.Final {
				mu.Lock()
				finals = append(finals, u)
				mu.Unlock()
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
	if len(finals) != 2 || finals[0].Phase != "下载" || finals[1].Phase != "上传" {
		t.Errorf("final progress updates = %+v", finals)
	}
	if n := fn.Released.Load(); n != 1 {
		t.Errorf("released %d times, want 1", n)
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
