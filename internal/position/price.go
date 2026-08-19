package position

import (
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// PositionPrice 单条个人持仓的现价派生展示视图（D-043 后端算；前端仅按 id 合并渲染，D-049）。
type PositionPrice struct {
	ID           int64
	Price        float64 // 0 = 无可用报价（诚实空态）
	Level        string  // 'ok'|'warn'|'crit'；'' = 无报价（非 PersonalLevel(0) 的 'ok'，语义保真）
	PnlUsd       float64 // 仅空单腿 + entry/qty 非空 + 有价；否则 0
	LiqPrice     float64 // crit_high(非0) 优先，否则理论值；算不出 0
	DistToLiqPct float64 // (liq−price)/price；liq/price 任一不可用 0
}

// ComputePositionPrice 计算单条个人持仓的现价派生视图（P3：快照路径与实时推送共用同一计算）。
// 语义逐行搬运自 personalPositionToProto 的价格派生块（D-049），条件/公式不得改变。
func ComputePositionPrice(p *pgstore.PersonalPosition, venues map[string]bus.Quote, freshTTL time.Duration, now time.Time, liqMMR float64) PositionPrice {
	v := PositionPrice{ID: p.ID}

	// 现价：最新未陈旧帧 mid（无报价/全陈旧 → price 0，实账按无值处理）。
	price := 0.0
	hasPrice := false
	if q, ok := bus.LatestQuote(venues, freshTTL, now); ok {
		price = q.Mid()
		hasPrice = true
		v.Price = price
		v.Level = PersonalLevel(price, p.WarnHigh, p.CritHigh)
	}

	// 浮动盈亏：仅空单腿且 entry 非空，且现价可用（无价不算，防 (entry−0)×qty 假盈亏）。
	if hasPrice && p.Kind == "short_hedge" && p.EntryPrice != nil && p.Qty != nil {
		v.PnlUsd = (*p.EntryPrice - price) * *p.Qty
	}
	// 强平价：crit_high（手动权威值，非 0）> 理论 = entry × (1 + 1/leverage − MMR)。
	liq := 0.0
	if p.CritHigh != nil && *p.CritHigh != 0 {
		liq = *p.CritHigh
	} else if p.EntryPrice != nil && p.Leverage != nil && *p.Leverage > 0 {
		liq = *p.EntryPrice * (1 + 1/float64(*p.Leverage) - liqMMR)
	}
	v.LiqPrice = liq
	// 距强平 % = (liq − 现价) / 现价；负数 = 已破线。liq 或现价不可用 → 0。
	if liq != 0 && hasPrice && price != 0 {
		v.DistToLiqPct = (liq - price) / price
	}
	return v
}
