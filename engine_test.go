package main

// 引擎的验证测试。都是对着 httptest 起的真服务打真实 HTTP 请求，
// 覆盖请求数模式、时长模式、中途停止、错误记录和直方图精度。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func okServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runEngine(t *testing.T, cfg Config) Snapshot {
	t.Helper()
	if err := cfg.normalize(); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	return NewEngine(cfg).Run(context.Background(), func(Snapshot) {})
}

// 请求数模式：发够数就停，一个不多一个不少，且全部落在状态码统计里。
func TestRequestsMode(t *testing.T) {
	srv := okServer(t)

	final := runEngine(t, Config{
		URL:         srv.URL,
		Method:      "GET",
		Concurrency: 8,
		Requests:    200,
		Timeout:     3000,
	})

	if final.Requests != 200 {
		t.Errorf("请求数 = %d，期望 200", final.Requests)
	}
	if final.Errors != 0 {
		t.Errorf("错误数 = %d，期望 0（错误分布 %v）", final.Errors, final.ErrorMap)
	}
	if got := final.StatusCodes["200"]; got != 200 {
		t.Errorf("200 计数 = %d，期望 200", got)
	}
	if final.AvgMs <= 0 || final.P99Ms <= 0 {
		t.Errorf("延迟统计没记上: 平均 %v P99 %v", final.AvgMs, final.P99Ms)
	}
	if final.P99Ms < final.AvgMs {
		t.Errorf("P99 (%v) 不该小于平均值 (%v)", final.P99Ms, final.AvgMs)
	}
	if len(final.LatencyBins) == 0 {
		t.Error("延迟分布是空的")
	}
	if final.Bytes <= 0 {
		t.Errorf("响应字节数 = %d，期望大于 0", final.Bytes)
	}
}

// 时长模式：跑够设定的秒数，且中途有实时快照交出来。
func TestDurationMode(t *testing.T) {
	srv := okServer(t)

	cfg := Config{URL: srv.URL, Method: "GET", Concurrency: 4, Duration: 1, Timeout: 3000}
	_ = cfg.normalize()

	var ticks atomic.Int32
	start := time.Now()
	final := NewEngine(cfg).Run(context.Background(), func(Snapshot) { ticks.Add(1) })
	elapsed := time.Since(start)

	if elapsed < 900*time.Millisecond {
		t.Errorf("只跑了 %v，没到设定的 1 秒", elapsed)
	}
	if elapsed > 4*time.Second {
		t.Errorf("跑了 %v，超出设定太多", elapsed)
	}
	if final.Requests == 0 {
		t.Error("一个请求都没发出去")
	}
	// 采样间隔 500ms，跑 1 秒至少该交出一份快照
	if ticks.Load() == 0 {
		t.Error("运行期间没有交出实时快照")
	}
}

// context 取消也要能让压测收工。
func TestContextCancel(t *testing.T) {
	srv := okServer(t)

	cfg := Config{URL: srv.URL, Method: "GET", Concurrency: 4, Duration: 60, Timeout: 3000}
	_ = cfg.normalize()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		NewEngine(cfg).Run(ctx, func(Snapshot) {})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消 context 后压测没有退出")
	}
}

// 连不上的目标：错误要记进分布，而不是把进程带崩。
func TestConnectionErrorsAreRecorded(t *testing.T) {
	cfg := Config{URL: "http://127.0.0.1:1/", Method: "GET", Concurrency: 2, Duration: 1, Timeout: 300}
	_ = cfg.normalize()

	final := NewEngine(cfg).Run(context.Background(), func(Snapshot) {})

	if final.Requests != 0 {
		t.Errorf("连不上却记了 %d 个成功请求", final.Requests)
	}
	if final.Errors == 0 {
		t.Error("期望记录到连接错误")
	}
	if len(final.ErrorMap) == 0 {
		t.Error("错误分布是空的")
	}
}

// 非 2xx 也是有效响应：要计入请求数和状态码分布，不该算传输层错误。
func TestNon2xxCountsAsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	final := runEngine(t, Config{
		URL:         srv.URL,
		Method:      "GET",
		Concurrency: 4,
		Requests:    50,
		Timeout:     3000,
	})

	if final.Requests != 50 {
		t.Errorf("请求数 = %d，期望 50", final.Requests)
	}
	if got := final.StatusCodes["503"]; got != 50 {
		t.Errorf("503 计数 = %d，期望 50", got)
	}
	if final.Errors != 0 {
		t.Errorf("503 是有效响应，不该算传输层错误，却记了 %d 个", final.Errors)
	}
}

