package taier

import (
	"errors"
	"math"
	"slices"
	"testing"
	"time"
)

// simulate 按 sampleInterval 向 meter 喂入累计字节数，rate(at) 给出 at 时刻之前一个采样间隔内的吞吐(Mbps)。
func simulate(m *meter, rate func(at time.Duration) float64) (stopAt time.Duration, bytes int64) {
	var total float64
	for at := sampleInterval; at <= time.Minute; at += sampleInterval {
		total += rate(at) * 1e6 / 8 * sampleInterval.Seconds()
		if _, _, stop := m.observe(m.start.Add(at), int64(total)); stop {
			return at, int64(total)
		}
	}
	panic("meter never stopped")
}

func newTestMeter(max time.Duration) *meter {
	return &meter{start: time.Unix(1000, 0), grace: 1500 * time.Millisecond, min: minMeasure, max: max}
}

func TestMeterStopsWhenStable(t *testing.T) {
	m := newTestMeter(10 * time.Second)
	stopAt, bytes := simulate(m, func(time.Duration) float64 { return 100 })
	mbps, measured, stable := m.result(m.start.Add(stopAt), bytes)
	// 预热 1.5 秒，随后 3 个 1 秒窗口一致，计量满 3 秒即停止。
	if !stable || measured != 3*time.Second || stopAt != 4500*time.Millisecond || math.Abs(mbps-100) > 0.01 {
		t.Errorf("stopAt=%v measured=%v stable=%v mbps=%.2f", stopAt, measured, stable, mbps)
	}
}

func TestMeterExcludesWarmup(t *testing.T) {
	m := newTestMeter(10 * time.Second)
	// 慢启动期间只有 10 Mbps，之后稳定在 100 Mbps；结果不应被预热期拉低。
	stopAt, bytes := simulate(m, func(at time.Duration) float64 {
		if at <= 1500*time.Millisecond {
			return 10
		}
		return 100
	})
	if mbps, _, stable := m.result(m.start.Add(stopAt), bytes); !stable || math.Abs(mbps-100) > 0.01 {
		t.Errorf("mbps=%.2f stable=%v, want 100 and stable", mbps, stable)
	}
}

func TestMeterRunsToMaxWhenUnstable(t *testing.T) {
	m := newTestMeter(6 * time.Second)
	// 以计量起点对齐，每个 1 秒窗口交替 80/120 Mbps，偏差 20%，不会收敛。
	stopAt, bytes := simulate(m, func(at time.Duration) float64 {
		if at <= 1500*time.Millisecond {
			return 100
		}
		if int((at-1500*time.Millisecond-1)/time.Second)%2 == 0 {
			return 80
		}
		return 120
	})
	mbps, measured, stable := m.result(m.start.Add(stopAt), bytes)
	if stable || measured != 6*time.Second || math.Abs(mbps-100) > 0.01 {
		t.Errorf("measured=%v stable=%v mbps=%.2f, want 6s unstable ~100", measured, stable, mbps)
	}
}

func TestMeterNoDataDuringWarmup(t *testing.T) {
	m := newTestMeter(4 * time.Second)
	// 预热结束时仍未收到数据：从头计量，而不是以预热结束时刻为起点。
	stopAt, bytes := simulate(m, func(at time.Duration) float64 {
		if at <= 2*time.Second {
			return 0
		}
		return 50
	})
	if !m.measureStart().Equal(m.start) {
		t.Errorf("measure start = %v, want phase start", m.measureStart())
	}
	if mbps, measured, _ := m.result(m.start.Add(stopAt), bytes); measured != stopAt || mbps <= 0 || mbps >= 50 {
		t.Errorf("measured=%v mbps=%.2f, want measured from start", measured, mbps)
	}
}

func TestConverged(t *testing.T) {
	tests := []struct {
		windows []float64
		want    bool
	}{
		{[]float64{100, 103, 97}, true},
		{[]float64{100, 110, 100}, false}, // 110 偏离均值 6.5%
		{[]float64{100, 100}, false},      // 窗口不足
		{[]float64{0, 0, 0}, false},
		{[]float64{10, 100, 101, 99}, true}, // 只看最近 3 个窗口
	}
	for _, tt := range tests {
		if got := converged(tt.windows); got != tt.want {
			t.Errorf("converged(%v) = %v, want %v", tt.windows, got, tt.want)
		}
	}
}

func TestNewLatencyStats(t *testing.T) {
	ms := func(v ...int) []time.Duration {
		out := make([]time.Duration, len(v))
		for i, x := range v {
			out[i] = time.Duration(x) * time.Millisecond
		}
		return out
	}
	s := newLatencyStats(ms(10, 20, 10, 20), 1)
	// 抖动按时间顺序计算：|20-10|、|10-20|、|20-10| 的平均为 10ms(按排序后计算会得到错误的 3.3ms)。
	if s.Samples != 4 || s.Min != 10*time.Millisecond || s.Max != 20*time.Millisecond ||
		s.Median != 15*time.Millisecond || s.Jitter != 10*time.Millisecond || s.FailRate() != 20 {
		t.Errorf("stats = %+v failRate=%v", s, s.FailRate())
	}
	if s := newLatencyStats(ms(30, 10, 20), 0); s.Median != 20*time.Millisecond || s.Jitter != 15*time.Millisecond {
		t.Errorf("odd stats = %+v", s)
	}
	if s := newLatencyStats(nil, 2); s.Samples != 0 || s.FailRate() != 100 {
		t.Errorf("empty stats = %+v", s)
	}
}

func TestErrorLog(t *testing.T) {
	var l errorLog
	if l.list() != nil {
		t.Error("empty log should list nil")
	}
	for _, msg := range []string{"a", "b", "a", "a"} {
		l.add(errors.New(msg))
	}
	if got, want := l.list(), []string{"a (×3)", "b"}; !slices.Equal(got, want) {
		t.Errorf("list = %v, want %v", got, want)
	}
}

func TestStages(t *testing.T) {
	for _, tt := range []struct {
		opts Options
		want []Stage
	}{
		{Options{}, []Stage{{PhaseDownload, 8}, {PhaseUpload, 8}}},
		{Options{SingleThread: true, Threads: 4}, []Stage{{PhaseDownload, 1}, {PhaseDownload, 4}, {PhaseUpload, 1}, {PhaseUpload, 4}}},
		{Options{SingleThread: true, Threads: 1}, []Stage{{PhaseDownload, 1}, {PhaseUpload, 1}}},
		{Options{SingleThread: true, SkipDownload: true}, []Stage{{PhaseUpload, 1}, {PhaseUpload, 8}}},
		{Options{SkipDownload: true, SkipUpload: true}, nil},
	} {
		if got := tt.opts.Stages(); !slices.Equal(got, tt.want) {
			t.Errorf("Stages(%+v) = %v, want %v", tt.opts, got, tt.want)
		}
	}
}

func TestBudget(t *testing.T) {
	tests := []struct {
		opts Options
		want time.Duration
	}{
		{Options{}, 44500 * time.Millisecond},
		{Options{SkipUpload: true}, 31500 * time.Millisecond},
		{Options{Seconds: 5, SkipDownload: true}, 28 * time.Second},
		{Options{SkipDownload: true, SkipUpload: true}, 20 * time.Second},
		// 加测单线程时每个方向多一个阶段；Threads 为 1 时已是单线程，不重复。
		{Options{SingleThread: true}, 69 * time.Second},
		{Options{SingleThread: true, Threads: 1}, 44500 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := tt.opts.Budget(); got != tt.want {
			t.Errorf("Budget(%+v) = %v, want %v", tt.opts, got, tt.want)
		}
	}
}
