package taier

import (
	"cmp"
	"context"
	"net"
	"slices"
	"sync"
	"time"
)

const (
	probeSamples     = 3
	probeTimeout     = 1500 * time.Millisecond
	probeConcurrency = 16
)

// ipv6RouteTarget 是一个公网 IPv6 地址(AliDNS)，仅用于路由查找，不会向它发送数据。
const ipv6RouteTarget = "[2400:3200::1]:53"

// HasIPv6Route 判断经 d 是否有公网 IPv6 路由。UDP Dial 只做路由查找，不发送数据包。
func HasIPv6Route(d Dialer) bool {
	conn, err := d.dial(context.Background(), "udp6", ipv6RouteTarget, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return false
	}
	ip := local.AddrPort().Addr()
	return ip.IsGlobalUnicast() && !ip.IsPrivate()
}

// ReachState 描述节点某个协议族地址的连通情况。
type ReachState uint8

const (
	ReachNoAddr  ReachState = iota // 节点没有该协议族的地址
	ReachSkipped                   // 本机不支持该协议族，未探测
	ReachFailed                    // 建连失败或节点不响应
	ReachOK                        // 可达，RTT 为多次应用层往返中的最小耗时
)

type Reach struct {
	State ReachState
	RTT   time.Duration
}

// Candidate 是带有 IPv4/IPv6 连通性探测结果的节点。
type Candidate struct {
	Node Node
	V4   Reach
	V6   Reach
}

// ReachOf 返回指定协议族的探测结果。
func (c Candidate) ReachOf(f Family) Reach {
	if f == IPv6 {
		return c.V6
	}
	return c.V4
}

func (c *Candidate) reach(f Family) *Reach {
	if f == IPv6 {
		return &c.V6
	}
	return &c.V4
}

// Probe 并发探测节点在 families 中各协议族下的应用层往返时延。
// 节点没有某协议族地址时记为 ReachNoAddr；有地址但该协议族不在 families 中时记为 ReachSkipped。
func Probe(ctx context.Context, d Dialer, nodes []Node, families ...Family) []Candidate {
	cands := make([]Candidate, len(nodes))
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i, n := range nodes {
		cands[i].Node = n
		for _, f := range []Family{IPv4, IPv6} {
			r := cands[i].reach(f)
			switch {
			case !n.IP(f).IsValid():
				r.State = ReachNoAddr
			case !slices.Contains(families, f):
				r.State = ReachSkipped
			default:
				wg.Go(func() {
					select {
					case sem <- struct{}{}:
						defer func() { <-sem }()
						*r = probeAddr(ctx, d, n.Addr(f))
					case <-ctx.Done():
						r.State = ReachFailed
					}
				})
			}
		}
	}
	wg.Wait()
	return cands
}

// probeAddr 用应用层往返时延探测节点(见 pinger)，取 probeSamples 次中的最小值。
// 预热请求失败即判为不可达：TCP 端口开着但不响应测速协议的节点同样无法测速。
func probeAddr(ctx context.Context, d Dialer, addr string) Reach {
	p, err := dialPinger(ctx, d, addr, probeTimeout)
	if err != nil {
		return Reach{State: ReachFailed}
	}
	defer p.close()
	var best time.Duration
	for range probeSamples {
		rtt, err := p.ping(probeTimeout)
		if rtt > 0 && (best == 0 || rtt < best) {
			best = rtt
		}
		if err != nil {
			break
		}
	}
	if best == 0 {
		return Reach{State: ReachFailed}
	}
	return Reach{State: ReachOK, RTT: best}
}

// TestGroup 是同一运营商、同一协议族下按建连时延升序排列的可达节点。
// 测速时依次尝试，首个成功的节点即为该组结果；Nodes 为空表示该组没有可达节点。
type TestGroup struct {
	Operator string
	Family   Family
	Nodes    []Node
}

// PlanTests 按运营商分组，为每个协议族挑出可达节点并按时延排序。
// 运营商在某协议族下没有任何地址时不生成对应分组。
func PlanTests(cands []Candidate, families ...Family) []TestGroup {
	var ops []string
	for _, c := range cands {
		if !slices.Contains(ops, c.Node.Operator) {
			ops = append(ops, c.Node.Operator)
		}
	}
	slices.SortStableFunc(ops, CompareOperator)

	var groups []TestGroup
	for _, op := range ops {
		for _, f := range families {
			var reachable []Candidate
			hasAddr := false
			for _, c := range cands {
				if c.Node.Operator != op {
					continue
				}
				r := c.ReachOf(f)
				hasAddr = hasAddr || r.State != ReachNoAddr
				if r.State == ReachOK {
					reachable = append(reachable, c)
				}
			}
			if !hasAddr {
				continue
			}
			slices.SortStableFunc(reachable, func(a, b Candidate) int {
				return cmp.Compare(a.ReachOf(f).RTT, b.ReachOf(f).RTT)
			})
			g := TestGroup{Operator: op, Family: f}
			for _, c := range reachable {
				g.Nodes = append(g.Nodes, c.Node)
			}
			groups = append(groups, g)
		}
	}
	return groups
}
