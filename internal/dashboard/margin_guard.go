// margin_guard.go —— D-405 S3：账户资金三档监控评估器（保证金安全网）。
// 事件驱动搭车 snapshot 写口（R0-S3：pgstore.InsertVenueAccountSnapshots snapHook，
// 三写口 scheduler/mt_event/okx_event 收敛单钩）——评估频率≤数据新鲜度，零新轮询 ticker。
// 判定结果 upsert fin_venue_margin_status（S6 RPC 数据源 + last_alert_at 冷却重启安全）。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/combo"
	"arb/internal/safego"
	"arb/internal/store/pgstore"
	"arb/internal/usdnotional"
)

// marginGuardStore guard 取数面（*pgstore.Store 原生实现，测试可 fake）。
type marginGuardStore interface {
	ListOpenLegsForMargin(ctx context.Context) ([]pgstore.MarginOpenLeg, error)
	TickVolatility24h(ctx context.Context, venue, venueType, symbol string) (float64, bool, error)
	GetVenueMarginStatus(ctx context.Context, venue, venueType string) (*pgstore.VenueMarginStatus, bool, error)
	UpsertVenueMarginStatus(ctx context.Context, r *pgstore.VenueMarginStatus) error
	InsertAlert(ctx context.Context, posID *int64, level, msg string) error
}

// MarginGuard 账户资金三档监控。
type MarginGuard struct {
	store marginGuardStore
	qbus  *bus.QuoteBus
	csOf  func(venueKey, venueSymbol string) (float64, error) // = Service.legContractSizeOf

	urgentCooldown time.Duration // ARB_MARGIN_URGENT_COOLDOWN（默认 30min，R0-S1）
	warnCooldown   time.Duration // ARB_MARGIN_WARN_COOLDOWN（默认 6h）

	mu           sync.Mutex
	urgentStreak map[string]int // urgent 去抖：同 venue 连续 urgent tick 数（SSOT：连续 2 tick 才发）
}

// NewMarginGuard 创建评估器（cooldown<=0 回落默认）。
func NewMarginGuard(store marginGuardStore, qbus *bus.QuoteBus, csOf func(string, string) (float64, error), urgentCD, warnCD time.Duration) *MarginGuard {
	if urgentCD <= 0 {
		urgentCD = 30 * time.Minute
	}
	if warnCD <= 0 {
		warnCD = 6 * time.Hour
	}
	return &MarginGuard{
		store: store, qbus: qbus, csOf: csOf,
		urgentCooldown: urgentCD, warnCooldown: warnCD,
		urgentStreak: make(map[string]int),
	}
}

// OnSnapshots snapHook 入口：批次落库后按 (venue,venue_type) 去重逐 venue 异步评估
// （写口不阻塞——safego 协程内跑 DB 评估）。
func (g *MarginGuard) OnSnapshots(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) {
	seen := make(map[string]bool, len(snaps))
	for _, sn := range snaps {
		key := sn.Venue + "|" + sn.VenueType
		if seen[key] {
			continue
		}
		seen[key] = true
		row := sn
		safego.Go("margin_guard:"+key, func() {
			if err := g.evaluate(context.Background(), &row); err != nil {
				log.Printf("[margin-guard] %s/%s: %v", row.Venue, row.VenueType, err)
			}
		})
	}
}

