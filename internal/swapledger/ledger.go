// Package swapledger 实现 swap 逐日记账（D-077 修复⑨ V1）。
//
// 独立日频 ticker（对账 StartLoop 同模式，带 per-call 超时+就绪降级——经验库 loop-initial-block 铁律）：
// 对 SWAPARB/CARRY 的 OPEN 仓逐腿记 swap_rate(bps/日) × notional(usdPerLot×qty) × settleMultiplier
// → position_cashflows(kind='swap') 当日一行（幂等：当日已记跳过）。
//
// V1 主路径=公式记账+日对账（proto BrokerPosition 无 swap 字段实证，broker 差分降为未来触发条件）。
// V2 三倍日校准：swap 采集轮顺带读 SymbolParams.ThreeDaysSwap → per-venue 三倍日（facts 表，勿新表）。
//
// D-077 修复⑪A M5 周末结算模型标注：
// 周末结算模型未标定（无数据支撑=会造新假设）——保留现模型（每日按采集到的 swap_rate 记账，
// 不区分工作日/结算日）+ 依赖 M3 per-symbol 三倍日数据与既有日对账告警纠偏：
// 日对账偏差 >5% 告警已会抓周末多记/少记；per-venue 结算日历待对账数据积累后建模（不做无数据假设）。
package swapledger

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// 资金费率换算常量。
const (
	DaysPerYear = 365
)

// SwapPositionTypes 需要逐日 swap 记账的持仓类型。
var SwapPositionTypes = []string{"SWAPARB", "CARRY"}

// FundingPositionTypes 需要逐日 funding 记账的持仓类型（8h×3 周期）。
var FundingPositionTypes = []string{"FUNDING_HEDGE", "PERP_FUNDING"}

// Store 依赖接口（pgstore.Store 实现）。
type Store interface {
	ListOpenPositionsByType(ctx context.Context, types []string) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	// D-159 W3a R5：per-leg 幂等（HasCashflowForLegDay 替代 position 级 HasCashflowForDay——
	// position 级短路会让失败腿永久漏记，R5：一腿失败下一轮只补失败腿）。
	HasCashflowForLegDay(ctx context.Context, posID int64, legNo int16, kind string, day time.Time) (bool, error)
	InsertCashflow(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// NotionalResolver 单腿 USD 名义额查询（dashboard.Service.USDPerLotForLeg 实现）。
// canonical + venueKey + side("ask"/"bid") → usdPerLot（1 手的 USD 名义）。
type NotionalResolver interface {
	USDPerLotForLeg(canonical, venueKey, side string) (float64, bool)
}

// TripleDayResolver per-venue/per-symbol 三倍 swap 日查询（D-077 V2→修复⑪A M3）。
// 优先级：symbol 级 Fact > venue 级 Fact > fail-closed（M4：删周三回退）。
type TripleDayResolver interface {
	TripleSwapDay(venue, canonical string) (time.Weekday, bool)
}

// Ledger swap 逐日记账器（D-077 V1）。
type Ledger struct {
	store           Store
	notional        NotionalResolver
	tripleDay       TripleDayResolver
	nonSettleVenues map[string]bool // D-112：已知不结算 swap 的 venue 集合（key=PositionLeg.Venue 完整 key）
	runTimeout      time.Duration   // 单次外部调用超时（loop-initial-block 铁律：默认 30s）
	initialDelay    time.Duration   // 首轮延迟（等 adapter 就绪，默认 90s）
	factTTL         time.Duration   // D-159 W4a R7：swap/funding Fact TTL（env 单源 cfg.SwapFactMaxAge，与 discoverSwapCombos/recalcNetSwap 同源）
}

// New 创建 swap 记账器。nonSettleVenues 为已知不结算 swap 的 venue 列表（D-112）。
// factTTL 为 swap/funding Fact 过期阈值（D-159 W4a R7，cfg.SwapFactMaxAge env 单源）。
func New(store Store, notional NotionalResolver, tripleDay TripleDayResolver, nonSettleVenues []string, factTTL time.Duration) *Ledger {
	nsv := make(map[string]bool, len(nonSettleVenues))
	for _, v := range nonSettleVenues {
		nsv[v] = true
	}
	return &Ledger{
		store:           store,
		notional:        notional,
		tripleDay:       tripleDay,
		nonSettleVenues: nsv,
		runTimeout:      30 * time.Second,
		initialDelay:    90 * time.Second,
		factTTL:         factTTL,
	}
}

// StartLoop 日频 ticker（对账 StartLoop 同模式，loop-initial-block 铁律）。
// 阻塞至 ctx 取消。首轮延迟 initialDelay（等 adapter 就绪），独立 goroutine 跑（不阻塞 ticker）。
func (l *Ledger) StartLoop(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// 首轮延迟（等 adapter 就绪），独立 goroutine 跑（不阻塞 ticker loop）
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.initialDelay):
		}
		if n, err := l.Run(ctx); err != nil {
			log.Printf("[swapledger] initial: %v", err)
		} else if n > 0 {
			log.Printf("[swapledger] initial: %d cashflows written", n)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := l.Run(ctx); err != nil {
				log.Printf("[swapledger] run: %v", err)
			} else if n > 0 {
				log.Printf("[swapledger] run: %d cashflows written", n)
			}
		}
	}
}

// Run 执行一轮 swap 记账（所有 OPEN 的 SWAPARB/CARRY/FUNDING_HEDGE/PERP_FUNDING 仓）。
// 返回写入的 cashflow 行数。每个外部调用包 runTimeout 超时（loop-initial-block 铁律）。
func (l *Ledger) Run(ctx context.Context) (int, error) {
	// 合并 swap + funding 类型
	allTypes := append(append([]string{}, SwapPositionTypes...), FundingPositionTypes...)

	pctx, cancel := context.WithTimeout(ctx, l.runTimeout)
	defer cancel()
	positions, err := l.store.ListOpenPositionsByType(pctx, allTypes)
	if err != nil {
		return 0, fmt.Errorf("list open positions: %w", err)
	}
	if len(positions) == 0 {
		return 0, nil
	}

	// 预加载 swap_long / swap_short / funding facts（一次查询，避免 per-leg 查库）
	swapLongMap, swapShortMap, fundingMap, err := l.loadFacts(pctx)
	if err != nil {
		return 0, fmt.Errorf("load facts: %w", err)
	}

	today := time.Now().UTC()
	written := 0
	for _, pos := range positions {
		n, err := l.accountPosition(pctx, pos, today, swapLongMap, swapShortMap, fundingMap)
		if err != nil {
			log.Printf("[swapledger] position %d: %v", pos.ID, err)
			continue
		}
		written += n
	}
	return written, nil
}

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
		if err := l.store.InsertCashflow(ctx, pos.ID, leg.LegNo, kind, leg.Venue, amount, day); err != nil {
			return written, fmt.Errorf("insert cashflow leg %d: %w", leg.LegNo, err)
		}
		written++
	}
	return written, nil
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
