package taier

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlanTests(t *testing.T) {
	ms := time.Millisecond
	v4, v6 := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")
	cand := func(name, op string, r4, r6 Reach) Candidate {
		n := Node{Name: name, Operator: op, IPv4: v4}
		if r6.State != ReachNoAddr {
			n.IPv6 = v6
		}
		return Candidate{Node: n, V4: r4, V6: r6}
	}
	ok := func(d time.Duration) Reach { return Reach{State: ReachOK, RTT: d} }
	none := Reach{State: ReachNoAddr}
	failed := Reach{State: ReachFailed}

	cands := []Candidate{
		cand("南京移动", "移动", ok(30*ms), ok(9*ms)),
		cand("南通移动", "移动", ok(12*ms), none),
		cand("无锡移动", "移动", failed, none),
		cand("南京电信", "电信", ok(8*ms), failed),
		cand("其他节点", "", ok(5*ms), none),
	}
	groups := PlanTests(cands, IPv4, IPv6)

	type want struct {
		op    string
		fam   Family
		nodes []string
	}
	wants := []want{
		{"电信", IPv4, []string{"南京电信"}},
		{"电信", IPv6, nil}, // 有地址但不可达，生成空分组以报告失败
		{"移动", IPv4, []string{"南通移动", "南京移动"}},
		{"移动", IPv6, []string{"南京移动"}},
		{"", IPv4, []string{"其他节点"}},
	}
	if len(groups) != len(wants) {
		t.Fatalf("got %d groups, want %d: %+v", len(groups), len(wants), groups)
	}
	for i, w := range wants {
		g := groups[i]
		var names []string
		for _, n := range g.Nodes {
			names = append(names, n.Name)
		}
		if g.Operator != w.op || g.Family != w.fam || len(names) != len(w.nodes) {
			t.Errorf("group %d = %s/%s %v, want %s/%s %v", i, g.Operator, g.Family, names, w.op, w.fam, w.nodes)
			continue
		}
		for j := range names {
			if names[j] != w.nodes[j] {
				t.Errorf("group %d nodes = %v, want %v", i, names, w.nodes)
				break
			}
		}
	}
}

func TestProbe(t *testing.T) {
	// 正常节点：HTTP keep-alive，GET / 返回 404。
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	// 端口开着但不响应测速协议(类似部分 9443 节点)：接受连接后立即关闭。
	silent, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	lo := netip.MustParseAddr("127.0.0.1")
	port := func(addr net.Addr) string { return strconv.Itoa(addr.(*net.TCPAddr).Port) }
	nodes := []Node{
		{Name: "up", IPv4: lo, Port: port(srv.Listener.Addr()), IPv6: netip.MustParseAddr("2001:db8::1")},
		{Name: "silent", IPv4: lo, Port: port(silent.Addr())},
	}
	cands := Probe(context.Background(), Dialer{}, nodes, IPv4)

	if r := cands[0].V4; r.State != ReachOK || r.RTT < 0 {
		t.Errorf("up.V4 = %+v, want reachable", r)
	}
	if r := cands[0].V6; r.State != ReachSkipped {
		t.Errorf("up.V6 = %+v, want skipped (IPv6 not requested)", r)
	}
	if r := cands[1].V4; r.State != ReachFailed {
		t.Errorf("silent.V4 = %+v, want failed", r)
	}
	if r := cands[1].V6; r.State != ReachNoAddr {
		t.Errorf("silent.V6 = %+v, want no address", r)
	}
}

func TestMeasureLatency(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	addr := srv.Listener.Addr().String()
	st := measureLatency(context.Background(), Dialer{}, addr, 5, time.Second)
	// 不要求时延大于 0：Windows 时钟精度较低，本机回环的往返可能测得 0。
	if st.Samples != 5 || st.Failed != 0 || st.Min < 0 || st.Min > st.Median || st.Median > st.Max {
		t.Errorf("got %+v, want 5 samples without failures", st)
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("used %d connections, want 1 (keep-alive)", n)
	}

	srv.Close()
	if st := measureLatency(context.Background(), Dialer{}, addr, 3, 200*time.Millisecond); st.Samples != 0 || st.FailRate() != 100 {
		t.Errorf("closed server: %+v, want 100%% failures", st)
	}
}

// 节点每次响应后都关闭连接时，无法在长连接上测往返时延：探测判为不可达，时延采样全部失败。
func TestPingerRequiresKeepAlive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		http.NotFound(w, r)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	if _, err := dialPinger(context.Background(), Dialer{}, addr, time.Second); err == nil {
		t.Error("dialPinger: want error for non keep-alive node")
	}
	if st := measureLatency(context.Background(), Dialer{}, addr, 2, time.Second); st.Samples != 0 || st.Failed != 2 {
		t.Errorf("measureLatency = %+v, want all failed", st)
	}
	if r := probeAddr(context.Background(), Dialer{}, addr); r.State != ReachFailed {
		t.Errorf("probeAddr = %+v, want failed", r)
	}
}
