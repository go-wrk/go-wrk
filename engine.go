package main

// 压测引擎。
//
// 配置项的语义对齐 go-wrk 的命令行参数（-c 并发、-d 时长、-T 超时、-M 方法、
// -H 请求头、-body、-host、-no-c、-no-ka、-no-vr、-redir、-http、-cert/-key/-ca），
// 另外补了 go-wrk 没有的 -n 请求数模式。
//
// 和原版实现的关键差别，都是为了在桌面端能实时看到过程：
//   - 每个 worker 独占一个 http.Client，并发数就是真实连接数，也避免共享连接池的锁竞争
//   - 统计全部走原子操作和固定桶直方图，主循环可以随时读取，不需要等 worker 结束
//   - 响应体用 io.Copy 丢弃，只数字节不占内存
//   - 客户端构造失败只让这一个 worker 退出并记一条错误，不会把整个应用带崩

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 版本号由构建时注入（wails build -ldflags "-X main.version=x.y.z"），
// 本地直接 build 就是 dev。
var version = "dev"

// userAgent 带上版本，目标端的日志里能认出是谁在压。
var userAgent = "go-wrk-desktop/" + version

// 并发数上限。再高就该用分布式压测了，单机开放更多连接只会先耗尽本地端口。
const maxConcurrency = 10000

// 时长和超时的上限。不夹的话 time.Duration 的乘法会溢出：填个 13 位的数进去，
// 时长可能变成负数（压测 57µs 就「跑完」）或者 59 年；超时变成负数后，
// net.Dialer 当它已过期、http.Client 干脆不设超时，每个请求瞬间失败，
// 全被记成「请求超时」—— 好目标被诊断成坏的。
const (
	maxDurationSec = 24 * 3600 // 单轮最长 24 小时
	// 单个请求最长 10 分钟。这个数要落在直方图的量程内（stats.go 里最大桶的
	// 上界约 2200 秒），超了的话更慢的样本会全挤进最后一个桶，百分位就不可分辨了。
	maxTimeoutMs = 600_000
)

// 算瞬时 QPS 的最小采样窗口（秒）。正常的话每 500ms 采一次，
// 但压测收尾那一次距上一个 tick 可能只有几毫秒 —— 短于这个长度的窗口
// 算出来只是噪声，几个请求就能显示出几百甚至上千的 QPS。
const minQPSWindow = 0.2

// Config 一轮压测的全部输入。
type Config struct {
	URL            string            `json:"url"`
	Method         string            `json:"method"`         // -M，默认 GET
	Concurrency    int               `json:"concurrency"`    // -c，并发连接数
	Duration       int               `json:"duration"`       // -d，时长（秒）
	Requests       int               `json:"requests"`       // -n，总请求数；>0 时优先于时长
	Timeout        int               `json:"timeout"`        // -T，超时（毫秒）
	Headers        map[string]string `json:"headers"`        // -H
	Body           string            `json:"body"`           // -body
	Host           string            `json:"host"`           // -host，覆盖 Host 头
	NoCompression  bool              `json:"noCompression"`  // -no-c
	NoKeepAlive    bool              `json:"noKeepAlive"`    // -no-ka
	SkipVerify     bool              `json:"skipVerify"`     // -no-vr
	AllowRedirects bool              `json:"allowRedirects"` // -redir
	HTTP2          bool              `json:"http2"`          // -http
	ClientCert     string            `json:"clientCert"`     // -cert
	ClientKey      string            `json:"clientKey"`      // -key
	CACert         string            `json:"caCert"`         // -ca

	// 不是 go-wrk 的参数，是本应用加的：连请求头和响应内容一起记进明细，
	// 供界面上的「请求明细」展开查看。开了每个请求会多分配一点内存。
	RecordDetail bool `json:"recordDetail"`
}

