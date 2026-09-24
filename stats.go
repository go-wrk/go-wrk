package main

// 延迟直方图与统计快照。
//
// 压测每秒可能产生几十万个样本，逐个保存不现实，所以用分桶计数：
// 延迟按 2 的幂分段，每段再切 32 个子桶，相对误差约 3%。
// 好处是内存固定（不到 1000 个 uint64）、可以并发写入、能实时算百分位，
// 而且桶本身就是前端画延迟分布图要的数据。

import (
	"math"
	"math/bits"
	"sync/atomic"
	"time"
)

const (
	histSubBits = 5                // 每个 2 的幂区间切 32 个子桶
	histSubCnt  = 1 << histSubBits // 32
	histMinExp  = 10               // 最小桶 2^10 ns ≈ 1μs
	// 量程上界约 2200 秒。必须盖住 engine.go 的 maxTimeoutMs（10 分钟 = 600 秒），
	// 盖不住的话更慢的样本会全挤进最后一个桶，百分位就不可分辨了。
	// TestHistogramCoversMaxTimeout 拦着这条约束。
	histMaxExp  = 40
	histBuckets = (histMaxExp - histMinExp + 1) * histSubCnt
)

// bucketOf 返回延迟（纳秒）落在哪个桶。落在量程外的归到两端。
func bucketOf(nanos int64) int {
	if nanos <= 0 {
		return 0
	}
	u := uint64(nanos)
	exp := 63 - bits.LeadingZeros64(u)
	if exp < histMinExp {
		return 0
	}
	if exp > histMaxExp {
		return histBuckets - 1
	}
	mantissa := int(u>>uint(exp-histSubBits)) & (histSubCnt - 1)
	return (exp-histMinExp)*histSubCnt + mantissa
}

// bucketLower 返回第 idx 个桶的下界（纳秒）。
func bucketLower(idx int) int64 {
	exp := idx/histSubCnt + histMinExp
	mantissa := idx % histSubCnt
	return int64(1)<<uint(exp) + int64(mantissa)<<uint(exp-histSubBits)
}

// bucketWidth 第 idx 个桶的宽度（纳秒）。桶按 2 的幂分段，越往右越宽。
func bucketWidth(idx int) int64 {
	exp := idx/histSubCnt + histMinExp
	return int64(1) << uint(exp-histSubBits)
}

// bucketMid 返回桶的中点，作为该桶样本的代表值。
func bucketMid(idx int) int64 {
	return bucketLower(idx) + bucketWidth(idx)/2
}

// Histogram 并发安全的延迟直方图。零值即可用。
type Histogram struct {
	counts [histBuckets]atomic.Int64
	total  atomic.Int64 // 样本总数
	sum    atomic.Int64 // 延迟总和（纳秒），用来算平均值
	min    atomic.Int64 // 最小值，0 表示还没有样本
	max    atomic.Int64
}

// Record 记录一次延迟。
func (h *Histogram) Record(d time.Duration) {
	ns := d.Nanoseconds()
	h.counts[bucketOf(ns)].Add(1)
	h.total.Add(1)
	h.sum.Add(ns)

	for {
		cur := h.min.Load()
		if cur != 0 && cur <= ns {
			break
		}
		if h.min.CompareAndSwap(cur, ns) {
			break
		}
	}
	for {
		cur := h.max.Load()
		if cur >= ns {
			break
		}
		if h.max.CompareAndSwap(cur, ns) {
			break
		}
	}
}

// Total 已记录的样本数。
func (h *Histogram) Total() int64 { return h.total.Load() }

// Mean 平均延迟。
func (h *Histogram) Mean() time.Duration {
	n := h.total.Load()
	if n == 0 {
		return 0
	}
	return time.Duration(h.sum.Load() / n)
}

// Min 最小延迟。
func (h *Histogram) Min() time.Duration { return time.Duration(h.min.Load()) }

// Max 最大延迟。
func (h *Histogram) Max() time.Duration { return time.Duration(h.max.Load()) }

