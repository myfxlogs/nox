// intra_day.go —— D-425 S2：日内单品高风险轨 v1 引擎（独立沙箱轨，与 carry 面代码隔离）。
// 5min 步进=仓位管理（kill/止损/EOD）→风险栅栏链（§1 全项硬编码顺序：kill→熔断→并发→
// 时段）→纸面成交（ticks 中价±半点差+taker 费）。真金路径零代码（v1→v2 三条件门见 SSOT §5）。
package scan

import (
	"context"
	"fmt"
	"math"
	"time"

	"arb/internal/calib"
	"arb/internal/store/pgstore"
)

// IntraConfig 风险口径（§1 钉死；env 六件只喂字段，栅栏顺序硬编码不可配）。
type IntraConfig struct {
	BudgetUSD        float64 // 总预算 cap（默认 10000）
	MaxNotionalUSD   float64 // 单订单名义 cap（默认 2000）
	Concurrency      int     // 并发（默认 1）
	DailyLossPct     float64 // 单日风险预算=预算的 %（默认 30）
	SessionGate      bool    // 时段闸开关（默认 on）
	Kill             bool    // kill-switch（一键全停）
	StopFracOfMargin float64 // 止损带=保证金预算分数（0.8=留 2% 滑点余量满足回放门）
}

// DefaultIntraConfig SSOT §1 默认值（env 覆盖见 wire_intra）。
func DefaultIntraConfig() IntraConfig {
	return IntraConfig{
		BudgetUSD:        10000,
		MaxNotionalUSD:   2000,
		Concurrency:      1,
		DailyLossPct:     30,
		SessionGate:      true,
		StopFracOfMargin: IntraStopBudgetFrac,
	}
}

// IntraStore 日内轨存储依赖（*pgstore.Store 原生实现；测试 fake 替身）。
type IntraStore interface {
	InsertIntraTrade(ctx context.Context, r *pgstore.IntraTrade) (int64, error)
	CloseIntraTrade(ctx context.Context, id int64, exitPx float64, reason string, slippageBps, pnl float64) error
	ListOpenIntraTrades(ctx context.Context) ([]pgstore.IntraTrade, error)
	IntraDayRealizedPnl(ctx context.Context, day time.Time) (float64, error)
	TicksWindow(ctx context.Context, venue, symbol string, since time.Time) ([]pgstore.TickRow, error)
	InsertIntraEvent(ctx context.Context, kind, baseline, detail string) error // D-441 S3
	HasIntraEventToday(ctx context.Context, kind string) (bool, error)         // D-441 S3 当日去重
	// ListIntraVenues venue 动态派生（业主裁定 2026-10-01）：mt=connected 账户 venue，
	// cex=exchange_configs 主网行 venue；查询失败调用方本轮不开新仓（纸面 fail-closed）。
	ListIntraVenues(ctx context.Context) (mt []string, cex []string, err error)
}

// IntraEngine 日内轨引擎（无状态步进；仓态全在 DB=重启安全）。
type IntraEngine struct {
	st      IntraStore
	cfg     IntraConfig
	now     func() time.Time
	alertFn func(ctx context.Context, level, msg string) // D-441 S3：critical 告警钩子（nil=不告警）
}

func NewIntraEngine(st IntraStore, cfg IntraConfig) *IntraEngine {
	return &IntraEngine{st: st, cfg: cfg, now: time.Now}
}

// SetNow 注入时钟（测试）。
func (e *IntraEngine) SetNow(f func() time.Time) { e.now = f }

// IntraStepReport 单步观测（禁静默：每类动作计数留痕）。
type IntraStepReport struct {
	KillClosed  int
	StopClosed  int
	CircuitHit  bool
	SignalXExit int
	EODClosed   int
	Opened      int
	Notes       []string
}

