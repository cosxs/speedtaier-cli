package taier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	apiBase = "https://dlcv2.cnspeedtest.cn:8443"
	appName = "全球网测"
	appType = "globalspeed"
	appVer  = "40408"
)

// apiClient 不设置 Timeout：http.Client.Timeout 包含读取响应体的时间，会抢先于各请求自己的 ctx 超时生效。
var apiClient = &http.Client{}

type Geo struct {
	IP       string `json:"ip"`
	Country  string `json:"country,omitempty"`
	Province string `json:"province,omitempty"`
	City     string `json:"city,omitempty"`
	District string `json:"district,omitempty"`
	Operator string `json:"operator,omitempty"`
}

func FetchGeo(ctx context.Context) (Geo, error) {
	body, err := apiGet(ctx, apiBase+"/dataServer/getIpLocS.php")
	if err != nil {
		return Geo{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(body)), "|", 4)
	if len(parts) < 2 {
		return Geo{}, fmt.Errorf("归属地接口返回异常: %.80q", body)
	}
	geo := Geo{IP: parts[0]}
	var area []string
	if err := json.Unmarshal([]byte(parts[1]), &area); err == nil {
		at := func(i int) string {
			if i < len(area) {
				return area[i]
			}
			return ""
		}
		geo.Country, geo.Province, geo.City, geo.District = at(0), at(1), at(2), at(3)
	}
	if len(parts) >= 4 && parts[3] != "" {
		geo.Operator = parts[3]
	}
	return geo, nil
}

// fetchIPv6Nodes 通过官方匹配接口查询某省某运营商的 IPv6 节点。
//
// 匹配接口的行为(实测):
//   - ip 非空时只按 ip 匹配，忽略 province/city；
//   - ip 与 city 都为空时按请求来源 IP 匹配；
//   - 只有 ip 为空且省、市、运营商齐全时才按地区匹配，city 取该省任一城市即可；
//   - ipv6=1 时每个省份+运营商只返回一个节点(省会主节点)，hostid 与全量库一致。
func fetchIPv6Nodes(ctx context.Context, province, city, operator string) ([]Node, error) {
	q := url.Values{
		"ip":       {""},
		"network":  {"4"},
		"province": {province},
		"city":     {city},
		"wifioper": {operator},
		"ipv6":     {"1"},
		"model":    {"Android"},
	}
	body, err := apiGet(ctx, apiBase+"/dataServer/mobilematch_list.php?"+q.Encode())
	if err != nil {
		return nil, err
	}
	return parseNodes(body)
}

// AttachIPv6 为同一省份的节点补全 IPv6 地址：每个运营商并发查询一次匹配接口，按 hostid 合并。
// 部分运营商查询失败时仍会合并其余结果，并返回汇总的错误。
func AttachIPv6(ctx context.Context, nodes []Node) error {
	if len(nodes) == 0 {
		return nil
	}
	province := nodes[0].Province
	cities := make(map[string]string) // 运营商 → 该运营商在节点库中的第一个城市
	for _, n := range nodes {
		if _, ok := cities[n.Operator]; !ok && n.Operator != "" && n.City != "" {
			cities[n.Operator] = n.City
		}
	}

	var (
		mu      sync.Mutex
		matched []Node
		errs    []error
		wg      sync.WaitGroup
	)
	for op, city := range cities {
		wg.Go(func() {
			found, err := fetchIPv6Nodes(ctx, province, city, op)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", op, err))
				return
			}
			matched = append(matched, found...)
		})
	}
	wg.Wait()
	attachIPv6(nodes, matched)
	return errors.Join(errs...)
}

type configEntry struct {
	Name     string `json:"name"`
	Filename string `json:"filename"`
	MD5      string `json:"md5"`
}

// serverListEntry 在配置清单中查找加密节点库的下载地址。
func serverListEntry(manifest []configEntry) (configEntry, bool) {
	for _, e := range manifest {
		if e.Name == "serverlist_encrypt_url" && strings.HasPrefix(e.Filename, "http") {
			return e, true
		}
	}
	return configEntry{}, false
}

// FetchOfficialList 获取官方全量节点库(仅含 IPv4 地址)，清单 MD5 未变化时直接使用本地缓存。
func FetchOfficialList(ctx context.Context) ([]Node, error) {
	q := url.Values{
		"province": {"北京"}, "city": {"北京"},
		"imei":    {"863096060000001"},
		"appname": {appName},
		"vercode": {appVer},
		"type":    {appType},
		"ipv6":    {"0"},
	}
	body, err := apiGet(ctx, apiBase+"/TaierAndroid/Config/ConfigMD5.php?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var manifest []configEntry
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("配置清单解析失败: %w", err)
	}
	entry, ok := serverListEntry(manifest)
	if !ok {
		return nil, fmt.Errorf("配置清单中没有 serverlist_encrypt_url")
	}

	enc, ok := readCachedServerList(entry.MD5)
	if !ok {
		if enc, err = fetchBytes(ctx, entry.Filename); err != nil {
			return nil, err
		}
		if strings.EqualFold(md5hex(enc), entry.MD5) {
			writeCachedServerList(enc)
		}
	}
	plain, err := decryptServerList(enc)
	if err != nil {
		return nil, err
	}
	nodes, err := parseNodes(plain)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("节点表为空")
	}
	return nodes, nil
}

func fetchBytes(ctx context.Context, rawURL string) ([]byte, error) {
	return httpGetBytes(ctx, rawURL, 60*time.Second, 16<<20)
}

func apiGet(ctx context.Context, rawURL string) ([]byte, error) {
	return httpGetBytes(ctx, rawURL, 20*time.Second, 8<<20)
}

func httpGetBytes(ctx context.Context, rawURL string, timeout time.Duration, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", uaAndroid)
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求 %s 返回 HTTP %d", rawURL, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("请求 %s 的响应超过 %d 字节上限", rawURL, limit)
	}
	return data, nil
}
