// Package bus 提供 TickSampler——报价采样落盘（§4.3）。
// 订阅 QuoteBus，按 canonical×venue 间隔 ≥60s 采样写 ticks 表。
package bus

import (
	"context"
	"log"
	"sync"
	"time"
)

// TickSampler 订阅 QuoteBus 并按节流间隔采样写入 ticks 表。
type TickSampler struct {
	bus      *QuoteBus
	interval time.Duration
	write    func(ctx context.Context, rows []TickRow) error
	ensure   func(ctx context.Context, year, month int) error
}

// TickRow 是 ticks 表的一行。
type TickRow struct {
	Ts        time.Time
	Venue     string
	Symbol    string
	Bid       float64
	Ask       float64
	VenueType string
}

// NewTickSampler 创建采样器。interval 为每 canonical×venue 最小写入间隔。
func NewTickSampler(
	b *QuoteBus,
	interval time.Duration,
	write func(ctx context.Context, rows []TickRow) error,
	ensure func(ctx context.Context, year, month int) error,
) *TickSampler {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &TickSampler{bus: b, interval: interval, write: write, ensure: ensure}
}

// Run 订阅全部 symbol 并采样。symbols 为要采样的 canonical symbol 列表。
// 阻塞至 ctx 取消。
func (ts *TickSampler) Run(ctx context.Context, symbols []string) {
	var mu sync.Mutex
	lastWrite := make(map[string]time.Time) // venueKey → last write time

	for _, sym := range symbols {
		ch, cancel := ts.bus.Subscribe(sym)
		go func(sym string, ch <-chan Quote) {
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case q, ok := <-ch:
					if !ok {
						return
					}
					ts.maybeWrite(ctx, &mu, lastWrite, q)
				}
			}
		}(sym, ch)
	}
	<-ctx.Done()
}

func (ts *TickSampler) maybeWrite(ctx context.Context, mu *sync.Mutex, lastWrite map[string]time.Time, q Quote) {
	vk := q.VenueKey()
	key := q.Symbol + "|" + vk

	mu.Lock()
	if last, ok := lastWrite[key]; ok && q.Time.Sub(last) < ts.interval {
		mu.Unlock()
		return
	}
	lastWrite[key] = q.Time
	mu.Unlock()

	year, month := q.Time.Year(), int(q.Time.Month())
	if ts.ensure != nil {
		if err := ts.ensure(ctx, year, month); err != nil {
			log.Printf("[ticksampler] ensure partition %04d_%02d: %v", year, month, err)
		}
	}

	row := TickRow{
		Ts:        q.Time,
		Venue:     q.Venue,
		Symbol:    q.Symbol,
		Bid:       q.Bid,
		Ask:       q.Ask,
		VenueType: q.VenueType,
	}
	if err := ts.write(ctx, []TickRow{row}); err != nil {
		log.Printf("[ticksampler] write %s %s: %v", q.Symbol, vk, err)
	}
}