// normalize 校验并补齐默认值，就地修改。
func (c *Config) normalize() error {
	c.URL = strings.TrimSpace(c.URL)
	if c.URL == "" {
		return errors.New("请填写目标地址")
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("目标地址解析失败：%v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("目标地址要以 http:// 或 https:// 开头")
	}
	if u.Host == "" {
		return errors.New("目标地址里没有主机名")
	}
	c.URL = u.String()

	c.Method = strings.ToUpper(strings.TrimSpace(c.Method))
	if c.Method == "" {
		c.Method = http.MethodGet
	}

	if c.Concurrency < 1 {
		c.Concurrency = 3
	}
	if c.Concurrency > maxConcurrency {
		c.Concurrency = maxConcurrency
	}
	if c.Timeout < 1 {
		c.Timeout = 3000
	}
	if c.Timeout > maxTimeoutMs {
		c.Timeout = maxTimeoutMs
	}
	if c.Requests < 0 {
		c.Requests = 0
	}
	if c.Requests == 0 && c.Duration < 1 {
		c.Duration = 3
	}
	if c.Duration > maxDurationSec {
		c.Duration = maxDurationSec
	}
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	if (c.ClientCert == "") != (c.ClientKey == "") {
		return errors.New("客户端证书和私钥要一起填")
	}
	return nil
}

// Engine 一轮压测的运行状态。统计字段全部原子访问，可被主循环并发读取。
type Engine struct {
	cfg Config
	// cfg.Body 的字节形式。预存一份：string 转 []byte 每次都会拷贝，
	// 高 QPS 压大 body 时就是白白制造 GC 压力，而内容整轮都不变。
	body []byte

	reqs    atomic.Int64 // 拿到响应的请求数
	errs    atomic.Int64 // 传输层错误数
	bytes   atomic.Int64 // 响应字节数
	claimed atomic.Int64 // 请求数模式下已分配出去的配额

	hist    *Histogram
	records *recordBuffer

	statusCodes [600]atomic.Int64 // 按状态码计数；索引即状态码
	otherStatus atomic.Int64      // 落在 100~599 之外的状态码

	errMu  sync.Mutex
	errMap map[string]int64

	started  time.Time
	lastReqs int64     // 上一个采样窗口结束时的请求数，用来算瞬时 QPS
	lastTick time.Time // 上一个采样时刻
	lastQPS  float64   // 上一次算出的瞬时 QPS，窗口太短时沿用
}

// NewEngine 建一个引擎。cfg 需要先 normalize。
func NewEngine(cfg Config) *Engine {
	return &Engine{
		cfg:     cfg,
		body:    []byte(cfg.Body),
		hist:    &Histogram{},
		records: newRecordBuffer(),
		errMap:  map[string]int64{},
		started: time.Now(),
	}
}

// Run 起 worker 跑压测，每 tickInterval 调一次 onTick 交出实时快照，
// 全部 worker 结束后返回最终快照。ctx 取消会让 worker 收工。
func (e *Engine) Run(ctx context.Context, onTick func(Snapshot)) Snapshot {
	e.lastTick = time.Now()

	// 时长模式跑 cfg.Duration 秒。请求数模式不受时长约束 —— 界面上填了
	// 请求数时时长输入框只是禁用、值还在，会一路发到后端，拿它当上限会把
	// 请求数模式提前砍掉，而且不报任何错。这里一律用 1 小时兜底，
	// 免得目标只收连接不响应时 worker 永远挂着。
	limit := time.Duration(e.cfg.Duration) * time.Second
	if e.cfg.Requests > 0 {
		limit = time.Hour
	}
	deadline := e.started.Add(limit)

	var wg sync.WaitGroup
	for range e.cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.worker(ctx, deadline)
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return e.snapshot(false)
		case <-ticker.C:
			onTick(e.snapshot(true))
		case <-ctx.Done():
			// 等 worker 自己收尾，它们每个请求后都会检查 ctx
			<-done
			return e.snapshot(false)
		}
	}
}

// worker 一个并发连接：独占自己的 http.Client，循环发请求直到该收工。
func (e *Engine) worker(ctx context.Context, deadline time.Time) {
	client, err := e.newClient()
	if err != nil {
		e.recordErr("建立连接失败：" + err.Error())
		return
	}
	defer client.CloseIdleConnections()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// 时长到了就收工。请求数模式下也查这一步 —— 目标只收连接、
		// 不返回响应时，光等请求数是等不到头的
		if time.Now().After(deadline) {
			return
		}
		if e.cfg.Requests > 0 {
			// 先领配额再发请求，保证总请求数正好是设定值
			if e.claimed.Add(1) > int64(e.cfg.Requests) {
				return
			}
		}

		e.oneRequest(ctx, client)
	}
}

