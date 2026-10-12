// wire_cex_paper.go —— D-555 S1：双CEX 纸面轨装配（engine FUNDING_HEDGE 候选 →
// scan.StepPaperCexCarry 保底/发现槽）。detector.Scan 纯函数枚举直传（P3 单源禁重写枚举）；
// funding 年化=FactCache Value 百分点口径（与 universe FundingAnn 同源同单位）。
// D-051 R1：① hook 只做候选入队+开仓闸，退出/计息归独立 30s 轮 pass（miss_streak
// 按轮计——票定二选一写明：30s 轮保原 3 轮≈90s 缺席语义；CFD 4h 对齐轮会拖成 12h+）；
// ② unfiltered 全量快照经 cexPaperSnap 入轮（开/退/计息同源=同一 ScanUnfiltered 流，
// 不读 fin_cex_universe 的 Allow 后行，ct_val/雷达语义零污染）；③ 聚合日志每轮一行。
package main

import (
	"context"
	"log"
	"sync"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/config"
	"nox/internal/research/detector"
	"nox/internal/research/scan"
	"nox/internal/store/pgstore"
	"nox/internal/trading/engine"
)

// cexPaperSkips D-051③：CEX 纸面入口拦截计数器（包级单实例——wireCexPaperStep
// 注入 opts，startSelfwatch 同实例读快照供日报分布；装配序 main.go→monitors）。
var cexPaperSkips = &scan.PaperSkipCounter{}

// cexPaperRound R1①③：hook 产出聚合 + unfiltered 全量快照（mu 保护——hook/快照在
// engine 报价 goroutine，轮 pass 在独立 goroutine）。hook 零日志，轮 pass 单行。
type cexPaperRound struct {
	mu     sync.Mutex
	latest []detector.Candidate
	opened []string
	skips  map[string]int
}

func (r *cexPaperRound) setLatest(cands []detector.Candidate) {
	r.mu.Lock()
	r.latest = cands
	r.mu.Unlock()
}

func (r *cexPaperRound) note(opened []string, skipN map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, opened...)
	if r.skips == nil {
		r.skips = map[string]int{}
	}
	for k, n := range skipN {
		r.skips[k] += n
	}
}

// step 一轮 pass：drain 聚合+最新快照 → 计息/退出 → 单行「opened=N exited=M skips={…}」。
func (r *cexPaperRound) step(ctx context.Context, st scan.PaperStore, now time.Time, pub *refreshCoalescer) {
	r.mu.Lock()
	latest, opened, skipN := r.latest, r.opened, r.skips
	r.opened, r.skips = nil, nil
	r.mu.Unlock()
	exited, accrueSkip, err := scan.StepPaperCexCarryAccrueExit(ctx, st, cexPaperCands(latest), now)
	if err != nil {
		log.Printf("[scan] paper cex carry round: %v", err)
		return
	}
	log.Printf("[scan] paper cex carry: opened=%d exited=%d skips=%v accrue_skip=%d", len(opened), exited, skipN, accrueSkip)
	if (len(opened) > 0 || exited > 0) && pub != nil {
		pub.Publish("paper_carry") // D-508 S9：纸面开平→前端刷新扇出（2s 合流）
	}
}

// cexPaperCands detector.Candidate→scan.CexPaperCand（跨 canonical 候选剔——
// v1 同 canonical 钉；VenueBuy=spot 腿 VenueSell=perp 腿，FundingAnn=净年化百分点）。
func cexPaperCands(cs []detector.Candidate) []scan.CexPaperCand {
	fc := make([]scan.CexPaperCand, 0, len(cs))
	for _, c := range cs {
		if c.CanonicalSell != "" && c.CanonicalSell != c.Canonical {
			continue // 构型守卫：跨 canonical 候选不进本轨（v1 同 canonical 钉）
		}
		fc = append(fc, scan.CexPaperCand{
			Canonical:    c.Canonical,
			SpotVenueKey: c.VenueBuy,
			PerpVenueKey: c.VenueSell,
			FundingAnn:   c.GrossBps, // detector：净年化（百分点）作 gross——与 universe FundingAnn 同单位
			SpotAsk:      c.PriceBuy, // D-555 S2：双腿报价直取（basis 计算）
			PerpBid:      c.PriceSell,
		})
	}
	return fc
}

// wireCexPaperStep 装配双CEX 纸面轨：engine hook（候选入队+开仓闸）+ 快照 sink +
// 独立 30s 轮（计息/退出）。opts（env 与 cfd 保底槽同源同值；scan 层纯函数不读 env——
// D-407 先例装配层直读）。refreshTopic=纸面开平→Publish（D-508 S9 同款）。
// D-051②：质量闸阈每轮读 env（passthrough 键：app_config 行 boot 注入 env，改值须重启生效）。
func wireCexPaperStep(ctx context.Context, st *pgstore.Store, eng *engine.Engine, refreshTopic *bus.Topic[string], cfg *config.Config) {
	refreshPub := newRefreshCoalescer(refreshTopic)           // 热修：2s/key 合流，帧率级开平不扇出 RPC 风暴
	round := &cexPaperRound{}                                 // R1：聚合态+快照
	opts := scan.PaperCexOpts{ReopenCooldown: 24 * time.Hour} // D-050：数量槽退役无上限 env
	if cfg.PaperAuthorizedReopenCooldownH >= 0 {
		opts.ReopenCooldown = time.Duration(cfg.PaperAuthorizedReopenCooldownH * float64(time.Hour))
	}
	opts.Skips = cexPaperSkips
	// D-145：闸阈改读 cfg 字段（原 hot env 读退役；语义=boot 快照，重启生效不变）。
	eng.SetCexPaperSnap(round.setLatest) // R1②：unfiltered 全量快照（每扫一覆）
	eng.SetCexPaperHook(func(c context.Context, cands []detector.Candidate) {
		opts.MinDepthUSD = cfg.PaperCexMinDepthUSD
		opts.MinFundingPct = cfg.PaperCexMinFundingPct
		// D-042 R1 ②：双腿报价连续性闸——bus 最新报价变更时间（LastChangeAt 价格变更钟）。
		quoteAlive := func(venueKey, symbol string) (time.Time, bool) {
			if eng.QuotesBus == nil {
				return time.Time{}, false
			}
			t, ok := eng.QuotesBus.LastChangeAt(symbol, venueKey)
			return t, ok
		}
		opened, skipN, err := scan.StepPaperCexCarryOpen(c, st, cexPaperCands(cands), time.Now(), opts, nil, quoteAlive)
		if err != nil {
			log.Printf("[scan] paper cex carry open: %v", err)
			return
		}
		round.note(opened, skipN) // R1③：聚合归轮日志，hook 零日志
	})
	// R1①：独立 30s 轮驱动计息/退出 pass——miss_streak 按轮计，与 hook 节拍解耦。
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case tm := <-t.C:
				round.step(ctx, st, tm, refreshPub)
			}
		}
	}()
}