// evaluate 单 venue 评估：腿波动求和 → 纯函数三档判定 → 告警（冷却/去抖）→ upsert。
func (g *MarginGuard) evaluate(ctx context.Context, sn *pgstore.VenueAccountSnapshot) error {
	legs, err := g.store.ListOpenLegsForMargin(ctx)
	if err != nil {
		return fmt.Errorf("list open legs: %w", err)
	}
	in := MarginEvalIn{VenueType: sn.VenueType}
	// equity<=0 = 全零采集洞/空仓边界（SSOT §3 一轮：1279967 全零行须查明）——
	// 按未采处理落 data_stale，不把零值当真值触发假 urgent。
	if sn.EquityRaw > 0 {
		in.Equity = &sn.EquityRaw
	}
	in.FreeMargin = &sn.FreeMarginRaw
	in.Buffer = sn.BufferMultiple

	dailyVol := 0.0
	dataMissing := false
	for _, l := range legs {
		lv, lt := legSnapshotVenue(l.Venue)
		if lv != sn.Venue || lt != sn.VenueType {
			continue
		}
		in.HasLegs = true
		notional, ok := marginLegNotionalUSD(g.qbus, g.csOf, l)
		if !ok {
			dataMissing = true
			continue
		}
		vol, vok, verr := g.store.TickVolatility24h(ctx, lv, lt, l.Symbol)
		if verr != nil {
			return fmt.Errorf("tick vol %s/%s: %w", l.Venue, l.Symbol, verr)
		}
		if !vok {
			dataMissing = true // 无 ticks/覆盖<2h → data_stale（SSOT §1）
			continue
		}
		dailyVol += notional * vol
	}
	in.DailyVolUSD = dailyVol

	out := evalMarginTier(in)
	// 任一腿波动/名义缺 → data_stale（SSOT §1 R0-S2：fail-open 禁保留陈旧绿；
	// 无腿 venue 不受腿数据缺影响）。
	if dataMissing && in.HasLegs {
		out.Tier = "data_stale"
	}

	prev, _, perr := g.store.GetVenueMarginStatus(ctx, sn.Venue, sn.VenueType)
	if perr != nil {
		return fmt.Errorf("get margin status: %w", perr)
	}
	now := time.Now()
	key := sn.Venue + ":" + sn.VenueType
	g.mu.Lock()
	if out.Tier == "urgent" {
		g.urgentStreak[key]++
	} else {
		g.urgentStreak[key] = 0
	}
	streak := g.urgentStreak[key]
	g.mu.Unlock()

	var prevTier string
	var lastAlert *time.Time
	if prev != nil {
		prevTier = prev.Tier
		lastAlert = prev.LastAlertAt
		// 换档=新告警周期：last_alert_at 属于「当前档连续段」的冷却基——
		// 档一变旧时间作废（否则 warn 的告警时间会误压 urgent 首发窗口）。
		if out.Tier != prevTier {
			lastAlert = nil
		}
	}
	if shouldMarginAlert(out.Tier, prevTier, lastAlert, streak, now, g.urgentCooldown, g.warnCooldown) {
		level, msg := marginAlertText(sn, &out)
		if err := g.store.InsertAlert(ctx, nil, level, msg); err != nil {
			log.Printf("[margin-guard] alert insert %s/%s: %v", sn.Venue, sn.VenueType, err)
		} else {
			t := now
			lastAlert = &t
		}
	}
	if out.Tier == "data_stale" {
		log.Printf("[margin-guard] WARN %s/%s data_stale（ticks/报价/字段缺，不发资金告警）", sn.Venue, sn.VenueType)
	}

	row := &pgstore.VenueMarginStatus{
		Venue: sn.Venue, VenueType: sn.VenueType, Tier: out.Tier,
		DailyVolUSD: &dailyVol, LastAlertAt: lastAlert,
	}
	if out.CoverageDays >= 0 {
		row.CoverageDays = &out.CoverageDays
	}
	if out.SuggestedTopupUSD > 0 {
		row.SuggestedTopupUSD = &out.SuggestedTopupUSD
	}
	return g.store.UpsertVenueMarginStatus(ctx, row)
}

// legSnapshotVenue 腿 venueKey → snapshot (venue,venue_type) 键：
// "3001157:mt5"→(3001157,mt5)；"okx:perp"→(okx,unified)（写侧归一同 cexscan.normalizeVenueType）。
func legSnapshotVenue(venueKey string) (string, string) {
	i := strings.LastIndex(venueKey, ":")
	if i < 0 {
		return venueKey, ""
	}
	v, t := venueKey[:i], venueKey[i+1:]
	if v == "okx" && (t == "spot" || t == "perp") {
		t = "unified"
	}
	return v, t
}

// marginLegNotionalUSD 单腿名义 USD：qty(EffectiveQty)×usdPerLot（canonical 计价币换算
// 复用 usdnotional.PerLot）。报价缺→回退 leg.Price（成交价锚）；CS/fx 缺→ok=false
// （fail-closed 不计入；marginGuard/开户门槛两调用方共享口径）。
func marginLegNotionalUSD(qbus *bus.QuoteBus, csOf func(string, string) (float64, error), l pgstore.MarginOpenLeg) (float64, bool) {
	canonical := canonicalOfOppID(l.OppID)
	if canonical == "" || l.Qty <= 0 {
		return 0, false
	}
	price := l.Price
	if q, ok := qbus.Latest(canonical, l.Venue); ok && q.Mid() > 0 {
		price = q.Mid()
	}
	if price <= 0 {
		return 0, false
	}
	cs, err := csOf(l.Venue, l.Symbol)
	if err != nil || cs <= 0 {
		return 0, false
	}
	perLot, ok := usdnotional.PerLot(canonical, price, cs, qbus)
	if !ok || perLot <= 0 {
		return 0, false
	}
	return l.Qty * perLot, true
}

// canonicalOfOppID 从 opportunity_id 提取 canonical（"TYPE:CANONICAL:..." 第二段；
// XASSET 复合 canonical "XPTUSDT-XPT" 段内带 "-" 不含 ":"，安全）。
func canonicalOfOppID(oppID string) string {
	ck := combo.FromOppID(oppID)
	parts := strings.Split(ck, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// marginAlertText 告警文案（urgent=critical 补资金 / warn=info 余粮天数）。
func marginAlertText(sn *pgstore.VenueAccountSnapshot, out *MarginEvalOut) (level, msg string) {
	if out.Tier == "urgent" {
		return "critical", fmt.Sprintf(
			"[保证金紧急] %s/%s 可用保证金 $%.2f 低于 1.5×日波动缓冲（档=urgent，覆盖 %.1f 日）——请业主补资金",
			sn.Venue, sn.VenueType, out.AvailMarginUSD, out.CoverageDays)
	}
	return "info", fmt.Sprintf(
		"[保证金预警] %s/%s 余粮约 %.1f 日（可用 $%.2f < 3×日波动缓冲；建议补 $%.2f）",
		sn.Venue, sn.VenueType, out.CoverageDays, out.AvailMarginUSD, out.SuggestedTopupUSD)
}
