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
	"strconv"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/keydomain"
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

// sessionTable MT 腿周会话表（UTC 坐标，adapter 落库已换算）：venue|symbol → 会话列表。
type sessionTable map[string][]sessionWindow

type sessionWindow struct {
	wday     int // UTC 周几（起点日）
	startMin int // UTC 分钟
	endMin   int // UTC 分钟（跨午夜 end<start，判定 wrap）
}

// inSession now 是否落在该会话内（跨午夜：起点日尾段 ∨ 次日头段）。
// D-083：start=end=0 是全天窗——MT5 end=1440 经 toUTCSession 的
// endAbs.Hour()*60+Minute() 折成 0（247m 类符号每交易日一行 00:00-24:00）。
// 不能走 end>=start 普通分支（会把全天窗判成仅 00:00 那一分钟开市）。
func (w sessionWindow) inSession(now time.Time) bool {
	nm := now.UTC().Hour()*60 + now.UTC().Minute()
	nd := int(now.UTC().Weekday())
	if w.startMin == 0 && w.endMin == 0 {
		return nd == w.wday // 全天窗：当天任意分钟都在市
	}
	if w.endMin >= w.startMin {
		return nd == w.wday && nm >= w.startMin && nm <= w.endMin
	}
	if nd == w.wday && nm >= w.startMin {
		return true
	}
	return nd == (w.wday+1)%7 && nm <= w.endMin
}

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
	facts, err := e.store.LatestFactsByKind(ctx, fact.KindTradeSession)
	if err != nil || len(facts) == 0 {
		return nil
	}
	// D-083：两阶段装表——先收 `sym#n` 计数行（adapter 每符号 emitted 窗数），
	// 再收窗行并丢弃 idx≥count 的残留幽灵窗（同 canonical 多 brokerSym 的历史并写
	// 不会被覆盖删除，只有计数行能界清有效窗数）。无 #n 行=旧数据 → 不过滤（兼容）。
	type widx struct {
		idx int // 窗序号（-1=无 # 后缀/不可解析，不参与截断）
		w   sessionWindow
	}
	counts := map[string]int{}
	raw := map[string][]widx{}
	for _, f := range facts {
		sym := f.Symbol
		idx := -1
		if i := strings.Index(sym, "#"); i >= 0 {
			suffix := sym[i+1:]
			sym = sym[:i] // D-029：adapter 唯一化后缀（canon#idx）剥离
			if suffix == "n" {
				counts[f.Venue+"|"+sym] = int(f.Value)
				continue // 计数行不入窗
			}
			if n, aerr := strconv.Atoi(suffix); aerr == nil {
				idx = n
			}
		}
		v := int64(f.Value)
		key := f.Venue + "|" + sym
		raw[key] = append(raw[key], widx{idx: idx, w: sessionWindow{
			wday: int(v / 1e8), startMin: int(v / 1e4 % 1e4), endMin: int(v % 1e4),
		}})
	}
	t := sessionTable{}
	for k, ws := range raw {
		cnt, declared := counts[k]
		for _, e := range ws {
			if declared && e.idx >= cnt {
				continue // 幽灵窗（残留旧符号行的越界序号）
			}
			t[k] = append(t[k], e.w)
		}
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
			if w.inSession(now) {
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