// oneRequest 发一次请求并记账。
func (e *Engine) oneRequest(ctx context.Context, client *http.Client) {
	var body io.Reader
	if len(e.body) > 0 {
		body = bytes.NewReader(e.body)
	}

	req, err := http.NewRequestWithContext(ctx, e.cfg.Method, e.cfg.URL, body)
	if err != nil {
		msg := "拼装请求失败：" + err.Error()
		e.recordErr(msg)
		e.records.Add(RequestRecord{
			At:      time.Since(e.started).Seconds(),
			StartMs: time.Now().UnixMilli(),
			Method:  e.cfg.Method,
			URL:     e.cfg.URL,
			Error:   msg,
		})
		return
	}
	for k, v := range e.cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", userAgent)
	if e.cfg.Host != "" {
		req.Host = e.cfg.Host
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		// 用户按了停止、或者应用要退了导致的失败，不算目标的错，不记账
		if ctx.Err() != nil {
			return
		}
		msg := shortErr(err)
		e.recordErr(msg)
		e.records.Add(RequestRecord{
			At:        time.Since(e.started).Seconds(),
			StartMs:   start.UnixMilli(),
			Method:    e.cfg.Method,
			URL:       e.cfg.URL,
			LatencyMs: msOf(time.Since(start)),
			Error:     msg,
		})
		return
	}

	// 默认只数字节不读进内存。开了记录详情就顺手截留前几 KB ——
	// 用 limitedWriter 而不是 LimitReader，是为了把响应体读完整，字节数才准。
	writer := io.Writer(io.Discard)
	var captured *limitedWriter
	if e.cfg.RecordDetail {
		captured = &limitedWriter{limit: maxBodyCapture}
		writer = captured
	}

	n, copyErr := io.Copy(writer, resp.Body)
	latency := time.Since(start)
	resp.Body.Close()
	if copyErr != nil {
		// 和上面 client.Do 那条一样：用户按停止、或者应用要退了造成的
		// 失败，不算目标的错
		if ctx.Err() != nil {
			return
		}
		msg := shortErr(copyErr)
		e.recordErr(msg)
		e.records.Add(RequestRecord{
			At:        time.Since(e.started).Seconds(),
			StartMs:   start.UnixMilli(),
			Method:    e.cfg.Method,
			URL:       e.cfg.URL,
			Status:    resp.StatusCode,
			LatencyMs: msOf(latency),
			Error:     msg,
		})
		return
	}

	size := n + estimateHeaderSize(resp.Header)
	e.hist.Record(latency)
	e.reqs.Add(1)
	e.bytes.Add(size)

	if code := resp.StatusCode; code >= 100 && code < 600 {
		e.statusCodes[code].Add(1)
	} else {
		e.otherStatus.Add(1)
	}

	rec := RequestRecord{
		At:        time.Since(e.started).Seconds(),
		StartMs:   start.UnixMilli(),
		Method:    e.cfg.Method,
		URL:       e.cfg.URL,
		Status:    resp.StatusCode,
		LatencyMs: msOf(latency),
		Size:      size,
	}
	if captured != nil {
		rec.RespBody = trimPartialRune(string(captured.buf))
		rec.BodyCut = captured.cut
		e.fillRequestDetail(&rec, resp)
	}
	e.records.Add(rec)
}

// fillRequestDetail 把请求头和请求体填进记录，响应头从 resp 里抄。
// 只在开了记录详情时被调用。
func (e *Engine) fillRequestDetail(rec *RequestRecord, resp *http.Response) {
	rec.ReqHeaders = make(map[string]string, len(e.cfg.Headers)+2)
	for k, v := range e.cfg.Headers {
		rec.ReqHeaders[k] = v
	}
	rec.ReqHeaders["User-Agent"] = userAgent
	if e.cfg.Host != "" {
		rec.ReqHeaders["Host"] = e.cfg.Host
	}

	if e.cfg.Body != "" {
		body, cut := cutString(e.cfg.Body, maxBodyCapture)
		rec.ReqBody = body
		rec.BodyCut = rec.BodyCut || cut
	}

	rec.RespHeaders = make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		rec.RespHeaders[k] = strings.Join(vs, ", ")
	}
}

