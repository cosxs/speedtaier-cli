package netx

import (
	"bufio"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// bindSocket 使用 SO_BINDTODEVICE；较旧的内核需要 CAP_NET_RAW 权限。
func bindSocket(fd uintptr, _ string, ifi *net.Interface) error {
	return syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifi.Name)
}

// isTunDevice 按设备类型识别 TUN：TUN/TAP 设备有 tun_flags；WireGuard 等三层隧道的类型为
// ARPHRD_NONE(65534)。PPPoE 拨号的 WAN 口类型为 ARPHRD_PPP(512)，虽是点对点接口但不是代理。
func isTunDevice(ifi *net.Interface) bool {
	dir := "/sys/class/net/" + ifi.Name
	if _, err := os.Stat(dir + "/tun_flags"); err == nil {
		return true
	}
	data, err := os.ReadFile(dir + "/type")
	if err != nil {
		return pointToPointTun(ifi)
	}
	return strings.TrimSpace(string(data)) == "65534"
}

// PrimaryInterface 返回 main 路由表中默认路由所在的非 TUN 网卡(取 metric 最小者)。
// TUN 模式的代理通常通过策略路由接管流量，main 表中原有的默认路由仍然保留；
// PPPoE 拨号的默认路由直接指向 ppp 接口，没有网关，因此不要求 RTF_GATEWAY。
func PrimaryInterface() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", err
	}
	defer f.Close()
	best, bestMetric := "", -1
	sc := bufio.NewScanner(f)
	sc.Scan() // 表头
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, _ := strconv.ParseUint(fields[3], 16, 32)
		metric, _ := strconv.Atoi(fields[6])
		if flags&syscall.RTF_UP == 0 || (bestMetric >= 0 && metric >= bestMetric) {
			continue
		}
		if ifi, err := net.InterfaceByName(fields[0]); err == nil && usable(ifi) {
			best, bestMetric = ifi.Name, metric
		}
	}
	if best == "" {
		return "", errors.New("路由表中没有经由物理网卡的默认路由")
	}
	return best, nil
}
