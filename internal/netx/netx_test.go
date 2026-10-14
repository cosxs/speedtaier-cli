package netx

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
)

func TestIsTUN(t *testing.T) {
	tests := []struct {
		flags net.Flags
		local string
		want  bool
	}{
		{net.FlagUp | net.FlagPointToPoint, "10.0.0.2", true}, // utun/tun
		{net.FlagUp | net.FlagBroadcast, "198.18.0.1", true},  // 虚拟地址段
		{net.FlagUp | net.FlagBroadcast, "192.168.1.10", false},
	}
	for _, tt := range tests {
		if got := isTUN(tt.flags, netip.MustParseAddr(tt.local)); got != tt.want {
			t.Errorf("isTUN(%v, %s) = %v, want %v", tt.flags, tt.local, got, tt.want)
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
	if err != nil || !physical(ifi) {
		t.Errorf("PrimaryInterface() = %q, not a physical interface (%v)", name, err)
	}
	t.Logf("primary interface: %s", name)
}

func TestDetectTUNEnvironment(t *testing.T) {
	name, ok := DetectTUN(netip.MustParseAddr("119.164.225.30"))
	t.Logf("DetectTUN: interface=%q tun=%v", name, ok)
}