// 请求头和请求体要真的发出去。
func TestHeadersAndBodyReachServer(t *testing.T) {
	var gotHeader, gotBody atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader.Store(r.Header.Get("X-Probe"))
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody.Store(string(buf[:n]))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	runEngine(t, Config{
		URL:         srv.URL,
		Method:      "POST",
		Concurrency: 1,
		Requests:    1,
		Timeout:     3000,
		Headers:     map[string]string{"X-Probe": "probe-value"},
		Body:        `{"k":"v"}`,
	})

	if h, _ := gotHeader.Load().(string); h != "probe-value" {
		t.Errorf("服务端收到的 X-Probe = %q", h)
	}
	if b, _ := gotBody.Load().(string); b != `{"k":"v"}` {
		t.Errorf("服务端收到的 body = %q", b)
	}
}

// 配置校验：地址和证书要拦住明显写错的输入。
func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"空地址", Config{URL: "  "}},
		{"缺协议", Config{URL: "example.com/api"}},
		{"协议不对", Config{URL: "ftp://example.com"}},
		{"只有证书没有私钥", Config{URL: "http://example.com", ClientCert: "a.crt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.cfg.normalize(); err == nil {
				t.Error("期望报错，却通过了")
			}
		})
	}

	// 合理的输入要被补上默认值
	ok := Config{URL: "http://example.com"}
	if err := ok.normalize(); err != nil {
		t.Fatalf("正常配置被拒: %v", err)
	}
	if ok.Concurrency != 3 || ok.Duration != 3 || ok.Timeout != 3000 || ok.Method != "GET" {
		t.Errorf("默认值没补齐: %+v", ok)
	}
}

// 直方图的量程必须盖得住引擎允许的最长超时。
//
// 超时的样本一旦落进最后一个桶就分不出快慢了 —— 最后一个桶收的是所有超出
// 量程的样本，400 秒和 600 秒会显示成同一个延迟。改 maxTimeoutMs 或
// histMaxExp 时这个测试会拦住不配套的改动。
func TestHistogramCoversMaxTimeout(t *testing.T) {
	maxNs := int64(maxTimeoutMs) * int64(time.Millisecond)
	if idx := bucketOf(maxNs); idx >= histBuckets-1 {
		t.Errorf("超时上限 %d 毫秒落进了最后一个桶（下标 %d / 共 %d 个），百分位没法分辨；"+
			"要么调小 maxTimeoutMs，要么调大 stats.go 的 histMaxExp",
			maxTimeoutMs, idx, histBuckets)
	}
}

// 直方图：分桶不能把样本落到比它大的区间里，百分位要落在合理范围。
func TestHistogramBucketing(t *testing.T) {
	for _, ns := range []int64{1024, 1500, 5000, 1e6, 1e9, 30e9} {
		idx := bucketOf(ns)
		if lower := bucketLower(idx); lower > ns {
			t.Errorf("样本 %dns 落进桶 %d，但桶下界是 %dns，比样本还大", ns, idx, lower)
		}
		if idx < 0 || idx >= histBuckets {
			t.Errorf("样本 %dns 算出越界下标 %d", ns, idx)
		}
	}
}

func TestHistogramPercentiles(t *testing.T) {
	var h Histogram
	// 1μs 到 1000μs 各记一次，P50 该在 500μs 附近
	for i := 1; i <= 1000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}

	if h.Total() != 1000 {
		t.Fatalf("样本数 = %d，期望 1000", h.Total())
	}
	if h.Min() != time.Microsecond {
		t.Errorf("最小值 = %v，期望 1μs", h.Min())
	}
	if h.Max() != 1000*time.Microsecond {
		t.Errorf("最大值 = %v，期望 1000μs", h.Max())
	}

	// 分桶有约 3% 的相对误差，给到 10% 的宽容度
	for _, c := range []struct {
		p    float64
		want time.Duration
	}{
		{0.50, 500 * time.Microsecond},
		{0.90, 900 * time.Microsecond},
		{0.99, 990 * time.Microsecond},
	} {
		got := h.Percentile(c.p)
		if diff := float64(got-c.want) / float64(c.want); diff > 0.10 || diff < -0.10 {
			t.Errorf("P%.0f = %v，期望约 %v", c.p*100, got, c.want)
		}
	}

	if mean := h.Mean(); mean < 490*time.Microsecond || mean > 510*time.Microsecond {
		t.Errorf("平均值 = %v，期望约 500μs", mean)
	}
}

