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
		amount, ok := l.legSwapAmount(canonical, leg, day, isFunding, longMap, shortMap, fundMap)
		if !ok {
			continue // swap rate / notional 不可用 → 跳过该腿（fail-closed，不猜）
		}
		if amount == 0 {
			continue // 零息 venue → 不写 cashflow（避免无意义行）
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
// 返回 (amount, ok)；ok=false 表示 rate/notional 不可用或 Fact 过期 → fail-closed 跳过。
// amount 正=收 swap，负=付 swap。
func (l *Ledger) legSwapAmount(canonical string, leg pgstore.PositionLeg, day time.Time,
	isFunding bool, longMap, shortMap, fundMap map[factKey]fact.Fact) (float64, bool) {
	venueName := venueFromKey(leg.Venue)
	fk := factKey{canonical, venueName}

	// 1. 取 swap/funding rate
	var f fact.Fact
	var rateFound bool
	if isFunding {
		f, rateFound = fundMap[fk]
	} else {
		// BUY(long) 腿用 swap_long，SELL(short) 腿用 swap_short
		if leg.Side == "BUY" {
			f, rateFound = longMap[fk]
		} else {
			f, rateFound = shortMap[fk]
		}
	}
	if !rateFound {
		return 0, false
	}
	// D-159 W3a R5 + W4a R7：Fact 过期防护——Fact.Ts 超 l.factTTL → fail-closed 不写估算为实际。
	// TTL 由 New 注入（cfg.SwapFactMaxAge env 单源，与 discoverSwapCombos/recalcNetSwap 同源）。
	if l.factTTL > 0 && time.Since(f.Ts) > l.factTTL {
		log.Printf("[swapledger] leg=%d swap fact expired (kind=%s venue=%s ts=%s, ttl=%v), skip",
			leg.LegNo, f.Kind, f.Venue, f.Ts.Format(time.RFC3339), l.factTTL)
		return 0, false
	}
	rate := f.Value

	// 2. 取 notional（usdPerLot × qty）
	side := "ask"
	if leg.Side == "SELL" {
		side = "bid"
	}
	usdPerLot, ok := l.notional.USDPerLotForLeg(canonical, leg.Venue, side)
	if !ok || usdPerLot <= 0 {
		return 0, false
	}
	notional := usdPerLot * leg.Qty
	if notional <= 0 {
		return 0, false
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
			return 0, false
		}
		amount = decimalAmount(rate, 10000, 1, notional, multiplier)
	}
	// D-112：已知不结算 venue → 记 0（不跳过，与零息 venue 同处理——amount=0 → accountPosition 跳过不写 cashflow）
	if l.nonSettleVenues[leg.Venue] {
		return 0, true
	}
	return amount, true
}

// LegSwapDiagnosis D-193-A 扩包 S3：leg_swapAmount 诊断结果（只读探针用）。
type LegSwapDiagnosis struct {
	PositionID    int64
	LegNo         int16
	Venue         string
	Side          string
	Canonical     string
	Day           time.Time
	FactKey       string  // 所查 fact 键 "canonical:venue"
	FactKind      string  // swap_long/swap_short/funding
	FactHit       bool    // fact 是否命中
	FactValue     float64 // fact.Value（命中时）
	FactTs        time.Time
	FactExpired   bool // Fact.Ts 超 factTTL
	NotionalHit   bool // usdPerLot 是否命中
	UsdPerLot     float64
	Notional      float64
	Multiplier    float64
	MultiplierOK  bool
	NonSettleVenue bool
	Ok            bool    // legSwapAmount 最终 ok
	Amount        float64 // legSwapAmount 最终 amount
	Reason        string  // ok=false 或 amount=0 的原因
}

// DiagnoseLegSwapAmount D-193-A 扩包 S3：只读诊断单腿 legSwapAmount 的中间状态。
// 对 478-481×leg1×9-2~9-4 输出每格 (ok, amount) + 所查 fact 键与命中状态。
// 结论三选一：ok=false（列出缺失 fact 键）/amount=0/正常（不该发生，停手回报）。
// 用生产库只读 DSN 由复审方跑；施工方用测试库验证逻辑。
func (l *Ledger) DiagnoseLegSwapAmount(ctx context.Context, pos pgstore.Position, leg pgstore.PositionLeg, day time.Time,
	longMap, shortMap, fundMap map[factKey]fact.Fact) LegSwapDiagnosis {
	canonical := canonicalFromOppID(pos.OpportunityID)
	isFunding := isFundingType(pos.Type)
	d := LegSwapDiagnosis{
		PositionID: pos.ID,
		LegNo:      leg.LegNo,
		Venue:      leg.Venue,
		Side:       leg.Side,
		Canonical:  canonical,
		Day:        day,
	}

	// 1. 取 swap/funding rate
	venueName := venueFromKey(leg.Venue)
	fk := factKey{canonical, venueName}
	d.FactKey = canonical + ":" + venueName
	var f fact.Fact
	var rateFound bool
	if isFunding {
		d.FactKind = fact.KindFunding
		f, rateFound = fundMap[fk]
	} else {
		if leg.Side == "BUY" {
			d.FactKind = fact.KindSwapLong
			f, rateFound = longMap[fk]
		} else {
			d.FactKind = fact.KindSwapShort
			f, rateFound = shortMap[fk]
		}
	}
	d.FactHit = rateFound
	if rateFound {
		d.FactValue = f.Value
		d.FactTs = f.Ts
		if l.factTTL > 0 && time.Since(f.Ts) > l.factTTL {
			d.FactExpired = true
			d.Ok = false
			d.Reason = "fact expired"
			return d
		}
	} else {
		d.Ok = false
		d.Reason = "fact not found (kind=" + d.FactKind + ")"
		return d
	}

	// 2. 取 notional
	side := "ask"
	if leg.Side == "SELL" {
		side = "bid"
	}
	usdPerLot, ok := l.notional.USDPerLotForLeg(canonical, leg.Venue, side)
	d.NotionalHit = ok
	d.UsdPerLot = usdPerLot
	if !ok || usdPerLot <= 0 {
		d.Ok = false
		d.Reason = "notional unavailable"
		return d
	}
	notional := usdPerLot * leg.Qty
	d.Notional = notional
	if notional <= 0 {
		d.Ok = false
		d.Reason = "notional <= 0"
		return d
	}

	// 3. 算金额
	if isFunding {
		d.Multiplier = 1
		d.MultiplierOK = true
		d.Amount = decimalAmount(f.Value, 100, DaysPerYear, notional, 1)
	} else {
		multiplier := settleMultiplier(leg.Venue, canonical, day, l.tripleDay)
		d.Multiplier = multiplier
		d.MultiplierOK = multiplier != 0
		if multiplier == 0 {
			d.Ok = false
			d.Reason = "settleMultiplier=0 (triple swap day unknown)"
			return d
		}
		d.Amount = decimalAmount(f.Value, 10000, 1, notional, multiplier)
	}

	// 4. nonSettleVenue 检查
	d.NonSettleVenue = l.nonSettleVenues[leg.Venue]
	if d.NonSettleVenue {
		d.Amount = 0
		d.Ok = true
		d.Reason = "non-settle venue (amount=0)"
		return d
	}

	d.Ok = true
	if d.Amount == 0 {
		d.Reason = "amount=0 (zero swap)"
	} else {
		d.Reason = "normal"
	}
	return d
}
