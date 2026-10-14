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

// PrimaryInterface 返回经由网关的 IPv4 默认路由所在的物理网卡。
// 透明代理接管默认路由后，原物理网卡的默认路由仍以 ifscope 形式保留在路由表中。
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
		if !ok || rm.Flags&syscall.RTF_GATEWAY == 0 || !isDefault(rm.Addrs) {
			continue
		}
		if ifi, err := net.InterfaceByIndex(rm.Index); err == nil && physical(ifi) {
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
