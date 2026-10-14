package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]Version{
		"v0.6.2":   {0, 6, 2},
		"0.6.2":    {0, 6, 2},
		" v1.12.3": {1, 12, 3},
	} {
		if got, ok := ParseVersion(in); !ok || got != want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	// 开发构建、预发布与非版本号都不是正式版。
	for _, in := range []string{"", "dev", "v0.6.2-3-gabc123", "v0.6.2-dirty", "v0.6.2+dirty", "v0.6", "v1.2.3.4", "va.b.c"} {
		if _, ok := ParseVersion(in); ok {
			t.Errorf("ParseVersion(%q): want not ok", in)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tt := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.6.1", "v0.6.2", true},
		{"v0.6.2", "v0.7.0", true},
		{"v0.9.9", "v1.0.0", true},
		{"v0.6.9", "v0.6.10", true}, // 按数值而不是字符串比较
		{"v0.6.2", "v0.6.2", false},
		{"v0.7.0", "v0.6.2", false},
		{"dev", "v9.9.9", false},              // 开发构建不提示
		{"v0.6.2-5-gabc123", "v0.7.0", false}, // 同上
		{"v0.6.2", "latest", false},           // 无法识别的最新版本
	} {
		if got := Newer(tt.current, tt.latest); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

// release 模拟 GitHub：API 返回最新标签，发布包与校验文件放在 /<repo>/releases/download/<tag>/ 下。
type release struct {
	tag     string
	assets  map[string][]byte // 文件名 → 内容
	sums    string            // 校验文件内容；空则按 assets 生成
	apiHits atomic.Int32
	status  int // 非 0 时 API 返回该状态码
}

func (r *release) server(t *testing.T) Source {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		r.apiHits.Add(1)
		if r.status != 0 {
			http.Error(w, "boom", r.status)
			return
		}
		fmt.Fprintf(w, `{"tag_name": %q, "draft": false}`, r.tag)
	})
	base := "/" + Repo + "/releases/download/" + r.tag + "/"
	mux.HandleFunc(base+"checksums-sha256.txt", func(w http.ResponseWriter, _ *http.Request) {
		sums := r.sums
		if sums == "" {
			for name, data := range r.assets {
				sum := sha256.Sum256(data)
				sums += hex.EncodeToString(sum[:]) + "  " + name + "\n"
			}
		}
		fmt.Fprint(w, sums)
	})
	for name, data := range r.assets {
		mux.HandleFunc(base+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Source{API: srv.URL, Download: srv.URL}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLatest(t *testing.T) {
	ctx := context.Background()
	src := (&release{tag: "v0.7.0"}).server(t)
	if got, err := src.Latest(ctx); err != nil || got != "v0.7.0" {
		t.Errorf("Latest = %q, %v", got, err)
	}
	// 标签不是正式版本号、接口出错时都返回错误。
	if _, err := (&release{tag: "nightly"}).server(t).Latest(ctx); err == nil {
		t.Error("tag nightly: want error")
	}
	if _, err := (&release{tag: "v0.7.0", status: 500}).server(t).Latest(ctx); err == nil {
		t.Error("HTTP 500: want error")
	}
	if _, err := (Source{API: "http://127.0.0.1:1"}).Latest(ctx); err == nil {
		t.Error("unreachable: want error")
	}
}

func TestProxyPrefix(t *testing.T) {
	// 加速前缀拼在原始地址之前：前缀 + https://api.github.com/...
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"tag_name":"v1.2.3"}`)
	}))
	defer srv.Close()
	src := Source{API: "http://api.example", Proxy: srv.URL + "/"}
	if got, err := src.Latest(context.Background()); err != nil || got != "v1.2.3" {
		t.Fatalf("Latest = %q, %v", got, err)
	}
	if want := "/http://api.example/repos/" + Repo + "/releases/latest"; gotPath != want {
		t.Errorf("proxied path = %q, want %q", gotPath, want)
	}
}

func TestAssetName(t *testing.T) {
	for _, tt := range []struct {
		goos, goarch, goarm, want string
		wantErr                   bool
	}{
		{"darwin", "arm64", "", "speedtaier-darwin-arm64.tar.gz", false},
		{"linux", "amd64", "", "speedtaier-linux-amd64.tar.gz", false},
		{"linux", "arm", "7", "speedtaier-linux-armv7.tar.gz", false},
		{"linux", "arm", "", "speedtaier-linux-armv7.tar.gz", false},
		{"linux", "386", "", "speedtaier-linux-386.tar.gz", false},
		{"windows", "amd64", "", "speedtaier-windows-amd64.zip", false},
		{"linux", "arm", "6", "", true},
		{"linux", "mips", "", "", true},
		{"freebsd", "amd64", "", "", true},
	} {
		got, err := AssetName(tt.goos, tt.goarch, tt.goarm)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("AssetName(%s, %s, %s) = %q, %v", tt.goos, tt.goarch, tt.goarm, got, err)
		}
	}
}

// installFixture 在临时目录放一个旧程序，并准备好包含新程序的发布包。
func installFixture(t *testing.T, asset string, archive []byte) (src Source, exe string, rel *release) {
	t.Helper()
	rel = &release{tag: "v0.7.0", assets: map[string][]byte{asset: archive}}
	exe = filepath.Join(t.TempDir(), "speedtaier")
	if err := os.WriteFile(exe, []byte("old program"), 0o755); err != nil {
		t.Fatal(err)
	}
	return rel.server(t), exe, rel
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInstall(t *testing.T) {
	const asset = "speedtaier-linux-amd64.tar.gz"
	archive := tarGz(t, map[string]string{
		"speedtaier-linux-amd64/speedtaier": "new program",
		"speedtaier-linux-amd64/README.md":  "readme",
	})
	src, exe, _ := installFixture(t, asset, archive)

	var verified string
	err := src.Install(context.Background(), "v0.7.0", asset, exe, func(path string) error {
		verified = readFile(t, path) // 校验时新程序已写出，旧程序尚未被替换
		if got := readFile(t, exe); got != "old program" {
			t.Errorf("exe replaced before verify: %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if verified != "new program" || readFile(t, exe) != "new program" {
		t.Errorf("verified %q, exe %q", verified, readFile(t, exe))
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(exe); err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("mode = %v, %v", info.Mode(), err)
		}
	}
	if _, err := os.Stat(siblingPath(exe, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary file left behind: %v", err)
	}
}

func TestSiblingPath(t *testing.T) {
	for in, want := range map[string]string{
		filepath.FromSlash("/usr/bin/speedtaier"):     filepath.FromSlash("/usr/bin/speedtaier.new"),
		filepath.FromSlash("C:/tools/speedtaier.exe"): filepath.FromSlash("C:/tools/speedtaier.new.exe"),
	} {
		if got := siblingPath(in, "new"); got != want {
			t.Errorf("siblingPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstallZip(t *testing.T) {
	const asset = "speedtaier-windows-amd64.zip"
	src, exe, _ := installFixture(t, asset, zipOf(t, map[string]string{
		"speedtaier-windows-amd64/speedtaier.exe": "new program",
		"speedtaier-windows-amd64/LICENSE":        "license",
	}))
	if err := src.Install(context.Background(), "v0.7.0", asset, exe, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, exe); got != "new program" {
		t.Errorf("exe = %q", got)
	}
}

// 任何一步失败都必须保留原程序。
func TestInstallKeepsOldProgramOnFailure(t *testing.T) {
	const asset = "speedtaier-linux-amd64.tar.gz"
	good := tarGz(t, map[string]string{"speedtaier-linux-amd64/speedtaier": "new program"})
	sumOf := func(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }

	for name, tt := range map[string]struct {
		archive []byte
		sums    string // 空表示按发布包生成
		verify  func(string) error
		wantErr string
	}{
		"checksum mismatch": {archive: good, sums: sumOf([]byte("other")) + "  " + asset + "\n", wantErr: "校验失败"},
		"asset not listed":  {archive: good, sums: sumOf(good) + "  other.tar.gz\n", wantErr: "校验文件中没有"},
		"no program":        {archive: tarGz(t, map[string]string{"speedtaier-linux-amd64/README.md": "x"}), wantErr: "没有程序文件"},
		"not an archive":    {archive: []byte("garbage"), wantErr: "解压失败"},
		"verify fails":      {archive: good, verify: func(string) error { return errors.New("exec format error") }, wantErr: "已保留原程序"},
	} {
		t.Run(name, func(t *testing.T) {
			src, exe, rel := installFixture(t, asset, tt.archive)
			rel.sums = tt.sums
			if tt.sums == "" {
				rel.sums = sumOf(tt.archive) + "  " + asset + "\n" // 摘要正确，问题出在包内容
			}
			err := src.Install(context.Background(), "v0.7.0", asset, exe, tt.verify)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if got := readFile(t, exe); got != "old program" {
				t.Errorf("exe changed to %q", got)
			}
			if _, err := os.Stat(siblingPath(exe, "new")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("temporary file left behind: %v", err)
			}
		})
	}

	// 发布包不存在(404)。
	src, exe, _ := installFixture(t, asset, good)
	if err := src.Install(context.Background(), "v9.9.9", asset, exe, nil); err == nil {
		t.Error("missing release: want error")
	}
}

func TestInstallNoWritePermission(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("需要非 root 的类 Unix 环境")
	}
	const asset = "speedtaier-linux-amd64.tar.gz"
	src, exe, _ := installFixture(t, asset, tarGz(t, map[string]string{"speedtaier": "new program"}))
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if err := src.Install(context.Background(), "v0.7.0", asset, exe, nil); !errors.Is(err, ErrNoWrite) {
		t.Errorf("err = %v, want ErrNoWrite", err)
	}
}

func TestChecker(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	// poll 等待后台检查完成。
	poll := func(newer func() string) string {
		for range 200 {
			if v := newer(); v != "" {
				return v
			}
			time.Sleep(10 * time.Millisecond)
		}
		return ""
	}
	setup := func(t *testing.T, tag string) (Checker, *release) {
		rel := &release{tag: tag}
		cache := Cache{Path: filepath.Join(t.TempDir(), "sub", "update-check.json")}
		return Checker{Source: rel.server(t), Cache: cache, Now: func() time.Time { return now }}, rel
	}

	t.Run("stale cache: query and refresh", func(t *testing.T) {
		c, rel := setup(t, "v0.7.0")
		if got := poll(c.Start("v0.6.2")); got != "v0.7.0" {
			t.Fatalf("newer = %q", got)
		}
		if latest, at, ok := c.Cache.Read(); !ok || latest != "v0.7.0" || !at.Equal(now) {
			t.Errorf("cache = %q %v %v", latest, at, ok)
		}
		// 缓存有效期内再次运行：直接使用缓存，不访问网络。
		hits := rel.apiHits.Load()
		if got := c.Start("v0.6.2")(); got != "v0.7.0" || rel.apiHits.Load() != hits {
			t.Errorf("cached run: newer=%q, API hits %d -> %d", got, hits, rel.apiHits.Load())
		}
		// 已经是最新版本。
		if got := c.Start("v0.7.0")(); got != "" {
			t.Errorf("up to date: newer = %q", got)
		}
	})

	t.Run("expired cache", func(t *testing.T) {
		c, rel := setup(t, "v0.8.0")
		c.Cache.Write("v0.7.0", now.Add(-25*time.Hour))
		if got := poll(c.Start("v0.6.2")); got != "v0.8.0" || rel.apiHits.Load() != 1 {
			t.Errorf("newer = %q, API hits %d", got, rel.apiHits.Load())
		}
	})

	t.Run("cache from the future is ignored", func(t *testing.T) {
		c, rel := setup(t, "v0.8.0")
		c.Cache.Write("v0.7.0", now.Add(48*time.Hour)) // 时钟被调整过
		if got := poll(c.Start("v0.6.2")); got != "v0.8.0" || rel.apiHits.Load() != 1 {
			t.Errorf("newer = %q, API hits %d", got, rel.apiHits.Load())
		}
	})

	t.Run("development builds are not checked", func(t *testing.T) {
		c, rel := setup(t, "v0.7.0")
		for _, cur := range []string{"dev", "v0.6.2-3-gabc123", "v0.6.2-dirty"} {
			if got := c.Start(cur)(); got != "" {
				t.Errorf("%s: newer = %q", cur, got)
			}
		}
		time.Sleep(50 * time.Millisecond)
		if n := rel.apiHits.Load(); n != 0 {
			t.Errorf("API hit %d times for development builds", n)
		}
	})

	t.Run("failure is silent", func(t *testing.T) {
		c, _ := setup(t, "v0.7.0")
		c.Source = Source{API: "http://127.0.0.1:1"}
		newer := c.Start("v0.6.2")
		time.Sleep(100 * time.Millisecond)
		if got := newer(); got != "" {
			t.Errorf("newer = %q", got)
		}
		if _, _, ok := c.Cache.Read(); ok {
			t.Error("failed check was cached")
		}
	})

	t.Run("slow check does not block", func(t *testing.T) {
		block := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
		defer srv.Close()
		defer close(block)
		c := Checker{Source: Source{API: srv.URL}, Timeout: time.Second}
		start := time.Now()
		newer := c.Start("v0.6.2")
		if got := newer(); got != "" || time.Since(start) > 500*time.Millisecond {
			t.Errorf("newer = %q after %v, want immediate empty result", got, time.Since(start))
		}
	})
}

func TestCache(t *testing.T) {
	c := Cache{Path: filepath.Join(t.TempDir(), "a", "b.json")}
	if _, _, ok := c.Read(); ok {
		t.Error("missing file read as ok")
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c.Write("v1.0.0", at)
	if latest, got, ok := c.Read(); !ok || latest != "v1.0.0" || !got.Equal(at) {
		t.Errorf("round trip = %q %v %v", latest, got, ok)
	}
	if err := os.WriteFile(c.Path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := c.Read(); ok {
		t.Error("corrupt cache read as ok")
	}
	// 零值不缓存，也不报错。
	var none Cache
	none.Write("v1.0.0", at)
	if _, _, ok := none.Read(); ok {
		t.Error("zero cache read as ok")
	}
}
