// Package bus 提供 TickSampler——报价采样落盘（§4.3）。
// 订阅 QuoteBus，按 canonical×venue 间隔 ≥60s 采样写 ticks 表。
package bus

import (
	"context"
	"log"
	"slices"
	"sync"
	"time"

	"nox/internal/platform/safego"
)

// ticksVenueTypes ticks 表 venue_type 白名单——DB CHECK（ticks_venue_type_chk，
// migration 1000_baseline）的代码侧镜像：非通道型帧（cmc:cfd 等）写径前置滤除，
// 不再撞 CHECK 拒写每 interval 刷错日志。新通道型入库须先扩 CHECK 再入本表。
var ticksVenueTypes = []string{"mt4", "mt5", "perp", "spot"}

// writeErrLogCooldown 同键同文案写失败日志冷却窗（恒真错每 interval 刷屏降噪）。
const writeErrLogCooldown = 10 * time.Minute

// TickSampler 订阅 QuoteBus 并按节流间隔采样写入 ticks 表。
type TickSampler struct {
	bus      *QuoteBus
	interval time.Duration
	write    func(ctx context.Context, rows []TickRow) error
	ensure   func(ctx context.Context, year, month int) error

	logMu     sync.Mutex
	dropSeen  map[string]bool     // 白名单滤除：venueKey 首见已记（恒真拒只记一条）
	errStamps map[string]errStamp // 写失败降频：采样键 → 上次记日志态
}

type errStamp struct {
	msg string
	at  time.Time
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
		safego.GoLoop("tick_sampler:sample", func() {
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
		})
	}
	<-ctx.Done()
}

func (ts *TickSampler) maybeWrite(ctx context.Context, mu *sync.Mutex, lastWrite map[string]time.Time, q Quote) {
	vk := q.VenueKey()
	// 白名单滤（票 10-20）：非通道型帧写径前置滤除——ticks 域保持纯（下游零
	// cmc:cfd 消费面已普查定谳）；qbus 现态面不截（分层后现态读=内存）。
	if !slices.Contains(ticksVenueTypes, q.VenueType) {
		ts.logFilteredDrop(q.Symbol, vk)
		return
	}
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
		ts.logWriteErr(key, err.Error())
	}
}

// logFilteredDrop 白名单外帧只记首见一条（每 interval 恒真拒，刷屏零增量信息）。
func (ts *TickSampler) logFilteredDrop(symbol, vk string) {
	ts.logMu.Lock()
	defer ts.logMu.Unlock()
	if ts.dropSeen == nil {
		ts.dropSeen = map[string]bool{}
	}
	if ts.dropSeen[vk] {
		return
	}
	ts.dropSeen[vk] = true
	log.Printf("[ticksampler] drop %s %s: venue_type 白名单外（ticks_venue_type_chk 拒写域），帧不落库", symbol, vk)
}

// logWriteErr 写失败日志降频（票 10-20②）：同键同文案冷却窗内只记首条；
// 文案变化或过窗再记（恒真错不再每 interval 刷屏）。
func (ts *TickSampler) logWriteErr(key, msg string) {
	ts.logMu.Lock()
	defer ts.logMu.Unlock()
	now := time.Now()
	if s, ok := ts.errStamps[key]; ok && s.msg == msg && now.Sub(s.at) < writeErrLogCooldown {
		return
	}
	if ts.errStamps == nil {
		ts.errStamps = map[string]errStamp{}
	}
	ts.errStamps[key] = errStamp{msg: msg, at: now}
	log.Printf("[ticksampler] write %s: %s", key, msg)
}
