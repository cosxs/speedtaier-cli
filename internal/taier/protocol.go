package taier

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	uaAndroid  = "Dalvik/2.1.0 (Linux; U; Android 14; NE2210 Build/TP1A.220624.014)"
	uaDownload = "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/100.0.4896.60 Safari/537.36"
)

var (
	errNodeBusy = errors.New("节点繁忙或排队超限")
	errToken    = errors.New("鉴权失败(token 无效)")
	errParams   = errors.New("节点拒绝请求(参数不合法)")
)

func reachable(ctx context.Context, d Dialer, addr string, timeout time.Duration) error {
	conn, err := d.dial(ctx, "tcp", addr, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

func doValid(ctx context.Context, d Dialer, addr, imei string, bandwidth int, timeout time.Duration) (string, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	q := url.Values{
		"key":       {""},
		"flag":      {"true"},
		"bandwidth": {strconv.Itoa(bandwidth)},
		"model":     {"Android"},
		"imei":      {imei},
		"time":      {ts},
		"token":     {token(imei, ts)},
	}
	code, body, err := rawRequest(ctx, d, addr, "GET", "/speed/dovalid?"+q.Encode(),
		[][2]string{{"Connection", "close"}, {"User-Agent", uaAndroid}}, timeout)
	if err != nil {
		return "", fmt.Errorf("dovalid 请求失败: %w", err)
	}
	if code != 200 {
		return "", fmt.Errorf("dovalid 返回 HTTP %d", code)
	}
	return parseDoValid(string(body))
}

// parseDoValid 解析 dovalid 响应：成功时为 "1" + 一个分隔符 + key，其余前缀为错误码。
func parseDoValid(body string) (string, error) {
	r := strings.TrimSpace(body)
	switch {
	case strings.HasPrefix(r, "1"):
		if len(r) <= 2 {
			return "", fmt.Errorf("dovalid 响应缺少 key: %.40q", r)
		}
		return r[2:], nil
	case strings.HasPrefix(r, "-1"):
		return "", fmt.Errorf("%w (dovalid: -1)", errNodeBusy)
	case strings.HasPrefix(r, "0"):
		return "", fmt.Errorf("%w (dovalid: 0)", errToken)
	case strings.HasPrefix(r, "2"):
		return "", fmt.Errorf("%w (dovalid: 2)", errParams)
	default:
		return "", fmt.Errorf("dovalid 响应异常: %.40q", r)
	}
}

func release(ctx context.Context, d Dialer, addr, uuid string) error {
	if uuid == "" {
		return nil
	}
	path := "/speed/dovalid?" + url.Values{"key": {uuid}}.Encode()
	_, _, err := rawRequest(ctx, d, addr, "POST", path,
		[][2]string{{"Connection", "close"}, {"Content-Length", "0"}}, 3*time.Second)
	return err
}

// rawRequest 的 timeout 覆盖建连与整个请求-响应过程。
func rawRequest(ctx context.Context, d Dialer, addr, method, path string, headers [][2]string, timeout time.Duration) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := d.dial(ctx, "tcp", addr, timeout)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	var req strings.Builder
	fmt.Fprintf(&req, "%s %s HTTP/1.1\r\nHost: %s\r\n", method, path, addr)
	for _, h := range headers {
		fmt.Fprintf(&req, "%s: %s\r\n", h[0], h[1])
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		return 0, nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("读取响应失败: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("读取响应体失败: %w", err)
	}
	if len(body) > maxResponseBody {
		return resp.StatusCode, nil, fmt.Errorf("响应体超过 %d 字节", maxResponseBody)
	}
	return resp.StatusCode, body, nil
}

// maxResponseBody 限制控制类响应(dovalid、时延探测等)的大小。
const maxResponseBody = 1 << 20
