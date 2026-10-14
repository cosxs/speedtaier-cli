package netx

import (
	"errors"
	"net"
	"strings"
	"syscall"

	"golang.org/x/net/route"
)

func bindSocket(fd uintptr, network string, ifi *net.Interface) error {
	if strings.HasSuffix(network, "6") {
		return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_BOUND_IF, ifi.Index)
	}
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_BOUND_IF, ifi.Index)
}

// isTunDevice：macOS 上透明代理与 VPN 使用的 utun 是点对点接口；PPPoE 的 ppp 接口除外。
func isTunDevice(ifi *net.Interface) bool { return pointToPointTun(ifi) }

// PrimaryInterface 返回 IPv4 默认路由所在的非 TUN 网卡。
// 透明代理接管默认路由后，原网卡的默认路由仍以 ifscope 形式保留在路由表中。
// 要求经由网关，以排除虚拟机网桥(bridge100 等)的默认路由；PPP 是点对点链路，没有网关。
func PrimaryInterface() (string, error) {
	rib, err := route.FetchRIB(syscall.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return "", err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return "", err
	}
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok || !isDefault(rm.Addrs) {
			continue
		}
		ifi, err := net.InterfaceByIndex(rm.Index)
		if err != nil || !usable(ifi) {
			continue
		}
		if rm.Flags&syscall.RTF_GATEWAY != 0 || ifi.Flags&net.FlagPointToPoint != 0 {
			return ifi.Name, nil
		}
	}
	return "", errors.New("路由表中没有经由物理网卡的默认路由")
}

func isDefault(addrs []route.Addr) bool {
	at := func(i int) route.Addr {
		if i < len(addrs) {
			return addrs[i]
		}
		return nil
	}
	dst, ok := at(syscall.RTAX_DST).(*route.Inet4Addr)
	if !ok || dst.IP != [4]byte{} {
		return false
	}
	mask, ok := at(syscall.RTAX_NETMASK).(*route.Inet4Addr)
	return !ok || mask.IP == [4]byte{}
}
