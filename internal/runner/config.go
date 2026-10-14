// Package runner 编排一次完整的测速：确定省份与运营商、选择出口网卡、探测节点连通性、按运营商测速，
// 并产出 Result。界面通过 Reporter 接收过程事件，不参与业务逻辑。
package runner

import (
	"context"
	"errors"
	"strings"

	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// ErrInterrupted 表示用户中断(如在交互选择中按下 Ctrl-C)。
var ErrInterrupted = errors.New("已中断")

// Chooser 让用户交互选择省份与节点；Config.Chooser 为 nil 表示自动选择。
// 两个方法按顺序各调用一次，都在调用 Run 的 goroutine 中同步调用；用户中断时返回 ErrInterrupted。
type Chooser interface {
	// Province 选择省份。provinces 为可选省份，home 为按归属地得到的省份(可能为空)，用作默认选项。
	Province(provinces []string, home string) (name string, err error)
	// Node 在已探测连通性的节点中选择一个，返回它在 nodes 中的下标。
	// nodes 按城市、运营商排序；def 是自动模式会选的节点的下标(没有可选节点时为 -1)，用作默认选项。
	Node(province string, nodes []Candidate, def int) (index int, err error)
}

// FamilyMode 决定测速使用的协议族。
type FamilyMode string

const (
	FamilyAuto FamilyMode = "auto" // 本机有 IPv6 路由时加测 IPv6
	Family4    FamilyMode = "4"
	Family6    FamilyMode = "6"
)

// ParseFamilyMode 解析 auto、4、6，也接受 v4、ipv4、v6、ipv6。
func ParseFamilyMode(s string) (FamilyMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto":
		return FamilyAuto, nil
	case "4", "v4", "ipv4":
		return Family4, nil
	case "6", "v6", "ipv6":
		return Family6, nil
	}
	return "", errors.New("可选值为 auto、4、6")
}

// families 返回要测速的协议族。
func (m FamilyMode) families(localV6 bool) ([]taier.Family, error) {
	switch {
	case m == Family4:
		return []taier.Family{taier.IPv4}, nil
	case m == Family6 && !localV6:
		return nil, errors.New("本机没有可用的 IPv6 路由，无法进行 IPv6 测速")
	case m == Family6:
		return []taier.Family{taier.IPv6}, nil
	case localV6:
		return []taier.Family{taier.IPv4, taier.IPv6}, nil
	default:
		return []taier.Family{taier.IPv4}, nil
	}
}

// --interface 的特殊取值。
const (
	InterfaceAuto = "auto" // 检测到透明代理时绑定主物理网卡
	InterfaceNone = "none" // 始终按系统路由
)

// --operator 的特殊取值。
const (
	OperatorAuto = "auto" // 只测本机运营商(按归属地)，无法确定时测时延最低的节点
	OperatorAll  = "all"  // 每个运营商各测一个节点
)

// Config 是一次运行的全部输入。
type Config struct {
	Province   string     // 省份拼音；空表示按归属地确定
	Family     FamilyMode // 空等同 FamilyAuto
	Interface  string     // auto、none 或网卡名；空等同 auto
	Operator   string     // auto、all 或运营商名(中文、拼音或英文，如 联通、unicom)；空等同 auto
	Test       taier.Options
	SurveyOnly bool // 只探测节点连通性，不测速(list 命令)

	// Chooser 非 nil 时交互选择省份与节点，只测所选节点，忽略 Operator；Province 非空时跳过省份选择。
	Chooser Chooser

	// Sources 提供外部数据，零值字段使用官方接口；测试中可替换为本地数据。
	Sources Sources
}

// Sources 是 runner 依赖的外部数据源。
type Sources struct {
	Nodes func(context.Context) ([]taier.Node, error)
	Geo   func(context.Context) (taier.Geo, error)
	IPv6  func(context.Context, []taier.Node) error
}

func (s Sources) withDefaults() Sources {
	if s.Nodes == nil {
		s.Nodes = taier.FetchOfficialList
	}
	if s.Geo == nil {
		s.Geo = taier.FetchGeo
	}
	if s.IPv6 == nil {
		s.IPv6 = taier.AttachIPv6
	}
	return s
}
