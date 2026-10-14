package taier

import (
	"net/netip"
	"slices"
	"testing"
)

func TestNormalizeProvince(t *testing.T) {
	for in, want := range map[string]string{
		"江苏":       "江苏",
		"江苏省":      "江苏",
		" 北京市 ":    "北京",
		"广西壮族自治区":  "广西",
		"宁夏回族自治区":  "宁夏",
		"新疆维吾尔自治区": "新疆",
		"内蒙古自治区":   "内蒙古",
		"香港特别行政区":  "香港",
	} {
		if got := NormalizeProvince(in); got != want {
			t.Errorf("NormalizeProvince(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupProvince(t *testing.T) {
	for in, want := range map[string]string{
		"shandong":  "山东",
		" BeiJing ": "北京",
		"shanxi":    "山西",
		"shaanxi":   "陕西",
		"neimenggu": "内蒙古",
		"山东":        "", // 只接受拼音
		"山东省":       "",
		"shandon":   "",
	} {
		got, ok := LookupProvince(in)
		if got != want || ok != (want != "") {
			t.Errorf("LookupProvince(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// 拼音表必须与 Provinces 的排序表一一对应且不重复。
	seen := map[string]bool{}
	for _, py := range KnownProvinces() {
		name, ok := LookupProvince(py)
		if !ok || seen[py] || ProvincePinyin(name) != py {
			t.Errorf("pinyin %q round-trips to %q", py, name)
		}
		seen[py] = true
	}
	if len(seen) != 31 {
		t.Errorf("got %d provinces, want 31", len(seen))
	}
}

func TestParseNodes(t *testing.T) {
	data := []byte(`[
		{"hostid":"1503","hostip":"218.2.122.246","hostname":"南京电信","location":"南京","oper":"电信","pname":"江苏","port":"65499"},
		{"hostid":"2362","hostip":"112.4.233.254","hostname":"连云港移动","location":"连云港","oper":"","pname":"江苏","port":"65499"},
		{"hostid":"1504","hostip":"2409:8020:0:29CC::1","hostname":"南京移动","city":"南京","pname":"江苏省"},
		{"hostid":"9","hostip":"not-an-ip","hostname":"坏节点"}
	]`)
	nodes, err := parseNodes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (invalid address skipped)", len(nodes))
	}
	if n := nodes[0]; n.ID != "1503" || n.IPv4 != netip.MustParseAddr("218.2.122.246") || n.IPv6.IsValid() || n.City != "南京" {
		t.Errorf("official node parsed wrong: %+v", n)
	}
	if got := nodes[1].Operator; got != "移动" {
		t.Errorf("operator inferred from name = %q, want 移动", got)
	}
	n := nodes[2]
	if n.IPv4.IsValid() || n.IPv6.String() != "2409:8020:0:29cc::1" || n.Province != "江苏" || n.City != "南京" || n.Port != defaultPort {
		t.Errorf("match node parsed wrong: %+v", n)
	}
	if got, want := n.Addr(IPv6), "[2409:8020:0:29cc::1]:65499"; got != want {
		t.Errorf("Addr(IPv6) = %q, want %q", got, want)
	}
	if got := n.Addr(IPv4); got != "" {
		t.Errorf("Addr(IPv4) = %q, want empty", got)
	}
}

func TestProvincesOrder(t *testing.T) {
	nodes := []Node{{Province: "广东"}, {Province: "北京"}, {Province: "江苏"}, {Province: "北京"}, {Province: ""}}
	if got, want := Provinces(nodes), []string{"北京", "江苏", "广东"}; !slices.Equal(got, want) {
		t.Errorf("Provinces = %v, want %v", got, want)
	}
}

func TestAttachIPv6(t *testing.T) {
	nodes := []Node{
		{ID: "1503", Name: "南京电信"},
		{ID: "0", Name: "未编号A"},
		{ID: "2361", Name: "南通移动"},
	}
	v6 := netip.MustParseAddr("240e:5a:6800:2::3")
	matched := []Node{
		{ID: "1503", IPv6: v6},
		{ID: "0", IPv6: netip.MustParseAddr("2001:db8::1")},    // hostid 0 不唯一，不能合并
		{ID: "1001", IPv6: netip.MustParseAddr("2001:db8::2")}, // 其他省份的节点
		{ID: "2361", IPv4: netip.MustParseAddr("192.0.2.1")},   // 没有 IPv6
	}
	if got := attachIPv6(nodes, matched); got != 1 {
		t.Errorf("attached = %d, want 1", got)
	}
	if nodes[0].IPv6 != v6 || nodes[1].IPv6.IsValid() || nodes[2].IPv6.IsValid() {
		t.Errorf("unexpected merge result: %+v", nodes)
	}
}
