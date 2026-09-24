package main

// 绑定给前端的应用层。前端能直接调用的方法都挂在这个结构上，
// 压测进度通过 Wails 事件往外推，不靠前端轮询。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 界面刷新节奏。500ms 足够让曲线看起来连续，又不会把事件通道刷爆。
const tickInterval = 500 * time.Millisecond

const (
	eventTick = "bench:tick" // 运行中的实时快照
	eventDone = "bench:done" // 一轮结束后的最终快照
)

// App 是绑定给前端的对象。
type App struct {
	// Wails 在独立的 goroutine 里跑 startup，绑定方法在另一个线程上读它，
	// 两者之间没有先后保证。用 atomic 存，免得读到一个写了一半的接口值。
	ctx atomic.Pointer[context.Context]

	mu     sync.Mutex
	engine *Engine
	cancel context.CancelFunc
	last   Snapshot

	// 最近一轮的请求明细。引擎跑完就销毁了，这个缓冲留着，
	// 这样压测结束后还能回看刚才那些请求。
	records *recordBuffer
}

func NewApp() *App {
	return &App{
		// 从没跑过压测时 GetSnapshot 返回的是这个零值。map 和 slice 不预置的话
		// 序列化出去是 null，和前端声明的 Record / 数组对不上。
		last: Snapshot{
			StatusCodes: map[string]int64{},
			ErrorMap:    map[string]int64{},
			LatencyBins: []LatencyBin{},
		},
	}
}

func (a *App) startup(ctx context.Context) { a.ctx.Store(&ctx) }

// DefaultConfig 给前端一份默认参数，免得默认值在前后端各写一份、改一处漏一处。
// 目标地址预填一个稳定的站点，打开就能直接试跑。
func (a *App) DefaultConfig() Config {
	return Config{
		URL:         "https://httpbin.org/",
		Method:      "GET",
		Concurrency: 3,
		Duration:    3,
		Timeout:     3000,
		HTTP2:       true,
		// 别漏了它：另外几个出口都会走 normalize 补上空 map，只有这里不走，
		// 漏掉的话前端拿到的 headers 是 null
		Headers: map[string]string{},
	}
}

// StartBench 起一轮压测后立刻返回。进度走 bench:tick 事件，
// 结束走 bench:done，前端不需要轮询。
func (a *App) StartBench(cfg Config) (Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.engine != nil {
		return a.last, errors.New("已经有一轮压测在进行，请先停止")
	}
	if err := cfg.normalize(); err != nil {
		return a.last, err
	}

	base := a.ctx.Load()
	if base == nil {
		return a.last, errors.New("界面还没准备好，稍等一下再试")
	}

	ctx, cancel := context.WithCancel(*base)
	engine := NewEngine(cfg)
	a.engine, a.cancel = engine, cancel
	a.records = engine.records

	go func() {
		final := engine.Run(ctx, func(s Snapshot) {
			a.mu.Lock()
			a.last = s
			a.mu.Unlock()
			runtime.EventsEmit(*base, eventTick, s)
		})

		a.mu.Lock()
		a.last = final
		a.engine = nil
		a.cancel = nil
		a.mu.Unlock()

		cancel() // 释放 context，避免派生的 ctx 一直挂着
		runtime.EventsEmit(*base, eventDone, final)
	}()

	return a.last, nil
}

// StopBench 让当前这轮提前收工。worker 会在各自把手上那个请求发完后退出。
func (a *App) StopBench() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cancel != nil {
		a.cancel()
	}
	return a.last
}

// GetSnapshot 取当前状态，供前端刚挂载时对齐一次。
func (a *App) GetSnapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// RecentRequests 取 seq 大于 after 的请求明细，前端带上已拿到的最大 seq 拿增量。
// 缓冲是环形覆盖的，压测跑太久的话太早的记录会被顶掉，前端按 seq 判断就行。
func (a *App) RecentRequests(after int64) []RequestRecord {
	a.mu.Lock()
	buf := a.records
	a.mu.Unlock()

	if buf == nil {
		return []RequestRecord{}
	}
	return buf.Since(after)
}
