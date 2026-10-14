package taier

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseDoValid(t *testing.T) {
	tests := []struct {
		body    string
		wantKey string
		wantErr error
	}{
		{"1|abc-123\r\n", "abc-123", nil},
		{"1|", "", nil}, // 缺少 key，必须报错而不是返回空 key
		{"1", "", nil},
		{"-1", "", errNodeBusy},
		{"0", "", errToken},
		{"2", "", errParams},
		{"<html>", "", nil},
	}
	for _, tt := range tests {
		key, err := parseDoValid(tt.body)
		if tt.wantKey != "" {
			if err != nil || key != tt.wantKey {
				t.Errorf("parseDoValid(%q) = %q, %v; want %q", tt.body, key, err, tt.wantKey)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseDoValid(%q) = %q, want error", tt.body, key)
		} else if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
			t.Errorf("parseDoValid(%q) error = %v, want %v", tt.body, err, tt.wantErr)
		}
	}
}

func TestRawRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chunked":
			_, _ = io.WriteString(w, "abc")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "de")
		case "/big":
			_, _ = w.Write(make([]byte, maxResponseBody+1))
		default:
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	get := func(path string) (int, string, error) {
		code, body, err := rawRequest(context.Background(), Dialer{}, srv.Listener.Addr().String(), "GET", path, nil, time.Second)
		return code, string(body), err
	}
	if code, body, err := get("/chunked"); err != nil || code != 200 || body != "abcde" {
		t.Errorf("chunked: %d %q %v", code, body, err)
	}
	if code, body, err := get("/nope"); err != nil || code != 404 || !strings.HasPrefix(body, "missing") {
		t.Errorf("404: %d %q %v", code, body, err)
	}
	if _, _, err := get("/big"); err == nil {
		t.Error("oversized body: want error")
	}

	// 声明的 Content-Length 大于实际数据即关闭连接：必须报错，不能返回残缺的响应体。
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = bufio.NewReader(c).ReadString('\n')
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nshort")
		c.Close()
	}()
	if _, body, err := rawRequest(context.Background(), Dialer{}, ln.Addr().String(), "GET", "/", nil, time.Second); err == nil {
		t.Errorf("truncated body: got %q, want error", body)
	}
}
