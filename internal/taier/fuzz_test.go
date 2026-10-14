package taier

import (
	"encoding/hex"
	"math"
	"strings"
	"testing"
	"time"
)

// 这些 fuzz 测试覆盖处理外部数据的入口：节点表、dovalid 响应、加密节点库，以及吞吐计量逻辑。
// go test 会运行种子用例；CI 中以 -fuzz 持续生成随机输入。

func FuzzParseNodes(f *testing.F) {
	f.Add([]byte(`[{"hostid":"1503","hostip":"218.2.122.246","hostname":"南京电信","location":"南京","oper":"电信","pname":"江苏","port":"65499"}]`))
	f.Add([]byte(`[{"hostip":"2409:8020:0:29CC::1","city":"南京","pname":"江苏省"},{"hostip":"bad"}]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		nodes, err := parseNodes(data)
		if err != nil {
			return
		}
		for _, n := range nodes {
			if n.IPv4.IsValid() == n.IPv6.IsValid() {
				t.Fatalf("node must have exactly one address family: %+v", n)
			}
			if n.Port == "" {
				t.Fatalf("node without port: %+v", n)
			}
		}
	})
}

func FuzzParseDoValid(f *testing.F) {
	for _, seed := range []string{"1|abc", "1|", "1", "-1", "0", "2", "", "<html>", "1\x00x"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if key, err := parseDoValid(body); err == nil && key == "" {
			t.Fatalf("parseDoValid(%q) returned empty key without error", body)
		}
	})
}

func FuzzDecryptServerList(f *testing.F) {
	f.Add([]byte("00112233445566778899aabbccddeeff"))
	f.Add([]byte(""))
	f.Add([]byte("zz"))
	f.Fuzz(func(t *testing.T, data []byte) {
		plain, err := decryptServerList(data)
		if err != nil {
			return
		}
		// 与 decryptServerList 一致：解码前去掉首尾空白。
		if raw, _ := hex.DecodeString(strings.TrimSpace(string(data))); len(plain) >= len(raw) {
			t.Fatalf("plaintext (%d bytes) should be shorter than ciphertext (%d bytes)", len(plain), len(raw))
		}
	})
}

// FuzzMeter 把输入解释为每个采样间隔新增的字节数(每字节 × 64KB)，检查计量总能按时结束且结果有效。
func FuzzMeter(f *testing.F) {
	f.Add([]byte{10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 255, 255, 1, 1})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, steps []byte) {
		const limit = 5 * time.Second
		m := meter{start: time.Unix(1000, 0), grace: 1500 * time.Millisecond, min: minMeasure, max: limit}
		var bytes int64
		var at time.Duration
		for i := 0; ; i++ {
			at += sampleInterval
			if i < len(steps) {
				bytes += int64(steps[i]) << 16
			}
			_, _, stop := m.observe(m.start.Add(at), bytes)
			if stop {
				break
			}
			if at > m.grace+limit+sampleInterval {
				t.Fatalf("meter did not stop by %v", at)
			}
		}
		mbps, measured, _ := m.result(m.start.Add(at), bytes)
		if mbps < 0 || math.IsNaN(mbps) || math.IsInf(mbps, 0) || measured <= 0 || measured > limit+sampleInterval {
			t.Fatalf("invalid result: mbps=%v measured=%v", mbps, measured)
		}
	})
}
