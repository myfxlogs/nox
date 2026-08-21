// Package swapledger 实现 swap 逐日记账（D-077 修复⑨ V1）。
//
// 独立日频 ticker（对账 StartLoop 同模式，带 per-call 超时+就绪降级——经验库 loop-initial-block 铁律）：
// 对 SWAPARB/CARRY 的 OPEN 仓逐腿记 swap_rate(bps/日) × notional(usdPerLot×qty) × settleMultiplier
// → position_cashflows(kind='swap') 当日一行（幂等：当日已记跳过）。
//
// V1 主路径=公式记账+日对账（proto BrokerPosition 无 swap 字段实证，broker 差分降为未来触发条件）。
// V2 三倍日校准：swap 采集轮顺带读 SymbolParams.ThreeDaysSwap → per-venue 三倍日（facts 表，勿新表）。
package swapledger

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// 资金费率周期具名常量（抄 arbcn settle.go，funding 类 8h×3/日）。
const (
	PeriodsPerDay = 3
	DaysPerYear   = 365
)

// SwapPositionTypes 需要逐日 swap 记账的持仓类型。
var SwapPositionTypes = []string{"SWAPARB", "CARRY"}

// FundingPositionTypes 需要逐日 funding 记账的持仓类型（8h×3 周期）。
var FundingPositionTypes = []string{"FUNDING_HEDGE", "PERP_FUNDING"}

