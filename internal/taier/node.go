package taier

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
)

const defaultPort = "65499"

// Family 是测速使用的 IP 协议族。
type Family uint8

const (
	IPv4 Family = 4
	IPv6 Family = 6
)

func (f Family) String() string {
	if f == IPv6 {
		return "IPv6"
	}
	return "IPv4"
}

type Node struct {
	ID       string     `json:"id,omitempty"`
	Name     string     `json:"name"`
	Province string     `json:"province,omitempty"`
	City     string     `json:"city,omitempty"`
	Operator string     `json:"operator,omitempty"`
	Port     string     `json:"port"`
	IPv4     netip.Addr `json:"ipv4,omitzero"`
	IPv6     netip.Addr `json:"ipv6,omitzero"`
}

// IP 返回节点在指定协议族下的地址，节点不支持该协议族时返回零值。
func (n Node) IP(f Family) netip.Addr {
	if f == IPv6 {
		return n.IPv6
	}
	return n.IPv4
}

// Addr 返回 host:port 形式的测速地址，节点不支持该协议族时返回空串。
func (n Node) Addr(f Family) string {
	ip := n.IP(f)
	if !ip.IsValid() {
		return ""
	}
	return net.JoinHostPort(ip.String(), cmp.Or(n.Port, defaultPort))
}

