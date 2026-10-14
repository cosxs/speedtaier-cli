package netx

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
)

func TestIsTUN(t *testing.T) {
	p2p := net.FlagUp | net.FlagPointToPoint
	tests := []struct {
		name  string
		flags net.Flags
		local string
		want  bool
	}{
		{"utun4", p2p, "10.0.0.2", true}, // Surge、VPN 的 utun
		{"tun0", p2p, "10.0.0.2", true},
		{"pppoe-wan", p2p, "100.64.1.2", false}, // PPPoE 拨号的 WAN 口不是代理
		{"ppp0", p2p, "100.64.1.2", false},
		{"en0", net.FlagUp | net.FlagBroadcast, "198.18.0.1", true}, // 源地址在虚拟地址段
		{"eth0", net.FlagUp | net.FlagBroadcast, "192.168.1.10", false},
	}
	for _, tt := range tests {
		ifi := &net.Interface{Name: tt.name, Flags: tt.flags}
		if got := isTUN(ifi, netip.MustParseAddr(tt.local)); got != tt.want {
			t.Errorf("isTUN(%s, %s) = %v, want %v", tt.name, tt.local, got, tt.want)
		}
	}
}

func TestBindControlLoopback(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var lo string
	ifaces, _ := net.Interfaces()
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 {
			lo = ifi.Name
		}
	}
	control, err := BindControl(lo)
	if err != nil {
		t.Fatal(err)
	}
	d := net.Dialer{Control: control}
	conn, err := d.Dial("tcp4", ln.Addr().String())
	switch {
	case errors.Is(err, ErrUnsupported), errors.Is(err, syscall.EPERM):
		t.Skipf("binding unavailable here: %v", err)
	case err != nil:
		t.Fatalf("dial bound to %s: %v", lo, err)
	}
	conn.Close()

	if _, err := BindControl("no-such-interface0"); err == nil {
		t.Error("unknown interface: want error")
	}
}

func TestPrimaryInterface(t *testing.T) {
	name, err := PrimaryInterface()
	if err != nil {
		t.Skipf("no primary interface here: %v", err)
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil || !usable(ifi) {
		t.Errorf("PrimaryInterface() = %q, not a usable interface (%v)", name, err)
	}
	t.Logf("primary interface: %s", name)
}

func TestDetectTUNEnvironment(t *testing.T) {
	name, ok := DetectTUN(netip.MustParseAddr("119.164.225.30"))
	t.Logf("DetectTUN: interface=%q tun=%v", name, ok)
}