// Store 依赖接口（pgstore.Store 实现）。
type Store interface {
	ListOpenPositionsByType(ctx context.Context, types []string) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	HasCashflowForDay(ctx context.Context, posID int64, kind string, day time.Time) (bool, error)
	InsertCashflow(ctx context.Context, posID int64, kind, venue string, amount float64, ts time.Time) error
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// NotionalResolver 单腿 USD 名义额查询（dashboard.Service.USDPerLotForLeg 实现）。
// canonical + venueKey + side("ask"/"bid") → usdPerLot（1 手的 USD 名义）。
type NotionalResolver interface {
	USDPerLotForLeg(canonical, venueKey, side string) (float64, bool)
}

// TripleDayResolver per-venue 三倍 swap 日查询（D-077 V2）。
// venue → time.Weekday（0=Sunday...6=Saturday）；ok=false 表示未知（回退周三假设）。
type TripleDayResolver interface {
	TripleSwapDay(venue string) (time.Weekday, bool)
}

// Ledger swap 逐日记账器（D-077 V1）。
type Ledger struct {
	store        Store
	notional     NotionalResolver
	tripleDay    TripleDayResolver
	runTimeout   time.Duration // 单次外部调用超时（loop-initial-block 铁律：默认 30s）
	initialDelay time.Duration // 首轮延迟（等 adapter 就绪，默认 90s）
}

// New 创建 swap 记账器。
func New(store Store, notional NotionalResolver, tripleDay TripleDayResolver) *Ledger {
	return &Ledger{
		store:        store,
		notional:     notional,
		tripleDay:    tripleDay,
		runTimeout:   30 * time.Second,
		initialDelay: 90 * time.Second,
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

// factKey (canonical, venue) → swap/funding rate。
type factKey struct {
	canonical, venue string
}

func (l *Ledger) loadFacts(ctx context.Context) (map[factKey]float64, map[factKey]float64, map[factKey]float64, error) {
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
	longMap := make(map[factKey]float64, len(longFacts))
	for _, f := range longFacts {
		longMap[factKey{f.Symbol, f.Venue}] = f.Value
	}
	shortMap := make(map[factKey]float64, len(shortFacts))
	for _, f := range shortFacts {
		shortMap[factKey{f.Symbol, f.Venue}] = f.Value
	}
	fundMap := make(map[factKey]float64, len(fundingFacts))
	for _, f := range fundingFacts {
		fundMap[factKey{f.Symbol, f.Venue}] = f.Value
	}
	return longMap, shortMap, fundMap, nil
}

// accountPosition 对单个持仓逐腿记 swap/funding cashflow（幂等：当日已记跳过）。
func (l *Ledger) accountPosition(ctx context.Context, pos pgstore.Position, day time.Time,
	longMap, shortMap, fundMap map[factKey]float64) (int, error) {
	// 幂等检查：当日已记 swap/funding → 跳过
	kind := cashflowKind(pos.Type)
	has, err := l.store.HasCashflowForDay(ctx, pos.ID, kind, day)
	if err != nil {
		return 0, fmt.Errorf("check existing cashflow: %w", err)
	}
	if has {
		return 0, nil
	}

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
	written := 0
	for _, leg := range legs {
		// 只记 OPEN phase 的腿（CLOSE phase 是平仓腿，无 swap 记账）
		if leg.Phase != "OPEN" {
			continue
		}
		amount, ok := l.legSwapAmount(canonical, leg, day, isFunding, longMap, shortMap, fundMap)
		if !ok {
			continue // swap rate / notional 不可用 → 跳过该腿（fail-closed，不猜）
		}
		if amount == 0 {
			continue // 零息 venue → 不写 cashflow（避免无意义行）
		}
		if err := l.store.InsertCashflow(ctx, pos.ID, kind, leg.Venue, amount, day); err != nil {
			return written, fmt.Errorf("insert cashflow leg %d: %w", leg.LegNo, err)
		}
		written++
	}
	return written, nil
}

// legSwapAmount 计算单腿当日 swap/funding USD 金额（纯函数，可单测）。
// 返回 (amount, ok)；ok=false 表示 rate/notional 不可用 → fail-closed 跳过。
// amount 正=收 swap，负=付 swap。
func (l *Ledger) legSwapAmount(canonical string, leg pgstore.PositionLeg, day time.Time,
	isFunding bool, longMap, shortMap, fundMap map[factKey]float64) (float64, bool) {
	venueName := venueFromKey(leg.Venue)
	fk := factKey{canonical, venueName}

	// 1. 取 swap/funding rate
	var rate float64
	var rateFound bool
	if isFunding {
		rate, rateFound = fundMap[fk]
	} else {
		// BUY(long) 腿用 swap_long，SELL(short) 腿用 swap_short
		if leg.Side == "BUY" {
			rate, rateFound = longMap[fk]
		} else {
			rate, rateFound = shortMap[fk]
		}
	}
	if !rateFound {
		return 0, false
	}

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

	// 3. 算金额
	if isFunding {
		// funding：年化百分点 → 日金额 = rate/100/DaysPerYear × notional × PeriodsPerDay
		// = rate/100/365 × notional × 3（8h×3 周期/日，arbcn PeriodsPerDay 具名常量）
		return rate / 100 / DaysPerYear * notional * PeriodsPerDay, true
	}

	// swap：日 bps → 日金额 = rate/10000 × notional × settleMultiplier
	multiplier := settleMultiplier(leg.Venue, day, l.tripleDay)
	return rate / 10000 * notional * multiplier, true
}

// settleMultiplier 返回当日结算乘数（D-077 V2：三倍日→3 否则 1）。
// 纯函数：venueKey 当日=该 venue 三倍 swap 日→3 否则 1。
// TripleDayResolver 未知（ok=false）→ 回退周三假设（archive 经验，待 V2 实测修正）。
func settleMultiplier(venueKey string, day time.Time, r TripleDayResolver) float64 {
	venueName := venueFromKey(venueKey)
	tripleDay, ok := r.TripleSwapDay(venueName)
	if !ok {
		tripleDay = time.Wednesday // 回退周三假设（archive 经验，待 V2 实测修正）
	}
	if day.Weekday() == tripleDay {
		return 3
	}
	return 1
}

// venueFromKey 从 venueKey（格式 "venueName:mt5"）提取 venue 名。
func venueFromKey(venueKey string) string {
	idx := strings.Index(venueKey, ":")
	if idx < 0 {
		return venueKey
	}
	return venueKey[:idx]
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
func canonicalFromOppID(oppID string) string {
	parts := strings.Split(oppID, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// cashflowKind 按持仓类型返回 cashflow kind（swap 或 funding）。
func cashflowKind(posType string) string {
	if isFundingType(posType) {
		return "funding"
	}
	return "swap"
}

// isFundingType 判断是否为 funding 类持仓（8h×3 周期）。
func isFundingType(posType string) bool {
	for _, t := range FundingPositionTypes {
		if t == posType {
			return true
		}
	}
	return false
}