// 标准差是拿桶中点反算的，得确认分桶近似没把它算跑偏。
func TestHistogramStdDev(t *testing.T) {
	// 样本完全一致：没有抖动
	var same Histogram
	for i := 0; i < 500; i++ {
		same.Record(20 * time.Millisecond)
	}
	if sd := same.StdDev(); sd > time.Millisecond {
		t.Errorf("样本全是 20ms，标准差该接近 0，实际 %v", sd)
	}

	// 一半 10ms 一半 30ms：均值 20ms，标准差正好 10ms
	var split Histogram
	for i := 0; i < 500; i++ {
		split.Record(10 * time.Millisecond)
		split.Record(30 * time.Millisecond)
	}
	// 分桶有约 3% 的误差，给到 15% 的宽容度
	if sd := split.StdDev(); sd < 8500*time.Microsecond || sd > 11500*time.Microsecond {
		t.Errorf("一半 10ms 一半 30ms，标准差该在 10ms 附近，实际 %v", sd)
	}

	// 样本不够时不给结论
	var few Histogram
	if sd := few.StdDev(); sd != 0 {
		t.Errorf("没有样本时该给 0，实际 %v", sd)
	}
	few.Record(5 * time.Millisecond)
	if sd := few.StdDev(); sd != 0 {
		t.Errorf("只有一个样本时该给 0，实际 %v", sd)
	}
}

// 分布里中间的空桶不能省：前端按索引等距画，省掉空桶位置就错了。
func TestBinsKeepEmptyBuckets(t *testing.T) {
	var h Histogram
	h.Record(10 * time.Millisecond)
	h.Record(1000 * time.Millisecond)

	bins := h.Bins()
	if len(bins) < 50 {
		t.Fatalf("中间的空桶被省掉了，只剩 %d 个桶；"+
			"前端画出来 10ms 和 1s 会挨在一起，看着只差一点", len(bins))
	}

	empties := 0
	for _, b := range bins {
		if b.Count == 0 {
			empties++
		}
	}
	if empties == 0 {
		t.Error("10ms 和 1s 之间该是一片空桶")
	}

	// 两个有样本的桶还在，而且落在首尾
	if bins[0].Count != 1 || bins[len(bins)-1].Count != 1 {
		t.Errorf("首尾桶的计数不对: %d, %d", bins[0].Count, bins[len(bins)-1].Count)
	}
	// 桶记的是下界，所以头一个略小于 10ms、末一个略小于 1s
	if bins[0].LowerMs > 10 || bins[len(bins)-1].LowerMs < 900 {
		t.Errorf("分布范围不对: %v ~ %v", bins[0].LowerMs, bins[len(bins)-1].LowerMs)
	}

	// 下界必须是递增的，前端才知道怎么摆柱子
	for i := 1; i < len(bins); i++ {
		if bins[i].LowerMs <= bins[i-1].LowerMs {
			t.Fatalf("第 %d 个桶的下界没有递增: %v 在 %v 之后", i, bins[i].LowerMs, bins[i-1].LowerMs)
		}
	}

	// 每个桶得上界大于下界，而且要正好接上下一个桶 —— 中间不能有缝，
	// 有缝说明前端算位置时会有偏差
	for i, b := range bins {
		if b.UpperMs <= b.LowerMs {
			t.Fatalf("第 %d 个桶的上界 %v 不大于下界 %v", i, b.UpperMs, b.LowerMs)
		}
		if i+1 < len(bins) && bins[i+1].LowerMs != b.UpperMs {
			t.Fatalf("第 %d 个桶的上界 %v 和下一个桶的下界 %v 对不上", i, b.UpperMs, bins[i+1].LowerMs)
		}
	}
}

// 一个样本都没有时给空切片，前端据此显示空态。
func TestBinsEmptyWhenNoSamples(t *testing.T) {
	var h Histogram
	if bins := h.Bins(); bins == nil {
		t.Error("没有样本时该给空切片，不是 nil —— nil 序列化成 null，和前端声明的数组对不上")
	} else if len(bins) != 0 {
		t.Errorf("没有样本时该是空的，实际有 %d 个桶", len(bins))
	}
}

// 超大数值要夹住：不夹的话 time.Duration 乘法会溢出，时长可能变成负数
// （压测几十微秒就"跑完"），超时变成负数后每个请求瞬间失败。
func TestNormalizeClampsHugeNumbers(t *testing.T) {
	c := Config{URL: "https://example.com", Duration: 1 << 40, Timeout: 1 << 40}
	if err := c.normalize(); err != nil {
		t.Fatalf("正常配置被拒: %v", err)
	}
	if c.Duration > maxDurationSec {
		t.Errorf("时长没夹住: %d", c.Duration)
	}
	if c.Timeout > maxTimeoutMs {
		t.Errorf("超时没夹住: %d", c.Timeout)
	}
	if time.Duration(c.Duration)*time.Second < 0 {
		t.Error("夹完之后时长还是会溢出成负数")
	}
	if time.Duration(c.Timeout)*time.Millisecond < 0 {
		t.Error("夹完之后超时还是会溢出成负数")
	}
}

