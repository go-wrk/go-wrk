package main

// 请求明细的环形缓冲。
//
// 压测每秒能发几千个请求，全存下来内存扛不住，所以只留最近若干条 ——
// 新记录覆盖旧的。界面按 seq 拉增量，拿到的就是这段窗口内的请求。
//
// 默认只记摘要（时间、状态码、延迟、大小）。勾上「记录完整内容」之后
// 才会额外记请求头和响应体，因为给每个请求拷一份 header map 和 body
// 是有代价的，高并发下不该默认开着。

import (
	"sort"
	"sync"
	"unicode/utf8"
)

// 摘要最多留这么多条
const maxRecords = 1000

// 开了记录详情时，请求体和响应体各最多留这么长
const maxBodyCapture = 4096

// RequestRecord 一条请求的档案。
// Headers 和 Body 只在开了记录详情时才有值。
type RequestRecord struct {
	Seq       int64   `json:"seq"`
	At        float64 `json:"at"`      // 相对压测开始的秒数
	StartMs   int64   `json:"startMs"` // 请求发出的墙钟时间（Unix 毫秒）；结束时间 = 它 + 耗时
	Method    string  `json:"method"`
	URL       string  `json:"url"`
	Status    int     `json:"status"` // 0 表示压根没拿到响应
	LatencyMs float64 `json:"latencyMs"`
	Size      int64   `json:"size"`
	Error     string  `json:"error,omitempty"`

	ReqHeaders  map[string]string `json:"reqHeaders,omitempty"`
	RespHeaders map[string]string `json:"respHeaders,omitempty"`
	ReqBody     string            `json:"reqBody,omitempty"`
	RespBody    string            `json:"respBody,omitempty"`
	BodyCut     bool              `json:"bodyCut,omitempty"` // 内容超过上限被截断过
}

// recordBuffer 定长环形缓冲，并发安全。
type recordBuffer struct {
	mu    sync.Mutex
	items []RequestRecord
	next  int
	seq   int64
}

func newRecordBuffer() *recordBuffer {
	return &recordBuffer{items: make([]RequestRecord, maxRecords)}
}

// Add 记一条。缓冲满了就覆盖最旧的那条。
func (b *recordBuffer) Add(r RequestRecord) {
	b.mu.Lock()
	b.seq++
	r.Seq = b.seq
	b.items[b.next] = r
	b.next = (b.next + 1) % len(b.items)
	b.mu.Unlock()
}

// Since 取 seq 大于 after 的所有记录，按 seq 升序。
// 前端每次带上已拿到的最大 seq，就能只取增量。
func (b *recordBuffer) Since(after int64) []RequestRecord {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]RequestRecord, 0, 32)
	for _, r := range b.items {
		if r.Seq > after {
			out = append(out, r)
		}
	}
	// 环形缓冲的物理顺序和写入顺序不一致，得排一下
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// cutString 截断到 limit 字节，并说明是否截断过。
// 往回退到 UTF-8 字符边界，免得把中文切成半个字。
func cutString(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// trimPartialRune 砍掉末尾被截断的半个 UTF-8 字符。
//
// 判据是「从末尾能不能解出一个完整字符」，不能看末字节是不是字符起始字节 ——
// 一个完整的中文，末字节同样是续字节，那样会把好字也砍掉。
func trimPartialRune(s string) string {
	for i := 0; i < 4 && len(s) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}

// limitedWriter 把写进来的内容截留前 limit 字节，其余照单全收地丢掉。
// 这样 io.Copy 还是能把整个响应体读完（字节数才准），但只留一小段。
type limitedWriter struct {
	buf   []byte
	limit int
	cut   bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.limit - len(w.buf); room > 0 {
		if len(p) > room {
			w.buf = append(w.buf, p[:room]...)
			w.cut = true
		} else {
			w.buf = append(w.buf, p...)
		}
	} else if len(p) > 0 {
		w.cut = true
	}
	return len(p), nil
}
