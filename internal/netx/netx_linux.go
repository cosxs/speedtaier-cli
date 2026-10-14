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

// PrimaryInterface 返回 main 路由表中经由网关的默认路由所在的物理网卡(取 metric 最小者)。
// TUN 模式的代理通常通过策略路由接管流量，main 表中的物理默认路由仍然保留。
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
		if flags&syscall.RTF_GATEWAY == 0 || (bestMetric >= 0 && metric >= bestMetric) {
			continue
		}
		if ifi, err := net.InterfaceByName(fields[0]); err == nil && physical(ifi) {
			best, bestMetric = ifi.Name, metric
		}
	}
	if best == "" {
		return "", errors.New("路由表中没有经由物理网卡的默认路由")
	}
	return best, nil
}
