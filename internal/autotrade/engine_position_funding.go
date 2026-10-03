// D-182 S3 拆分：从 engine_position.go 提取 recalcNetFunding（funding 退出判定）。
// D-312 S1：重写为逐腿 :perp 口径——旧 longFunding−shortFunding 两处病：
// ①符号反向（funding 正值=多头付空方，与 detector/perp_funding.go 同源），返回值=−净收入，
// 消费方 netFunding<=FundingExitPct 判平 → 盈利仓误平、亏损仓永不触发；
// ②venue-pair 模型对套保结构不成立——同所双腿同 bare 命中同一条 fact net≡0 首查误平，
// 跨所 spot 腿错配该所 perp fact 或缺 fact 永不退出。
package autotrade

import (
	"arb/internal/keydomain"
	"context"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// recalcNetFunding 逐腿求和持仓 funding 净收入（年化百分点，正=净收钱）。
// funding 只挂 :perp 腿：SELL（空）收 → +rate、BUY（多）付 → −rate；非 :perp 腿
// （:spot/:mt4/:mt5 等）贡献 0 不查 fact（现货/MT 腿不收付 funding）。
// fact 复用 fundingRateForVenue（D-311 已有 helper，勿再写 LatestFacts 匹配循环）；
// 任一 :perp 腿缺 fact / 陈旧 >SwapFactMaxAge（>0 时）/ 无 :perp 腿 → (0,false) fail-closed。
// 双腿同 canonical（canonicalFromOppID，FUNDING_HEDGE/PERP_FUNDING 沿用现状）。
// window>0 → 腿 funding 取窗内滚动均值（D-027 carry_death 3d 口径）；≤0 → 瞬时最新值
// （FUNDING_HEDGE 旧语义/展示）。均值窗内零样本 → fail-closed 同缺 fact。
func (e *Engine) recalcNetFunding(ctx context.Context, p *pgstore.Position, window time.Duration) (float64, bool) {
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return 0, false
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, false
	}
	maxAge := e.cfg.SwapFactMaxAge
	var net float64
	hasPerp := false
	for i := range legs {
		leg := legs[i]
		if leg.Phase != "OPEN" || !keydomain.IsType(leg.Venue, "perp") {
			continue
		}
		bare := keydomain.BareOf(leg.Venue)
		rate, ts, ok := e.fundingRateMeanForVenue(ctx, bare, canonical, window)
		if !ok {
			log.Printf("[autotrade] exit pos=%d: funding fact missing venue=%s canonical=%s (fail-closed)", p.ID, bare, canonical)
			return 0, false
		}
		if maxAge > 0 && time.Since(ts) > maxAge {
			log.Printf("[autotrade] exit pos=%d: funding fact stale venue=%s canonical=%s >%v (fail-closed)", p.ID, bare, canonical, maxAge)
			return 0, false
		}
		// 持有净收入：BUY（多头）付 funding=−rate；SELL（空头）收 funding=+rate。
		if leg.Side == "BUY" {
			net -= rate
		} else {
			net += rate
		}
		hasPerp = true
	}
	if !hasPerp {
		log.Printf("[autotrade] exit pos=%d: no :perp leg (fail-closed)", p.ID)
		return 0, false
	}
	return net, true
}
