package cli

import (
	"time"

	"github.com/cosxs/speedtaier-cli/internal/runner"
	"github.com/cosxs/speedtaier-cli/internal/taier"
)

const ms = time.Millisecond

var node = runner.NodeRef{Name: "济南电信", Operator: "电信", City: "济南", Address: "58.56.118.126:65499"}

// delay 构造时延统计事件，单位毫秒。
func delay(samples int, median, min, max, jitter float64) taier.LatencyStats {
	d := func(v float64) time.Duration { return time.Duration(v * float64(ms)) }
	return taier.LatencyStats{Samples: samples, Median: d(median), Min: d(min), Max: d(max), Jitter: d(jitter)}
}

var (
	idleDone = taier.ProgressUpdate{Phase: taier.PhaseLatency, Fraction: 1, Final: true, Latency: delay(20, 5.9, 4.6, 13.7, 2.8)}
	downOne  = taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 1, Fraction: 1, Final: true, Mbps: 971.7,
		Bytes: 619_800_000, Total: 5300 * ms, Latency: delay(40, 14.6, 12, 26.9, 4.1)}
	downMid = taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 8, Fraction: 0.4, Mbps: 801.2,
		Bytes: 300_000_000, Total: 3 * time.Second, Latency: delay(6, 15.4, 14, 20, 1.8)}
	downAll = taier.ProgressUpdate{Phase: taier.PhaseDownload, Threads: 8, Fraction: 1, Final: true, Mbps: 965.7,
		Bytes: 621_600_000, Total: 5300 * ms, Latency: delay(30, 14.9, 13, 23.6, 3.1)}
	upOne = taier.ProgressUpdate{Phase: taier.PhaseUpload, Threads: 1, Fraction: 1, Final: true, Mbps: 113.5,
		Bytes: 105_200_000, Total: 7 * time.Second, Latency: delay(40, 6.9, 5.8, 122.2, 15.1)}
)

var stages = []taier.Stage{
	{Phase: taier.PhaseDownload, Threads: 1}, {Phase: taier.PhaseDownload, Threads: 8},
	{Phase: taier.PhaseUpload, Threads: 1}, {Phase: taier.PhaseUpload, Threads: 8},
}
