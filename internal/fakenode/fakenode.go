// Package fakenode 提供模拟泰尔测速节点的 HTTP 服务，供测试使用。
//
// 支持 dovalid(鉴权与释放)、下载、上传以及时延探测(GET / 返回 404)。
// 传输限速约 64 MB/s 每连接，降低测试开销。
package fakenode

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Key 是模拟节点在 dovalid 成功时下发的测速 key。
const Key = "fake-key"

type Node struct {
	Host     string
	Port     string
	Released atomic.Int32 // 收到的释放请求数
}

// Addr 返回 host:port。
func (n *Node) Addr() string { return net.JoinHostPort(n.Host, n.Port) }

type Options struct {
	Busy      bool          // dovalid 返回 "-1"(节点繁忙)
	PingDelay time.Duration // 延迟响应时延探测请求，用于控制候选节点的排序
}

// Start 启动模拟节点，测试结束时自动关闭。
func Start(t testing.TB, opts Options) *Node {
	t.Helper()
	n := &Node{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(opts.PingDelay)
		http.NotFound(w, r)
	})
	mux.HandleFunc("/speed/dovalid", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			if r.URL.Query().Get("key") == Key {
				n.Released.Add(1)
			}
		case opts.Busy:
			_, _ = io.WriteString(w, "-1")
		default:
			_, _ = io.WriteString(w, "1|"+Key)
		}
	})
	mux.HandleFunc("/speed/File(1G).dl", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != Key {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		buf := make([]byte, 64<<10)
		for {
			if _, err := w.Write(buf); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	mux.HandleFunc("/speed/doAnalsLoad.do", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Key") != Key {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		buf := make([]byte, 64<<10)
		for {
			if _, err := r.Body.Read(buf); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	n.Host, n.Port, _ = net.SplitHostPort(srv.Listener.Addr().String())
	return n
}
