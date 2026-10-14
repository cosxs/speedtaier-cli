// Package update 检查 GitHub Releases 上的新版本，并下载、校验、替换当前程序。
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repo 是发布包所在的 GitHub 仓库。
const Repo = "cosxs/speedtaier-cli"

// Source 是发布信息与发布包的来源，零值使用 GitHub；测试中可指向本地服务。
type Source struct {
	API      string       // GitHub API 地址，默认 https://api.github.com
	Download string       // 发布包下载地址，默认 https://github.com
	Proxy    string       // 访问加速前缀(与 install.sh 的 GITHUB_PROXY 相同)，最终地址为 前缀 + 原始地址
	Client   *http.Client // 默认带 30 秒超时
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s Source) api() string      { return s.Proxy + cmpOr(s.API, "https://api.github.com") }
func (s Source) download() string { return s.Proxy + cmpOr(s.Download, "https://github.com") }

func cmpOr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// get 请求 url 并返回至多 limit 字节的响应体；超出 limit 视为错误。
func (s Source) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "speedtaier")
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream, text/plain")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: 响应超过 %d 字节", url, limit)
	}
	return data, nil
}

// Latest 返回最新正式版的标签，如 "v0.7.0"(不含预发布版本)。
func (s Source) Latest(ctx context.Context) (string, error) {
	body, err := s.get(ctx, s.api()+"/repos/"+Repo+"/releases/latest", 1<<20)
	if err != nil {
		return "", err
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("解析发布信息失败: %w", err)
	}
	v, ok := ParseVersion(rel.Tag)
	if !ok {
		return "", fmt.Errorf("无法识别的版本号 %q", rel.Tag)
	}
	return v.String(), nil
}

// Version 是 vMAJOR.MINOR.PATCH 形式的版本号。
type Version struct{ Major, Minor, Patch int }

var versionRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// ParseVersion 解析 "v0.6.2" 或 "0.6.2"。开发版本(dev、v0.6.2-3-gabc123、v0.6.2-dirty 等)不是正式版，返回 false。
func ParseVersion(s string) (Version, bool) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, false
	}
	var v Version
	for i, dst := range []*int{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, false
		}
		*dst = n
	}
	return v, true
}

func (v Version) String() string { return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch) }

// Less 判断 v 是否早于 o。
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Newer 判断 latest 是否比 current 新。current 不是正式版(开发构建)时返回 false，不打扰开发者。
func Newer(current, latest string) bool {
	cur, ok := ParseVersion(current)
	if !ok {
		return false
	}
	lat, ok := ParseVersion(latest)
	return ok && cur.Less(lat)
}

// Cache 保存上次检查到的最新版本，避免每次运行都访问网络。零值不缓存。
type Cache struct{ Path string }

// DefaultCache 位于系统缓存目录(与节点库缓存相同)；无法确定缓存目录时返回不缓存的零值。
func DefaultCache() Cache {
	dir, err := os.UserCacheDir()
	if err != nil {
		return Cache{}
	}
	return Cache{Path: filepath.Join(dir, "speedtaier", "update-check.json")}
}

type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// Read 返回缓存的最新版本及检查时间。
func (c Cache) Read() (latest string, checkedAt time.Time, ok bool) {
	if c.Path == "" {
		return "", time.Time{}, false
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return "", time.Time{}, false
	}
	var e cacheEntry
	if json.Unmarshal(data, &e) != nil || e.Latest == "" {
		return "", time.Time{}, false
	}
	return e.Latest, e.CheckedAt, true
}

// Write 尽力写入缓存，失败不影响使用。
func (c Cache) Write(latest string, at time.Time) {
	if c.Path == "" {
		return
	}
	data, err := json.Marshal(cacheEntry{CheckedAt: at.UTC(), Latest: latest})
	if err != nil || os.MkdirAll(filepath.Dir(c.Path), 0o755) != nil {
		return
	}
	_ = os.WriteFile(c.Path, data, 0o644)
}

// Checker 在后台检查新版本，不阻塞主流程。
type Checker struct {
	Source  Source
	Cache   Cache
	TTL     time.Duration // 两次联网检查的最短间隔，默认 24 小时
	Timeout time.Duration // 单次联网检查的超时，默认 3 秒
	Now     func() time.Time
}

func (c Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Start 开始检查并立即返回。返回的函数不阻塞：检查已完成且有新版本时返回版本号，否则返回空串。
// 缓存未过期时直接使用缓存，不访问网络；current 不是正式版时不检查。
func (c Checker) Start(current string) (newer func() string) {
	none := func() string { return "" }
	if _, ok := ParseVersion(current); !ok {
		return none
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if latest, at, ok := c.Cache.Read(); ok && c.now().Sub(at) < ttl && !c.now().Before(at) {
		return func() string { return ifNewer(current, latest) }
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	done := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		latest, err := c.Source.Latest(ctx)
		if err != nil {
			done <- ""
			return
		}
		c.Cache.Write(latest, c.now())
		done <- latest
	}()
	return func() string {
		select {
		case latest := <-done:
			return ifNewer(current, latest)
		default:
			return ""
		}
	}
}

func ifNewer(current, latest string) string {
	if Newer(current, latest) {
		return latest
	}
	return ""
}

// ErrNoWrite 表示没有替换程序文件的权限。
var ErrNoWrite = errors.New("没有写入权限")