// StdDev 延迟的标准差，用来衡量抖动 —— 平均 20ms 可能是每次都 20ms，
// 也可能是一半 1ms 一半 39ms，标准差能把这两种分开。
//
// 用分桶的中点当样本代表值反算，而不是在 Record 里累加平方和：一来热路径
// 一点开销都不用加，二来平方和会溢出 —— 137 秒的延迟平方就是 1.9e22，
// 远超 int64 上限。代价是精度受分桶影响，和百分位一个量级。
func (h *Histogram) StdDev() time.Duration {
	n := h.total.Load()
	if n < 2 {
		return 0
	}
	mean := float64(h.sum.Load()) / float64(n)

	var sq float64
	for i := range h.counts {
		c := h.counts[i].Load()
		if c == 0 {
			continue
		}
		d := float64(bucketMid(i)) - mean
		sq += float64(c) * d * d
	}
	return time.Duration(math.Sqrt(sq / float64(n)))
}

// Percentile 返回 p 分位延迟（p 取 0~1，例如 0.99）。
// 从最小桶开始累加计数，第一个让累计数越过目标的桶就是答案。
func (h *Histogram) Percentile(p float64) time.Duration {
	n := h.total.Load()
	if n == 0 {
		return 0
	}
	target := int64(float64(n) * p)
	if target < 1 {
		target = 1
	}
	var cum int64
	for i := range h.counts {
		cum += h.counts[i].Load()
		if cum >= target {
			return time.Duration(bucketMid(i))
		}
	}
	return time.Duration(h.max.Load())
}

// Bins 导出延迟分布，供界面画图。
//
// 中间的空桶也要带上。前端是按索引等距画的 —— 桶本身是对数分桶，等距画出来
// 横轴就是对数的。只给非空桶的话，10ms 和 1s 那两个桶会挨在一起画，
// 看上去只差一点点，实际差一百倍。
func (h *Histogram) Bins() []LatencyBin {
	first, last := -1, -1
	for i := range h.counts {
		if h.counts[i].Load() > 0 {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		// 给空切片而不是 nil：序列化出去是 []，和前端声明的数组类型对得上
		return []LatencyBin{}
	}

	out := make([]LatencyBin, 0, last-first+1)
	for i := first; i <= last; i++ {
		lower := bucketLower(i)
		out = append(out, LatencyBin{
			LowerMs: float64(lower) / 1e6,
			UpperMs: float64(lower+bucketWidth(i)) / 1e6,
			Count:   h.counts[i].Load(),
		})
	}
	return out
}

// LatencyBin 是延迟分布图上的一个柱子。
type LatencyBin struct {
	LowerMs float64 `json:"lowerMs"`
	UpperMs float64 `json:"upperMs"`
	Count   int64   `json:"count"`
}

// Snapshot 是某一时刻的压测状态。主循环按固定间隔交出一份，界面直接渲染。
type Snapshot struct {
	Running     bool             `json:"running"`
	Elapsed     float64          `json:"elapsed"`  // 已运行秒数
	Requests    int64            `json:"requests"` // 拿到响应的请求数（含 4xx/5xx）
	Errors      int64            `json:"errors"`   // 传输层错误数（连不上、超时、读中断）
	Bytes       int64            `json:"bytes"`    // 响应总字节数（含响应头估算）
	QPS         float64          `json:"qps"`      // 最近一个采样窗口的瞬时 QPS
	AvgQPS      float64          `json:"avgQps"`   // 全程平均 QPS
	MinMs       float64          `json:"minMs"`
	MaxMs       float64          `json:"maxMs"`
	AvgMs       float64          `json:"avgMs"`
	StdDevMs    float64          `json:"stdDevMs"` // 延迟标准差，衡量抖动
	P50Ms       float64          `json:"p50Ms"`
	P75Ms       float64          `json:"p75Ms"`
	P90Ms       float64          `json:"p90Ms"`
	P99Ms       float64          `json:"p99Ms"`
	StatusCodes map[string]int64 `json:"statusCodes"`
	ErrorMap    map[string]int64 `json:"errorMap"`
	LatencyBins []LatencyBin     `json:"latencyBins"`
}

func msOf(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}