// 用户自己按的停止不算目标的错：读响应体读到一半被打断，
// 不该在错误分布里记一条 context canceled。
func TestContextCancelDoesNotCountAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 4096))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done() // 挂着不结束，等客户端断开
	}))
	t.Cleanup(srv.Close)

	cfg := Config{URL: srv.URL, Method: "GET", Concurrency: 4, Duration: 60, Timeout: 30000}
	_ = cfg.normalize()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	final := NewEngine(cfg).Run(ctx, func(Snapshot) {})

	if final.Errors != 0 {
		t.Errorf("自己取消不该算目标的错，却记了 %d 个: %v", final.Errors, final.ErrorMap)
	}
}

// 自然跑完的最终快照不能标记成「运行中」—— stopped 标志只在手点停止时置位。
func TestFinalSnapshotIsNotRunning(t *testing.T) {
	srv := okServer(t)

	final := runEngine(t, Config{
		URL:         srv.URL,
		Method:      "GET",
		Concurrency: 2,
		Requests:    20,
		Timeout:     3000,
	})
	if final.Running {
		t.Error("自然跑完的快照仍是 running=true")
	}
}

// 采样窗口太短时，瞬时 QPS 该沿用上一次的值。
//
// 压测收尾那一帧距上一个 tick 可能只有几毫秒，三个请求就能算出 800 的 QPS，
// 界面上看着像吞吐突然爆了 —— 实测见过 544、819 和 0.0，而全程平均稳稳
// 在 143。
func TestQPSIgnoresTinyWindow(t *testing.T) {
	cfg := Config{URL: "http://example.com", Concurrency: 1, Duration: 1, Timeout: 1000}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(cfg)

	// 第一个窗口：500ms 里完成 150 个 → 300 QPS
	eng.reqs.Store(150)
	eng.lastTick = time.Now().Add(-500 * time.Millisecond)
	first := eng.snapshot(true)
	if first.QPS < 250 || first.QPS > 350 {
		t.Fatalf("500ms 窗口完成 150 个，QPS 该在 300 附近，实际 %.1f", first.QPS)
	}

	// 3ms 后又完成 3 个 —— 直接算会是 1000，但该沿用上一次的值
	eng.reqs.Add(3)
	eng.lastTick = time.Now().Add(-3 * time.Millisecond)
	second := eng.snapshot(true)

	if second.QPS != first.QPS {
		t.Errorf("窗口只有 3ms 时不该重算：上次 %.1f，这次 %.1f", first.QPS, second.QPS)
	}
	if second.Requests != 153 {
		t.Errorf("请求数还是该照常累加，实际 %d", second.Requests)
	}
}

// 请求数模式不能被表单里残留的时长砍掉。
// 界面上填了请求数之后，时长输入框只是禁用、值还在，会一路发到后端。
func TestRequestsModeIgnoresDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // 拖慢，保证 1 秒里发不满
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		URL:         srv.URL,
		Method:      "GET",
		Concurrency: 1,
		Requests:    20, // 每个 200ms，要 4 秒才发得完
		Duration:    1,  // 界面上残留的值 —— 不该生效
		Timeout:     10000,
	}
	_ = cfg.normalize()

	final := NewEngine(cfg).Run(context.Background(), func(Snapshot) {})

	if final.Requests != 20 {
		t.Errorf("请求数模式该发满 20 个，实际 %d 个 —— 被 Duration 截断了", final.Requests)
	}
}

// 服务端实际收到的请求数要正好等于设定值，一个不多一个不少。
// worker 是靠共享计数器领配额的，多并发下也不能超发。
func TestRequestsModeSendsExactCount(t *testing.T) {
	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	for _, want := range []int{1, 7, 100} {
		received.Store(0)

		cfg := Config{
			URL:         srv.URL,
			Method:      "GET",
			Concurrency: 4,
			Requests:    want,
			Timeout:     3000,
		}
		_ = cfg.normalize()

		final := NewEngine(cfg).Run(context.Background(), func(Snapshot) {})

		if got := received.Load(); got != int64(want) {
			t.Errorf("设定 %d 个，服务端收到 %d 个", want, got)
		}
		if final.Requests != int64(want) {
			t.Errorf("设定 %d 个，引擎记了 %d 个", want, final.Requests)
		}
	}
}