// Step 一次 5min 步进。顺序钉死：kill→仓位管理（止损/信号/EOD）→熔断计账→开仓栅栏链。
func (e *IntraEngine) Step(ctx context.Context) (IntraStepReport, error) {
	rep := IntraStepReport{}
	now := e.now()
	open, err := e.st.ListOpenIntraTrades(ctx)
	if err != nil {
		return rep, fmt.Errorf("intra list open: %w", err)
	}

	// ① kill-switch（最先，硬编码）：持仓立即平、禁新开。
	if e.cfg.Kill {
		for _, t := range open {
			if err := e.closeTrade(ctx, t, now, "kill", &rep); err != nil {
				return rep, err
			}
			rep.KillClosed++
		}
		rep.Notes = append(rep.Notes, "kill: all flat, entries blocked")
		e.emitEvent(ctx, "kill", "熔断：已全部平仓，今日禁开新仓") // D-441 S3
		return rep, nil
	}

	// ② 仓位管理：止损（浮亏达保证金 80% 预算带）→均值回归信号出→EOD。
	for _, t := range open {
		ticks, err := e.st.TicksWindow(ctx, t.Venue, t.Symbol, now.Add(-10*time.Minute))
		if err != nil || len(ticks) == 0 {
			rep.Notes = append(rep.Notes, fmt.Sprintf("trade#%d no fresh tick（下轮重试）", t.ID))
			continue
		}
		last := ticks[len(ticks)-1]
		mid := midOf(last.Bid, last.Ask)
		loss := (mid - t.EntryPx) / t.EntryPx
		if t.Side == "short" {
			loss = -loss
		}
		// R1 口径终裁：单位链=保证金 10% →10x 杠杆→1% 价格；止损带=预算 80%=0.8% 价格+0.2% 滑点余量。
		stopDist := IntraStopBudgetFrac * 0.10 / IntraLeverage // 价格带=0.8%
		if loss <= -stopDist {
			if err := e.closeTrade(ctx, t, now, "stop", &rep); err != nil {
				return rep, err
			}
			rep.StopClosed++
			continue
		}
		if t.Baseline == "meanrev" {
			if fired := e.signalExit(ctx, t, now, &rep); fired {
				continue
			}
		}
		if now.UTC().Hour() == 23 && now.UTC().Minute() >= 50 {
			if err := e.closeTrade(ctx, t, now, "eod", &rep); err != nil {
				return rep, err
			}
			rep.EODClosed++
		}
	}

	// ③ 日内熔断：当日已实现亏损 ≤ −预算×DailyLossPct% → 当日停新开（在开仓由止损/EOD 管理）。
	realized, err := e.st.IntraDayRealizedPnl(ctx, now.UTC())
	if err != nil {
		return rep, fmt.Errorf("intra day pnl: %w", err)
	}
	dayBudget := -e.cfg.BudgetUSD * e.cfg.DailyLossPct / 100
	if realized <= dayBudget {
		rep.CircuitHit = true
		rep.Notes = append(rep.Notes, fmt.Sprintf("daily circuit: realized=%.2f ≤ %.2f（当日停新开）", realized, dayBudget))
		e.emitEvent(ctx, "circuit", fmt.Sprintf("当日已实现亏损 %.2f 跌破日预算 %.2f", realized, dayBudget)) // D-441 S3
		return rep, nil
	}

	// ④ 开仓栅栏链（硬编码顺序：并发→时段→白名单信号；并发=1）。
	if len(open) >= e.cfg.Concurrency {
		return rep, nil
	}
	// venue 动态派生（每 Step 实时读配置表；账户/所增删即随动）。查询失败 → 本轮
	// 不开新仓（纸面 fail-closed），Notes 留痕。
	mtVenues, cexVenues, err := e.st.ListIntraVenues(ctx)
	if err != nil {
		rep.Notes = append(rep.Notes, fmt.Sprintf("intra venues: %v（本轮不开新仓）", err))
		return rep, nil
	}
	// 时段闸在品种对内逐对判定（crypto 7×24 例外仅 BTC/ETH；env 可关=全时段）
	for _, pair := range buildIntraPairs(mtVenues, cexVenues) {
		if len(open) >= e.cfg.Concurrency {
			break
		}
		if e.cfg.SessionGate && !SessionAllows(pair.Crypto, now) {
			continue
		}
		nt, err := e.tryOpen(ctx, pair, now, &rep, open)
		if err != nil {
			return rep, err
		}
		if nt != nil {
			// D-425 R2 计数回写：新仓即时入快照——单步累计受 Concurrency 硬限（修前步初快照不回写=生产 open=3 实证）。
			open = append(open, *nt)
			rep.Opened++
		}
	}
	return rep, nil
}