// rawNode 兼容全量节点库(hostip/pname/location/oper)与匹配接口(hostip/pname/city)两种字段命名。
type rawNode struct {
	ID       string `json:"hostid"`
	IP       string `json:"ip"`
	HostIP   string `json:"hostip"`
	Port     string `json:"port"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Province string `json:"p"`
	PName    string `json:"pname"`
	City     string `json:"c"`
	CityName string `json:"city"`
	Location string `json:"location"`
	Operator string `json:"o"`
	Oper     string `json:"oper"`
}

// parseNodes 解析节点表，跳过地址无效的记录。
func parseNodes(data []byte) ([]Node, error) {
	var raw []rawNode
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("解析节点表失败: %w", err)
	}
	nodes := make([]Node, 0, len(raw))
	for _, r := range raw {
		ip, err := netip.ParseAddr(strings.TrimSpace(cmp.Or(r.IP, r.HostIP)))
		if err != nil {
			continue
		}
		name := cmp.Or(r.Name, r.Hostname)
		n := Node{
			ID:       r.ID,
			Name:     name,
			Port:     cmp.Or(r.Port, defaultPort),
			Province: NormalizeProvince(cmp.Or(r.Province, r.PName)),
			City:     cmp.Or(r.City, r.CityName, r.Location),
			Operator: normalizeOperator(cmp.Or(r.Operator, r.Oper), name),
		}
		if ip = ip.Unmap(); ip.Is4() {
			n.IPv4 = ip
		} else {
			n.IPv6 = ip
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

type province struct{ name, pinyin string }

// provinceTable 按 GB/T 2260 行政区划代码排列，省份列表按此排序。
// 陕西按国家标准拼写为 shaanxi，以区别山西 shanxi。
var provinceTable = []province{
	{"北京", "beijing"}, {"天津", "tianjin"}, {"河北", "hebei"}, {"山西", "shanxi"}, {"内蒙古", "neimenggu"},
	{"辽宁", "liaoning"}, {"吉林", "jilin"}, {"黑龙江", "heilongjiang"},
	{"上海", "shanghai"}, {"江苏", "jiangsu"}, {"浙江", "zhejiang"}, {"安徽", "anhui"},
	{"福建", "fujian"}, {"江西", "jiangxi"}, {"山东", "shandong"},
	{"河南", "henan"}, {"湖北", "hubei"}, {"湖南", "hunan"}, {"广东", "guangdong"}, {"广西", "guangxi"}, {"海南", "hainan"},
	{"重庆", "chongqing"}, {"四川", "sichuan"}, {"贵州", "guizhou"}, {"云南", "yunnan"}, {"西藏", "xizang"},
	{"陕西", "shaanxi"}, {"甘肃", "gansu"}, {"青海", "qinghai"}, {"宁夏", "ningxia"}, {"新疆", "xinjiang"},
}

// KnownProvinces 返回全部省份的拼音，用于 shell 补全。
func KnownProvinces() []string {
	out := make([]string, len(provinceTable))
	for i, p := range provinceTable {
		out[i] = p.pinyin
	}
	return out
}

// LookupProvince 按拼音(不区分大小写，如 "shandong")查找省份，返回节点库使用的中文简称。
func LookupProvince(pinyin string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(pinyin))
	for _, p := range provinceTable {
		if s == p.pinyin {
			return p.name, true
		}
	}
	return "", false
}

// ProvincePinyin 返回省份简称对应的拼音，未知省份返回空串。
func ProvincePinyin(name string) string {
	for _, p := range provinceTable {
		if p.name == name {
			return p.pinyin
		}
	}
	return ""
}

// Provinces 返回节点表中出现的省份，按行政区划代码排序。
func Provinces(nodes []Node) []string {
	var out []string
	for _, n := range nodes {
		if n.Province != "" && !slices.Contains(out, n.Province) {
			out = append(out, n.Province)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int {
		return cmp.Compare(provinceRank(a), provinceRank(b))
	})
	return out
}

func provinceRank(name string) int {
	if i := slices.IndexFunc(provinceTable, func(p province) bool { return p.name == name }); i >= 0 {
		return i
	}
	return len(provinceTable)
}

// SameCity 判断两个城市名是否指同一城市，兼容归属地的全称，如 "济南市"、"延边朝鲜族自治州" 分别与 "济南"、"延边" 相同。
func SameCity(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a))
}

// InProvince 返回属于指定省份的节点，保持节点库原有顺序(省会排在最前)。
func InProvince(nodes []Node, province string) []Node {
	province = NormalizeProvince(province)
	var out []Node
	for _, n := range nodes {
		if n.Province == province {
			out = append(out, n)
		}
	}
	return out
}

var provinceSuffixes = []string{"特别行政区", "自治区", "省", "市", "壮族", "回族", "维吾尔"}

// NormalizeProvince 把"江苏省""广西壮族自治区"等写法统一成节点库使用的简称。
func NormalizeProvince(s string) string {
	s = strings.TrimSpace(s)
	for _, suf := range provinceSuffixes {
		s = strings.TrimSuffix(s, suf)
	}
	return s
}

// operatorOrder 是运营商的展示与测速顺序，未列出的排在其后，未知运营商排在最后。
var operatorOrder = []string{"电信", "联通", "移动", "广电", "教育网", "鹏博士"}

// normalizeOperator 去掉"中国"前缀；节点库未标注运营商时从节点名推断(如"连云港移动")。
func normalizeOperator(op, name string) string {
	if op = strings.TrimPrefix(strings.TrimSpace(op), "中国"); op != "" {
		return op
	}
	for _, o := range operatorOrder {
		if strings.Contains(name, o) {
			return o
		}
	}
	return ""
}

// CompareOperator 按 operatorOrder 比较两个运营商，用于排序。
func CompareOperator(a, b string) int {
	return cmp.Compare(operatorRank(a), operatorRank(b))
}

func operatorRank(op string) int {
	if op == "" {
		return len(operatorOrder) + 1
	}
	for i, o := range operatorOrder {
		if strings.HasPrefix(op, o) { // "广电网" 归入 "广电"
			return i
		}
	}
	return len(operatorOrder)
}

// operatorAliases 是常见运营商的拼音与英文名。
var operatorAliases = map[string]string{
	"dianxin": "电信", "telecom": "电信",
	"liantong": "联通", "unicom": "联通",
	"yidong": "移动", "mobile": "移动",
	"guangdian": "广电",
}

// LookupOperator 把运营商的中文名、拼音或英文名(不区分大小写)统一为 operatorOrder 中的写法，
// 如 "unicom"、"中国联通" 都返回 "联通"；未知运营商只去掉"中国"前缀。
func LookupOperator(s string) string {
	if o, ok := operatorAliases[strings.ToLower(strings.TrimSpace(s))]; ok {
		return o
	}
	return canonicalOperator(s)
}

// SameOperator 判断两个运营商名称是否指同一运营商，如 "中国联通" 与 "联通"、"广电网" 与 "广电"。
func SameOperator(a, b string) bool {
	a, b = canonicalOperator(a), canonicalOperator(b)
	return a != "" && a == b
}

func canonicalOperator(op string) string {
	op = strings.TrimPrefix(strings.TrimSpace(op), "中国")
	for _, o := range operatorOrder {
		if strings.HasPrefix(op, o) {
			return o
		}
	}
	return op
}

// attachIPv6 按 hostid 把匹配接口返回的 IPv6 地址合并进 nodes，返回合并数量。
// 节点库中 hostid 为 "0" 的记录不唯一，不参与合并。
func attachIPv6(nodes, matched []Node) int {
	index := make(map[string]int, len(nodes))
	for i, n := range nodes {
		if n.ID != "" && n.ID != "0" {
			index[n.ID] = i
		}
	}
	attached := 0
	for _, m := range matched {
		if i, ok := index[m.ID]; ok && m.IPv6.IsValid() {
			nodes[i].IPv6 = m.IPv6
			attached++
		}
	}
	return attached
}
