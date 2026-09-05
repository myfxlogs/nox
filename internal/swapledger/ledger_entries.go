package swapledger

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// D-159 W3a R5：loadFacts 返回 map[factKey]fact.Fact（保留完整 Fact，含 Ts 供 TTL 检查；
// 原实现只存 float64 丢 Ts → 过期 Fact 无法识别）。
func (l *Ledger) loadFacts(ctx context.Context) (map[factKey]fact.Fact, map[factKey]fact.Fact, map[factKey]fact.Fact, error) {
	longFacts, err := l.store.LatestFacts(ctx, fact.KindSwapLong, "", "")
	if err != nil {
		return nil, nil, nil, err
	}
	shortFacts, err := l.store.LatestFacts(ctx, fact.KindSwapShort, "", "")
	if err != nil {
		return nil, nil, nil, err
	}
	fundingFacts, err := l.store.LatestFacts(ctx, fact.KindFunding, "", "")
	if err != nil {
		return nil, nil, nil, err
	}
	longMap := make(map[factKey]fact.Fact, len(longFacts))
	for _, f := range longFacts {
		longMap[factKey{f.Symbol, f.Venue}] = f
	}
	shortMap := make(map[factKey]fact.Fact, len(shortFacts))
	for _, f := range shortFacts {
		shortMap[factKey{f.Symbol, f.Venue}] = f
	}
	fundMap := make(map[factKey]fact.Fact, len(fundingFacts))
	for _, f := range fundingFacts {
		fundMap[factKey{f.Symbol, f.Venue}] = f
	}
	return longMap, shortMap, fundMap, nil
}

// accountPosition 对单个持仓逐腿记 swap/funding cashflow。
// D-159 W3a R5：per-leg 幂等——每腿独立检查（position+leg+kind+day），一腿失败下一轮只补失败腿，
// 已有 cashflow 的腿跳过但不再短路整笔 position（旧 position 级 HasCashflowForDay 会让失败腿永久漏记）。
func (l *Ledger) accountPosition(ctx context.Context, pos pgstore.Position, day time.Time,
	longMap, shortMap, fundMap map[factKey]fact.Fact) (int, error) {
	legs, err := l.store.ListPositionLegs(ctx, pos.ID)
	if err != nil {
		return 0, fmt.Errorf("list legs: %w", err)
	}
	if len(legs) == 0 {
		return 0, nil
	}

	canonical := canonicalFromOppID(pos.OpportunityID)
	if canonical == "" {
		return 0, fmt.Errorf("canonical from oppID %q empty", pos.OpportunityID)
	}

	isFunding := isFundingType(pos.Type)
	kind := cashflowKind(pos.Type)
	written := 0
	for _, leg := range legs {
		// 只记 OPEN phase 的腿（CLOSE phase 是平仓腿，无 swap 记账）
		if leg.Phase != "OPEN" {
			continue
		}
		// D-159 W3a R5：per-leg 幂等检查（该腿当日已记→跳过该腿，不短路整笔）。
		has, err := l.store.HasCashflowForLegDay(ctx, pos.ID, leg.LegNo, kind, day)
		if err != nil {
			return written, fmt.Errorf("check existing cashflow leg %d: %w", leg.LegNo, err)
		}
		if has {
			continue
		}
		amount, ok, reason := l.legSwapAmount(canonical, leg, day, isFunding, longMap, shortMap, fundMap)
		if !ok {
			log.Printf("[swapledger] pos=%d leg=%d day=%s skip: %s", pos.ID, leg.LegNo, day.Format("2006-01-02"), reason)
			continue // swap rate / notional 不可用 → 跳过该腿（fail-closed，不猜）
		}
		if amount == 0 {
			// D-193-A 扩包返修 R5：reason 为空才回退 zero_swap，non_settle_venue 用专属 reason
			skipReason := reason
			if skipReason == "" {
				skipReason = "zero_swap"
			}
			log.Printf("[swapledger] pos=%d leg=%d day=%s skip: %s", pos.ID, leg.LegNo, day.Format("2006-01-02"), skipReason)
			continue // 零息/non-settle venue → 不写 cashflow（避免无意义行）
		}
		// D-193-A 扩包（决策 1-7）：迁 InsertCashflowFull，填事实身份字段。
		// event_key=<source>:<posID>:<legNo>:<kind>:<occurred_at YYYY-MM-DD>（决策 1）
		// quality='estimated'（公式记账，决策 3）；source='swapledger'（决策 4）
		// occurred_at=day 零点（业务时间，决策 5）；received_ts=now（写入时间）
		// account_id=venue 前缀数字（决策 6）；external_event_id/raw_*=NULL（公式路径不造假，决策 2/7）
		now := time.Now().UTC()
		dayStr := day.Format("2006-01-02")
		eventKey := fmt.Sprintf("swapledger:%d:%d:%s:%s", pos.ID, leg.LegNo, kind, dayStr)
		quality := "estimated"
		src := "swapledger"
		legNoVal := leg.LegNo
		cf := &pgstore.Cashflow{
			PositionID: pos.ID,
			LegNo:      &legNoVal,
			Kind:       kind,
			Venue:      leg.Venue,
			AmountUSD:  amount,
			Ts:         day,
			EventKey:   &eventKey,
			Source:     &src,
			Quality:    &quality,
			OccurredAt: &day,
			ReceivedTs: &now,
			AccountID:  parseVenueAccountID(leg.Venue),
		}
		won, err := l.store.InsertCashflowFull(ctx, cf)
		if err != nil {
			return written, fmt.Errorf("insert cashflow full leg %d: %w", leg.LegNo, err)
		}
		if won {
			written++
		}
	}
	return written, nil
}

