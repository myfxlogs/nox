// D-182 S3 拆分：从 engine_position.go 提取 recalcNetFunding（funding 退出判定）。
package autotrade

import (
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// recalcNetFunding 重算净 funding 年化（D-072 §4.2，facts 驱动 + 48h 新鲜度守卫）。
// D-105 T1：同 recalcNetSwap fail-closed 修复——venue 匹配加 found 标志，任一 venue 未匹配
// → 缺数据不决策平仓；新鲜度守卫改为对匹配到的 fact 检查（旧值查列表首元素同修）。
func (e *Engine) recalcNetFunding(ctx context.Context, p *pgstore.Position) (float64, bool) {
	canonical := canonicalFromOppID(p.OpportunityID)
	if canonical == "" {
		return 0, false
	}
	fundingFacts, err := e.store.LatestFacts(ctx, fact.KindFunding, "", canonical)
	if err != nil || len(fundingFacts) == 0 {
		return 0, false
	}
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return 0, false
	}
	var buyVenue, sellVenue string
	for i := range legs {
		if legs[i].Phase == "OPEN" {
			if legs[i].Side == "BUY" {
				buyVenue = legs[i].Venue
			} else {
				sellVenue = legs[i].Venue
			}
		}
	}
	if buyVenue == "" || sellVenue == "" {
		return 0, false
	}
	buyBare := strings.Split(buyVenue, ":")[0]
	sellBare := strings.Split(sellVenue, ":")[0]
	// D-105 T1：venue 匹配加 found 标志，缺数据不决策平仓。
	var longFunding, shortFunding float64
	var longFact, shortFact *fact.Fact
	for i := range fundingFacts {
		if fundingFacts[i].Venue == buyBare {
			longFunding = fundingFacts[i].Value
			longFact = &fundingFacts[i]
		}
		if fundingFacts[i].Venue == sellBare {
			shortFunding = fundingFacts[i].Value
			shortFact = &fundingFacts[i]
		}
	}
	if longFact == nil {
		log.Printf("[autotrade] exit pos=%d: funding fact missing venue=%s", p.ID, buyBare)
		return 0, false
	}
	if shortFact == nil {
		log.Printf("[autotrade] exit pos=%d: funding fact missing venue=%s", p.ID, sellBare)
		return 0, false
	}
	// D-159 W4a R7：TTL 统一用 cfg.SwapFactMaxAge（env 单源，默认 2h），与 discoverSwapCombos/recalcNetSwap 同源。
	maxAge := e.cfg.SwapFactMaxAge
	if maxAge > 0 && (time.Since(longFact.Ts) > maxAge || time.Since(shortFact.Ts) > maxAge) {
		log.Printf("[autotrade] skip exit pos=%d: funding facts stale >%v", p.ID, maxAge)
		return 0, false
	}
	return longFunding - shortFunding, true
}
