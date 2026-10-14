// Package runner 编排一次完整的测速：确定省份、选择出口网卡、探测节点连通性、按运营商测速，
// 并产出可序列化为 JSON 的 Result。界面通过 Reporter 接收过程事件，不参与业务逻辑。
package runner

import (
	"context"
	"errors"
	"strings"

	"github.com/cosxs/speedtaier-cli/internal/taier"
)

// ErrInterrupted 表示用户中断(如在交互选择中按下 Ctrl-C)。
var ErrInterrupted = errors.New("已中断")

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

// Config 是一次运行的全部输入。
type Config struct {
	Province   string     // 省份拼音；空表示交互选择或按归属地确定
	Family     FamilyMode // 空等同 FamilyAuto
	Interface  string     // auto、none 或网卡名；空等同 auto
	Test       taier.Options
	SurveyOnly bool   // 只探测节点连通性，不测速(list 命令)
	Version    string // 工具版本，写入结果便于追溯

	// ChooseProvince 在未指定省份时交互选择，home 为归属地省份(可能为空)；nil 表示非交互，直接使用归属地。
	ChooseProvince func(provinces []string, home string) (string, error)

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
