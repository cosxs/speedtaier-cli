// Package netx 提供测速连接所需的平台相关网络能力：把 socket 绑定到网卡、查找主物理网卡、检测透明代理。
//
// 透明代理(Surge 增强模式、Clash/sing-box TUN 等)接管默认路由后，TCP 握手在本地完成、上传数据被代理大量缓冲，
// 时延与上传测速都会失真。把测速连接绑定到物理网卡即可绕开代理的虚拟网卡，直接经物理线路发出。
package netx

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"syscall"
)

// ErrUnsupported 表示当前平台不支持绑定网卡。
var ErrUnsupported = errors.New("当前平台不支持绑定网卡")

// fakeIPRange 是 Surge、Clash、sing-box 等透明代理默认使用的虚拟地址段(RFC 2544 基准测试保留地址)。
var fakeIPRange = netip.MustParsePrefix("198.18.0.0/15")

// BindControl 返回用于 net.Dialer.Control 的函数，把 socket 绑定到网卡 ifName。
func BindControl(ifName string) (func(network, address string, c syscall.RawConn) error, error) {
	ifi, err := net.InterfaceByName(ifName)
	if err != nil {
		return nil, err
	}
	return func(network, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) { serr = bindSocket(fd, network, ifi) }); err != nil {
			return err
		}
		return serr
	}, nil
}

// DetectTUN 判断按系统路由访问 target 是否会进入透明代理的虚拟网卡，是则返回该网卡名。
// UDP Dial 只做路由查找，不发送数据。
func DetectTUN(target netip.Addr) (string, bool) {
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(target, 53)))
	if err != nil {
		return "", false
	}
	defer conn.Close()
	local := conn.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	ifi := interfaceOf(local)
	if ifi == nil {
		return "", fakeIPRange.Contains(local)
	}
	return ifi.Name, isTUN(ifi, local)
}

// isTUN 判断流量是否进入了透明代理：出口是 TUN 设备，或源地址落在代理使用的虚拟地址段。
func isTUN(ifi *net.Interface, local netip.Addr) bool {
	return fakeIPRange.Contains(local) || isTunDevice(ifi)
}

// pointToPointTun 是无法识别设备类型时的兜底判断：TUN 是点对点接口，
// 但 PPPoE 拨号的 WAN 口(ppp0、pppoe-wan)同样是点对点接口，需要排除。
func pointToPointTun(ifi *net.Interface) bool {
	return ifi.Flags&net.FlagPointToPoint != 0 && !strings.HasPrefix(ifi.Name, "ppp")
}

// usable 判断网卡能否作为绕开代理的出口：已启用、非回环、非 TUN。
func usable(ifi *net.Interface) bool {
	return ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagLoopback == 0 && !isTunDevice(ifi)
}

func interfaceOf(ip netip.Addr) *net.Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for i := range ifaces {
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if addr, ok := netip.AddrFromSlice(n.IP); ok && addr.Unmap() == ip {
					return &ifaces[i]
				}
			}
		}
	}
	return nil
}
