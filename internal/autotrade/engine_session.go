// engine_session.go —— D-029 ②：MT 腿休市判定（周表主信号 + 报价新鲜度佐证）。
// 口径（裁定钉死）：
//  1. trade_mode ∈ {Disabled(0), CloseOnly(1)} → 闭（静态交易权限保留拦截面）；
//  2. trade_session 周表命中该腿 → 按 UTC 周表判开闭（adapter 落库已换算服务器→UTC）；
//  3. 周表说开市 → 动态径：MT 腿报价新鲜度 > MtQuoteStaleMin 分钟 → 闭；**N 缺（≤0）=
//     fail-closed 判闭**（无动态佐证不判开——宁可不累计/不 new，不下带病单）。
//     部署须配 ARB_MT_QUOTE_STALE_MIN 启用（报审钉明）。
//  4. 周表未命中（采集缺席）→ false（fail-open 既有链兜底；下单回报 10018 兜底）。
//
// 闭市语义消费=「carry_death breach 不累计」（xassetExitCheck）+「econEntryGate 拒开」。
package autotrade

import (
	"context"
	"time"

	"arb/internal/fact"
	"arb/internal/keydomain"
	"arb/internal/marketstate"
	"arb/internal/store/pgstore"
)

// sessionNow 判定时刻（UTC；hook 可注入——测试控时相，生产 nil=真钟）。
func (e *Engine) sessionNow() time.Time {
	if e.sessionNowFn != nil {
		return e.sessionNowFn()
	}
	return time.Now().UTC()
}

// SetSessionNowFn D-029 ②：注入判定时刻 hook（测试/未来装配用；生产缺省=真钟 UTC）。
func (e *Engine) SetSessionNowFn(fn func() time.Time) { e.sessionNowFn = fn }

// sessionTable/sessionWindow D-170：判定逻辑搬 internal/marketstate（裁决器共享同一份
// 装载，防双轨漂移）；类型别名零扩散。
type sessionTable = marketstate.Table
type sessionWindow = marketstate.Window

// loadSessionTable 全量拉 trade_session facts 组表（行数=符号×7×session，百级内）。
func (e *Engine) loadSessionTable(ctx context.Context) sessionTable {
	// D-029 R1 ④：Engine 内缓存 1 分钟 TTL（周表静态每日刷新，判定调用高频——防全量
	// facts 重拉/重组反复发生）；ttl 过期即重拉（采集班次 24h 内至多重拉 1440 次=无害）。
	e.sessionTblMu.Lock()
	if e.sessionTbl != nil && time.Since(e.sessionTblAt) < time.Minute {
		t := e.sessionTbl
		e.sessionTblMu.Unlock()
		return t
	}
	e.sessionTblMu.Unlock()
	t := marketstate.LoadSessionTable(ctx, e.store)
	if len(t) == 0 {
		return nil
	}
	e.sessionTblMu.Lock()
	e.sessionTbl, e.sessionTblAt = t, time.Now()
	e.sessionTblMu.Unlock()
	return t
}

// mtMarketClosedFor 单 MT 腿休市判定（venue=bare、canonical=腿 canonical——R1 ②：动态径
// ticks 按 canonical 键查（与 MT ticks 落库键一致），入场径/持仓径同口径）。
func (e *Engine) mtMarketClosedFor(ctx context.Context, bare, canonical string) bool {
	// 径 1：trade_mode 静态权限（CloseOnly/Disabled 拦截；Full/缺=过）。
	tm, terr := e.store.LatestFacts(ctx, fact.KindTradeMode, bare, canonical)
	if terr == nil && len(tm) > 0 {
		switch int64(tm[0].Value) {
		case fact.TradeModeDisabled, fact.TradeModeCloseOnly: // 禁交易 / 只可平
			return true
		}
	}
	// 径 2：周表主信号。
	table := e.loadSessionTable(ctx)
	win, hasTable := table[bare+"|"+canonical]
	now := e.sessionNow()
	if hasTable {
		open := false
		for _, w := range win {
			if w.InSession(now) {
				open = true
				break
			}
		}
		if !open {
			return true // 周表判闭
		}
		// 周表说开 → 径 3：动态新鲜度佐证（N 缺=fail-closed 判闭；ticks 按 canonical 键 R1 ②）。
		n := e.cfg.MtQuoteStaleMin
		if n <= 0 {
			return true
		}
		ticks, terr := e.store.TicksWindow(ctx, bare, canonical, now.Add(-time.Duration(n)*time.Minute))
		if terr != nil || len(ticks) == 0 {
			return true // 窗内零 tick=报价静默超阈
		}
		return false
	}
	// 径 4：周表缺席（采集缺席）→ fail-open 既有链兜底（下单回报 10018 兜底）。
	return false
}

// mtLegMarketClosed D-027 改口径（D-029 ②）：持仓任一 OPEN+FILLED MT 腿休市 → true。
// 消费=xassetExitCheck（闭市期间 carry_death breach 不累计）+ econEntryGate（拒开）。
func (e *Engine) mtLegMarketClosed(ctx context.Context, p *pgstore.Position) bool {
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return false
	}
	buyCanonical := canonicalFromOppID(p.OpportunityID)
	sellCanonical := e.xassetSellCanonical(ctx, p, legs)
	for i := range legs {
		l := legs[i]
		if l.Phase != "OPEN" || l.Status != "FILLED" || !keydomain.IsMTKey(l.Venue) {
			continue
		}
		canonical := buyCanonical
		if l.Side != "BUY" {
			canonical = sellCanonical
		}
		if canonical == "" {
			continue
		}
		if e.mtMarketClosedFor(ctx, keydomain.BareOf(l.Venue), canonical) {
			return true
		}
	}
	return false
}
