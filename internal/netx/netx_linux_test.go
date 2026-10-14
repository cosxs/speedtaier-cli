package netx

import (
	"net"
	"os"
	"testing"
)

// 按真实的 /sys/class/net 检查本机网卡：回环不是 TUN；带 tun_flags 的 TUN/TAP 设备必须识别为 TUN。
func TestIsTunDeviceSysfs(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for i := range ifaces {
		ifi := &ifaces[i]
		got := isTunDevice(ifi)
		_, statErr := os.Stat("/sys/class/net/" + ifi.Name + "/tun_flags")
		t.Logf("%-12s flags=%-40v tun=%v", ifi.Name, ifi.Flags, got)
		switch {
		case ifi.Flags&net.FlagLoopback != 0 && got:
			t.Errorf("loopback %s detected as TUN", ifi.Name)
		case statErr == nil && !got:
			t.Errorf("TUN/TAP device %s not detected", ifi.Name)
		}
	}
}