// tryOpen 单对信号判定+开仓（栅栏全过才成交；纸面成交=ticks 中价±半点差+taker 费）。
// 返回新开仓行（供调用方计数回写并发快照；无信号=nil）。rep 指针传递：Notes 随步上浮。
func (e *IntraEngine) tryOpen(ctx context.Context, pair IntraPair, now time.Time, rep *IntraStepReport, open []pgstore.IntraTrade) (*pgstore.IntraTrade, error) {
	for _, o := range open { // 同对已开仓不重复（含本步计数回写的新仓）
		if o.Venue == pair.Venue && o.Symbol == pair.Symbol {
			return nil, nil
		}
	}
	ticks, err := e.st.TicksWindow(ctx, pair.Venue, pair.Symbol, now.Add(-90*time.Minute))
	if err != nil {
		return nil, nil // 数据缺席静默跳过（同族惯例），WP1 由调用方日志兜底
	}
	for _, baseline := range []string{"momentum_break", "meanrev"} {
		events := simulate(baseline, ticks, e.cfg, now)
		// 取末事件：仅「进」事件且未被后续出场抵消（simulate 末态在仓=信号仍有效）
		var lastEnter *simEvent
		inPos := false
		for i := range events {
			if events[i].Enter {
				lastEnter = &events[i]
				inPos = true
			} else {
				inPos = false
			}
		}
		if lastEnter == nil || !inPos {
			continue
		}
		notional := e.cfg.MaxNotionalUSD
		entryPx := lastEnter.Px
		qty := notional / entryPx
		margin := notional / IntraLeverage
		stopDist := math.Max(lastEnter.StopDist, 0.003)
		var stopPx float64
		if lastEnter.Side == "long" {
			stopPx = entryPx * (1 - stopDist)
		} else {
			stopPx = entryPx * (1 + stopDist)
		}
		row := &pgstore.IntraTrade{
			Baseline: baseline, Symbol: pair.Symbol, Venue: pair.Venue, Side: lastEnter.Side,
			EntryTs: now, EntryPx: entryPx, Qty: qty, NotionalUSD: notional,
			MarginUSD: margin, StopPx: stopPx, SignalNote: lastEnter.Note,
		}
		if _, err := e.st.InsertIntraTrade(ctx, row); err != nil {
			return nil, fmt.Errorf("intra open: %w", err)
		}
		rep.Notes = append(rep.Notes, fmt.Sprintf("open %s %s %s @%.4f stop=%.4f (%s)", baseline, pair.Venue, pair.Symbol, entryPx, stopPx, lastEnter.Note))
		return row, nil
	}
	return nil, nil
}

// signalExit 均值回归 z 回归零轴出（模拟器同口径）。
func (e *IntraEngine) ReplayGate(ctx context.Context, venue, symbol, baseline string, since time.Time) (p100 float64, ok bool, n int, err error) {
	ticks, err := e.st.TicksWindow(ctx, venue, symbol, since)
	if err != nil {
		return 0, false, 0, fmt.Errorf("replay ticks: %w", err)
	}
	events := simulate(baseline, ticks, e.cfg, since)
	feeFrac := 2 * calib.Lookup("intra_taker_fee_bps", "", "", IntraTakerFeeBps) / 10000 // 双侧 taker 费（D-533 calib 覆盖）；loss/margin=×IntraLeverage
	worst := 0.0
	for i := 0; i+1 < len(events); i++ {
		en, ex := events[i], events[i+1]
		if !en.Enter {
			continue
		}
		var pnlPxFrac float64
		if en.Side == "long" {
			pnlPxFrac = (ex.Px - en.Px) / en.Px
		} else {
			pnlPxFrac = (en.Px - ex.Px) / en.Px
		}
		lossOverMargin := (-pnlPxFrac + feeFrac) * IntraLeverage
		if lossOverMargin > worst {
			worst = lossOverMargin
		}
		n++
		i++ // 配对消费（enter 后紧随其 exit）
	}
	return worst, n > 0 && worst <= 0.10, n, nil
}
