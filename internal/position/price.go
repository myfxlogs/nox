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

// CMCSource 供监视器注入的 CMC 数据查询接口（D-208 v2）。
// nil = 本监视器不支持 CMC 行，走老路径（ComputePositionPrice）。
type CMCSource interface {
	// LastCMCMid 返回 instrumentID 的 mid + fetched_at（新鲜度判定用）。
	LastCMCMid(instrumentID string) (mid float64, fetchedAt time.Time, err error)
	// LastCMCCloseout 返回 closeout 强平价 + distance_to_closeout_pct（CMC 百分数，调用方换算为小数）。
	LastCMCCloseout() (closeout, distPct float64, err error)
	// LastCMCUnrealised 返回 instrumentID 的 unrealised_pnl（broker 权威值）。
	LastCMCUnrealised(instrumentID string) (float64, error)
	// LastCMCPosition 返回 instrumentID 的持仓详情（entry_price/quantity/opened_at/order_id/leverage 推导）。
	LastCMCPosition(instrumentID string) (pgstore.CMCPosition, error)
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

// ComputeCMCPositionPrice 用 CMC 真实数据组装视图（D-208 v2）。
// 价格/强平/距强平/浮盈全来自 broker 实测，无公式。
// 陈旧守卫：mid 帧龄 > freshTTL → 诚实空态（Price=0, Level=""，复用 G3 语义）。
// Level：price 对照 WarnHigh/CritHigh（既有列，语义不变——空单的 warn/crit 是价格阈值）。
// DistToLiqPct 口径：CMC 的 distance_to_closeout_pct 是百分数（如 30.57），
// 视图函数换算为小数（/100）——与老口径 (liq−price)/price 同量级。
func ComputeCMCPositionPrice(p *pgstore.PersonalPosition, src CMCSource, freshTTL time.Duration, now time.Time) PositionPrice {
	v := PositionPrice{ID: p.ID}

	if src == nil || p.CMCInstrumentID == nil || *p.CMCInstrumentID == "" {
		return v // 无 CMC 源或非 CMC 行 → 空视图（调用方应走老路径）
	}

	// mid + 新鲜度
	mid, fetchedAt, err := src.LastCMCMid(*p.CMCInstrumentID)
	if err != nil || mid == 0 {
		return v // 无报价 → 诚实空态
	}
	if now.Sub(fetchedAt) > freshTTL {
		return v // 陈旧 → 诚实空态
	}

	v.Price = mid
	v.Level = PersonalLevel(mid, p.WarnHigh, p.CritHigh)

	// 强平价：CMC summary 的 closeout_price（真实值，含真实现金/保证金构成）
	closeout, distPct, err := src.LastCMCCloseout()
	if err == nil && closeout > 0 {
		v.LiqPrice = closeout
		// CMC distance_to_closeout_pct 是百分数（如 30.57），换算为小数
		if distPct != 0 {
			v.DistToLiqPct = distPct / 100.0
		} else if mid > 0 {
			// 库字段为 0 时自算（与老口径一致：(liq−price)/price）
			v.DistToLiqPct = (closeout - mid) / mid
		}
	}

	// 浮盈：CMC broker 权威 unrealised_pnl
	pnl, err := src.LastCMCUnrealised(*p.CMCInstrumentID)
	if err == nil {
		v.PnlUsd = pnl
	}

	return v
}
