package main

// 请求明细缓冲和文本截断的测试。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRecordBufferIncremental(t *testing.T) {
	b := newRecordBuffer()
	for i := 0; i < 5; i++ {
		b.Add(RequestRecord{URL: fmt.Sprintf("https://x/%d", i)})
	}

	all := b.Since(0)
	if len(all) != 5 {
		t.Fatalf("该有 5 条，实际 %d", len(all))
	}
	for i, r := range all {
		if r.Seq != int64(i+1) {
			t.Errorf("第 %d 条 seq = %d，期望 %d", i, r.Seq, i+1)
		}
	}

	// 前端带上已拿到的最大 seq，只该拿到比它新的
	got := b.Since(3)
	if len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Errorf("增量拉取结果不对: %+v", got)
	}

	if extra := b.Since(5); len(extra) != 0 {
		t.Errorf("已经没有新记录，却拿到 %d 条", len(extra))
	}
}

// 缓冲满了要覆盖最旧的，而且拿出来的顺序必须还是递增的。
func TestRecordBufferWraps(t *testing.T) {
	b := newRecordBuffer()
	total := maxRecords + 50
	for i := 0; i < total; i++ {
		b.Add(RequestRecord{URL: "https://x"})
	}

	got := b.Since(0)
	if len(got) != maxRecords {
		t.Fatalf("缓冲该只剩 %d 条，实际 %d", maxRecords, len(got))
	}
	if got[0].Seq != 51 {
		t.Errorf("最早一条 seq = %d，期望 51（前 50 条该被顶掉）", got[0].Seq)
	}
	if got[len(got)-1].Seq != int64(total) {
		t.Errorf("最新一条 seq = %d，期望 %d", got[len(got)-1].Seq, total)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("第 %d 条的 seq 没有递增: %d 在 %d 之后", i, got[i].Seq, got[i-1].Seq)
		}
	}
}

func TestCutString(t *testing.T) {
	if s, cut := cutString("hello", 10); s != "hello" || cut {
		t.Errorf("没超限不该截断: %q cut=%v", s, cut)
	}
	if s, cut := cutString("hello world", 5); s != "hello" || !cut {
		t.Errorf("截断结果 %q cut=%v", s, cut)
	}

	// 切点落在中文中间时不能切出半个字
	zh := "中文测试内容"
	if _, cut := cutString(zh, 4); !cut {
		t.Error("该标记为截断")
	}
	s, _ := cutString(zh, 4)
	if !utf8.ValidString(s) {
		t.Errorf("切出了无效 UTF-8: %q", s)
	}
	if s != "中" {
		t.Errorf("切到第 4 字节，结果该是 %q，实际 %q", "中", s)
	}
}

func TestTrimPartialRune(t *testing.T) {
	if got := trimPartialRune("完整的中文"); got != "完整的中文" {
		t.Errorf("完整字符串被改坏了: %q", got)
	}
	if got := trimPartialRune(""); got != "" {
		t.Errorf("空串该原样返回，实际 %q", got)
	}
	if got := trimPartialRune("abc"); got != "abc" {
		t.Errorf("纯 ASCII 被改坏了: %q", got)
	}

	// 「文」三字节只留两字节 —— 尾巴上挂着半个字
	broken := "中文"[:5]
	if got := trimPartialRune(broken); got != "中" {
		t.Errorf("trimPartialRune(%q) = %q，期望 %q", broken, got, "中")
	}
}

func TestLimitedWriterKeepsHeadAndCountsAll(t *testing.T) {
	w := &limitedWriter{limit: 4}
	payload := "0123456789"

	n, err := w.Write([]byte(payload))
	if err != nil {
		t.Fatalf("Write 报错: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write 返回 %d，期望 %d —— 必须全部“消费”掉，否则 io.Copy 会提前收工", n, len(payload))
	}
	if string(w.buf) != "0123" {
		t.Errorf("截留内容 = %q，期望 %q", string(w.buf), "0123")
	}
	if !w.cut {
		t.Error("该标记为截断过")
	}
}