// parseVenueAccountID D-193-A 扩包（决策 6）：从 venue key 前缀解析 account_id。
// venue 格式如 "52993526:mt5" → 52993526；解析失败→nil（不猜）。
func parseVenueAccountID(venue string) *int64 {
	parts := strings.SplitN(venue, ":", 2)
	if len(parts) == 0 {
		return nil
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil
	}
	return &id
}

// legSwapAmount 计算单腿当日 swap/funding USD 金额（纯函数，可单测）。
// D-159 W3a R5：接收 map[factKey]fact.Fact（含 Ts 供 TTL 检查）。
// D-193-A 扩包返修 M2：返回 (amount, ok, reason)；ok=false 时 reason 标识失败分支（定罪 notional 链观察前提）。
// ok=true 时 reason=""；amount 正=收 swap，负=付 swap。
func (l *Ledger) legSwapAmount(canonical string, leg pgstore.PositionLeg, day time.Time,
	isFunding bool, longMap, shortMap, fundMap map[factKey]fact.Fact) (float64, bool, string) {
	venueName := venueFromKey(leg.Venue)
	fk := factKey{canonical, venueName}

	// 1. 取 swap/funding rate
	var f fact.Fact
	var rateFound bool
	var kindName string
	if isFunding {
		f, rateFound = fundMap[fk]
		kindName = fact.KindFunding
	} else {
		// BUY(long) 腿用 swap_long，SELL(short) 腿用 swap_short
		if leg.Side == "BUY" {
			f, rateFound = longMap[fk]
			kindName = fact.KindSwapLong
		} else {
			f, rateFound = shortMap[fk]
			kindName = fact.KindSwapShort
		}
	}
	if !rateFound {
		return 0, false, fmt.Sprintf("fact_not_found kind=%s key=%s:%s", kindName, canonical, venueName)
	}
	// D-159 W3a R5 + W4a R7：Fact 过期防护——Fact.Ts 超 l.factTTL → fail-closed 不写估算为实际。
	// TTL 由 New 注入（cfg.SwapFactMaxAge env 单源，与 discoverSwapCombos/recalcNetSwap 同源）。
	// D-193-A 扩包返修 M2：TTL 过期并入 reason（删独立 log，避免重复打两条）。
	if l.factTTL > 0 && time.Since(f.Ts) > l.factTTL {
		return 0, false, fmt.Sprintf("fact_expired kind=%s venue=%s ts=%s ttl=%v", kindName, f.Venue, f.Ts.Format(time.RFC3339), l.factTTL)
	}
	rate := f.Value

	// 2. 取 notional（usdPerLot × qty）
	side := "ask"
	if leg.Side == "SELL" {
		side = "bid"
	}
	usdPerLot, ok := l.notional.USDPerLotForLeg(canonical, leg.Venue, side)
	if !ok || usdPerLot <= 0 {
		return 0, false, fmt.Sprintf("notional_usdperlot venue=%s side=%s", leg.Venue, side)
	}
	notional := usdPerLot * leg.Qty
	if notional <= 0 {
		return 0, false, "notional_nonpos"
	}

	// 3. 算金额（D-077 修复⑩ G5：decimalutil 精度守卫，经 strconv 字符串中介避免 float64 漂移）
	var amount float64
	if isFunding {
		// D-077 修复⑪A M2：funding 记账删三倍虚乘。
		// 采集层 Fact 已是年化口径 → 日金额 = rate/100/DaysPerYear × notional（含 8h×3 周期合计）。
		// 旧公式 × PeriodsPerDay(3) = 3 倍虚乘（现记 9f 实收 3f）→ 删除。
		amount = decimalAmount(rate, 100, DaysPerYear, notional, 1)
	} else {
		// swap：日 bps → 日金额 = rate/10000 × notional × settleMultiplier
		multiplier := settleMultiplier(leg.Venue, canonical, day, l.tripleDay)
		if multiplier == 0 {
			// D-077 修复⑪A M4：fail-closed（三倍日未知）→ 跳过该腿记账，不记 0 cashflow
			return 0, false, "settle_multiplier_zero"
		}
		amount = decimalAmount(rate, 10000, 1, notional, multiplier)
	}
	// D-112：已知不结算 venue → 记 0（不跳过，与零息 venue 同处理——amount=0 → accountPosition 跳过不写 cashflow）
	// D-193-A 扩包返修 R5：nonSettle 专属 reason（与真零息区分，accountPosition 打 non_settle_venue 而非 zero_swap）
	if l.nonSettleVenues[leg.Venue] {
		return 0, true, "non_settle_venue"
	}
	return amount, true, ""
}

// LegSwapDiagnosis 类型 + DiagnoseLegSwapAmount 方法见 ledger_diagnosis.go（从 ledger_entries.go 拆出保 ≤300 行）。