// newClient 按配置建一个 http.Client，供单个 worker 独占。
func (e *Engine) newClient() (*http.Client, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: e.cfg.SkipVerify}

	if e.cfg.ClientCert != "" {
		cert, err := tls.LoadX509KeyPair(e.cfg.ClientCert, e.cfg.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("加载客户端证书：%v", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	if e.cfg.CACert != "" {
		pem, err := os.ReadFile(e.cfg.CACert)
		if err != nil {
			return nil, fmt.Errorf("读取 CA 证书：%v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA 证书无法解析：%s", e.cfg.CACert)
		}
		tlsCfg.RootCAs = pool
	}

	timeout := time.Duration(e.cfg.Timeout) * time.Millisecond
	transport := &http.Transport{
		TLSClientConfig:     tlsCfg,
		DisableCompression:  e.cfg.NoCompression,
		DisableKeepAlives:   e.cfg.NoKeepAlive,
		ForceAttemptHTTP2:   e.cfg.HTTP2,
		TLSHandshakeTimeout: timeout,
		MaxIdleConnsPerHost: 2,
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
	if !e.cfg.AllowRedirects {
		// 返回 3xx 响应本身而不是报错，这样重定向也会计入状态码分布
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client, nil
}

func (e *Engine) recordErr(msg string) {
	e.errs.Add(1)
	e.errMu.Lock()
	e.errMap[msg]++
	e.errMu.Unlock()
}

// snapshot 取一份当前状态的只读快照。
// running 由调用方给 —— 自然跑完那种收工不会置 stopped 标志，不能拿它当依据。
func (e *Engine) snapshot(running bool) Snapshot {
	now := time.Now()
	elapsed := now.Sub(e.started).Seconds()
	reqs := e.reqs.Load()

	s := Snapshot{
		Running:     running,
		Elapsed:     elapsed,
		Requests:    reqs,
		Errors:      e.errs.Load(),
		Bytes:       e.bytes.Load(),
		MinMs:       msOf(e.hist.Min()),
		MaxMs:       msOf(e.hist.Max()),
		AvgMs:       msOf(e.hist.Mean()),
		StdDevMs:    msOf(e.hist.StdDev()),
		P50Ms:       msOf(e.hist.Percentile(0.50)),
		P75Ms:       msOf(e.hist.Percentile(0.75)),
		P90Ms:       msOf(e.hist.Percentile(0.90)),
		P99Ms:       msOf(e.hist.Percentile(0.99)),
		LatencyBins: e.hist.Bins(),
	}

	// 瞬时 QPS = 本窗口完成的请求数 ÷ 窗口长度。窗口太短就沿用上一次的值，
	// 不然收尾那一帧会报出几百上千的数字，看着像吞吐突然爆了。
	// 代价是跑不满 200ms 的一轮瞬时 QPS 会是 0，那种情况看平均 QPS。
	if gap := now.Sub(e.lastTick).Seconds(); gap >= minQPSWindow {
		e.lastQPS = float64(reqs-e.lastReqs) / gap
		e.lastReqs, e.lastTick = reqs, now
	}
	s.QPS = e.lastQPS
	if elapsed > 0 {
		s.AvgQPS = float64(reqs) / elapsed
	}

	s.StatusCodes = make(map[string]int64, 8)
	for code := 100; code < 600; code++ {
		if c := e.statusCodes[code].Load(); c > 0 {
			s.StatusCodes[strconv.Itoa(code)] = c
		}
	}
	if c := e.otherStatus.Load(); c > 0 {
		s.StatusCodes["其它"] = c
	}

	e.errMu.Lock()
	s.ErrorMap = make(map[string]int64, len(e.errMap))
	for k, v := range e.errMap {
		s.ErrorMap[k] = v
	}
	e.errMu.Unlock()

	return s
}

// shortErr 把请求失败的原因整理成一行人话。
//
// 先剥掉 *url.Error 的外层包装，否则每条错误都带着各自的 URL，在错误分布里
// 会变成互不相干的键。再给几类常见失败加个中文抬头 —— 目标出错时错误分布里
// 堆着一片英文，扫一眼分不清是连不上还是超时。
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "请求超时"
		}
		err = ue.Err
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "请求超时"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "域名解析失败：" + clip(dnsErr.Name)
	}

	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "证书校验失败：" + clip(certErr.Error())
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "连接失败：" + clip(opErr.Error())
	}

	return clip(err.Error())
}

// clip 太长的错误截断掉，免得一条消息占满整行。
func clip(msg string) string {
	if len(msg) > 120 {
		return msg[:120]
	}
	return msg
}

// estimateHeaderSize 估算响应头占的字节数。
func estimateHeaderSize(h http.Header) int64 {
	var n int64
	for k, vs := range h {
		n += int64(len(k) + len(": \r\n"))
		for _, v := range vs {
			n += int64(len(v))
		}
	}
	return n + 2
}
