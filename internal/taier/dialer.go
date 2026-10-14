package taier

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/cosxs/speedtaier-cli/internal/netx"
)

// Dialer 建立到测速节点的连接。零值按系统路由；由 NewDialer 绑定网卡后，
// 连接直接经该网卡发出，绕开透明代理(TUN)接管的默认路由。
type Dialer struct {
	iface   string
	control func(network, address string, c syscall.RawConn) error
}

// NewDialer 返回绑定到网卡 ifName 的 Dialer。
func NewDialer(ifName string) (Dialer, error) {
	control, err := netx.BindControl(ifName)
	if err != nil {
		return Dialer{}, fmt.Errorf("网卡 %s 不可用: %w", ifName, err)
	}
	return Dialer{iface: ifName, control: control}, nil
}

// Interface 返回绑定的网卡名，按系统路由时为空。
func (d Dialer) Interface() string { return d.iface }

// Check 验证经此 Dialer 能否路由到 ip，用于提前发现绑定失败(如缺少权限)。UDP Dial 只做路由查找，不发送数据。
func (d Dialer) Check(ip netip.Addr) error {
	conn, err := d.dial(context.Background(), "udp", netip.AddrPortFrom(ip, 53).String(), time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func (d Dialer) dial(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
	nd := net.Dialer{Timeout: timeout, Control: d.control}
	return nd.DialContext(ctx, network, addr)
}