// 开了记录详情：请求头、响应头、响应体都要在明细里。
func TestEngineRecordsDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Answer", "42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		URL:          srv.URL,
		Method:       "POST",
		Concurrency:  1,
		Requests:     1,
		Timeout:      3000,
		Headers:      map[string]string{"X-Probe": "yes"},
		Body:         `{"q":"hi"}`,
		RecordDetail: true,
	}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(cfg)
	engine.Run(context.Background(), func(Snapshot) {})

	got := engine.records.Since(0)
	if len(got) != 1 {
		t.Fatalf("该有 1 条明细，实际 %d", len(got))
	}
	r := got[0]

	if r.Method != "POST" || r.Status != 200 {
		t.Errorf("明细基本字段不对: %+v", r)
	}
	if r.ReqHeaders["X-Probe"] != "yes" {
		t.Errorf("请求头没记上: %v", r.ReqHeaders)
	}
	if r.ReqHeaders["User-Agent"] != userAgent {
		t.Errorf("User-Agent 没记上: %v", r.ReqHeaders)
	}
	if r.RespHeaders["X-Answer"] != "42" {
		t.Errorf("响应头没记上: %v", r.RespHeaders)
	}
	if r.ReqBody != `{"q":"hi"}` {
		t.Errorf("请求体没记上: %q", r.ReqBody)
	}
	if !strings.Contains(r.RespBody, `"ok":true`) {
		t.Errorf("响应体没记上: %q", r.RespBody)
	}
	if r.Size <= 0 {
		t.Errorf("大小没记上: %d", r.Size)
	}
}

// 没开记录详情：只留摘要，不该带 header 和 body。
func TestEngineSkipsDetailWhenOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{URL: srv.URL, Method: "GET", Concurrency: 1, Requests: 1, Timeout: 3000}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(cfg)
	engine.Run(context.Background(), func(Snapshot) {})

	got := engine.records.Since(0)
	if len(got) != 1 {
		t.Fatalf("该有 1 条明细，实际 %d", len(got))
	}
	r := got[0]
	if r.Status != 200 || r.Size <= 0 {
		t.Errorf("摘要字段不该缺: %+v", r)
	}
	if r.ReqHeaders != nil || r.RespHeaders != nil || r.RespBody != "" {
		t.Errorf("没开记录详情却记了内容: %+v", r)
	}
}

// 连不上时也要留一条明细，错误原因写在里面。
func TestEngineRecordsFailures(t *testing.T) {
	cfg := Config{URL: "http://127.0.0.1:1/", Method: "GET", Concurrency: 1, Duration: 1, Timeout: 300}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine(cfg)
	engine.Run(context.Background(), func(Snapshot) {})

	got := engine.records.Since(0)
	if len(got) == 0 {
		t.Fatal("失败的请求也该留下明细")
	}
	if got[0].Error == "" {
		t.Errorf("明细里没写失败原因: %+v", got[0])
	}
	if got[0].Status != 0 {
		t.Errorf("没拿到响应，状态码该是 0，实际 %d", got[0].Status)
	}
}

// 明细里的开始时间得是真实的墙钟时间，不能是零值或者相对秒数。
func TestRecordCarriesWallClock(t *testing.T) {
	srv := okServer(t)

	before := time.Now().UnixMilli()
	cfg := Config{URL: srv.URL, Method: "GET", Concurrency: 1, Requests: 1, Timeout: 3000}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(cfg)
	started := engine.started
	engine.Run(context.Background(), func(Snapshot) {})
	after := time.Now().UnixMilli()

	got := engine.records.Since(0)
	if len(got) != 1 {
		t.Fatalf("该有 1 条明细，实际 %d", len(got))
	}
	start := got[0].StartMs
	if start < before || start > after {
		t.Errorf("开始时间 %d 落在压测区间 [%d, %d] 之外", start, before, after)
	}
	if got[0].StartMs == 0 {
		t.Error("开始时间是零值")
	}

	// 相对秒数得和墙钟时间对得上。不能直接断言 At > 0 ——
	// 第一个请求可能和引擎启动落在同一个时钟刻度里，那时 At 就是 0。
	wantAt := float64(got[0].StartMs-started.UnixMilli()) / 1000
	if diff := got[0].At - wantAt; diff > 0.05 || diff < -0.05 {
		t.Errorf("相对秒数 %v 和墙钟时间对不上（按开始时间算该是 %v）", got[0].At, wantAt)
	}
}
