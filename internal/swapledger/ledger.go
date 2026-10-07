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
	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

// 资金费率换算常量。
const (
	DaysPerYear = 365
)

// SwapPositionTypes 需要逐日 swap 记账的持仓类型。
// D-314：XASSET_CARRY 入列（混合 venue 类型，kind 由 cashflowKind per-leg 判定，与本清单归属无关——
// 清单只用于 Run()/reconcile 查持仓）。
var SwapPositionTypes = []string{"SWAPARB", "CARRY", "XASSET_CARRY"}

// FundingPositionTypes 需要逐日 funding 记账的持仓类型（8h×3 周期）。
var FundingPositionTypes = []string{"FUNDING_HEDGE", "PERP_FUNDING"}

// Store 依赖接口（pgstore.Store 实现）。
type Store interface {
	ListOpenPositionsByType(ctx context.Context, types []string) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	// D-159 W3a R5：per-leg 幂等（HasCashflowForLegDay 替代 position 级 HasCashflowForDay——
	// position 级短路会让失败腿永久漏记，R5：一腿失败下一轮只补失败腿）。
	HasCashflowForLegDay(ctx context.Context, posID int64, legNo int16, kind string, day time.Time) (bool, error)
	// D-193-A 扩包：生产写入迁 InsertCashflowFull（带事实身份字段）；旧 InsertCashflow 保留供测试基建。
	InsertCashflowFull(ctx context.Context, c *pgstore.Cashflow) (won bool, err error)
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) // D-508 S3：kind-only 无 OR 专用查询
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	// D-330 断层④：sellCanonical 三段链查询（pgstore 既有实现，接口补齐）——
	// SELL 腿 funding/swap fact 按侧向 canonical 查，跨 canonical 对不再必然 miss。
	GetOpportunity(ctx context.Context, oppID string) (*pgstore.Opportunity, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
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
	// D-108①：首轮同日有界重试（缺省 10min×6；零值回落缺省，测试注入缩小）。
	initialRetryBackoff time.Duration
	initialRetryMax     int
	factTTL             time.Duration // D-159 W4a R7：swap/funding Fact TTL（env 单源 cfg.SwapFactMaxAge，与 discoverSwapCombos/recalcNetSwap 同源）
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

// NewForDiagnosis D-193-A 扩包 S3：创建探针用 Ledger（无 loop 超时/延迟，factTTL=0 不检查过期）。
// 供 cmd/swapledgerprobe 诊断 legSwapAmount 中间状态用，非生产路径。
func NewForDiagnosis(store Store, notional NotionalResolver, tripleDay TripleDayResolver, nonSettleVenues []string, factTTL time.Duration) *Ledger {
	return New(store, notional, tripleDay, nonSettleVenues, factTTL)
}

// D-108① 首轮同日有界重试缺省：10min 间隔 ×6 次（≈1h 覆盖——生产实拍两次重启首轮
// load facts 超时均为 facts 冷缓存态，温热后即成功；重试用尽仍败=等次日 ticker）。
const (
	initialRetryBackoffDefault = 10 * time.Minute
	initialRetryMaxDefault     = 6
)

// runInitial 首轮 Run 同日有界重试：失败按 initialRetryBackoff 间隔重试至
// initialRetryMax 次成功即止；全败返回末错（当日缺口由次日 ticker 兜）。ctx 取消即停。
// 字段零值=缺省（New 未设时回落，测试注入缩小间隔）。
func (l *Ledger) runInitial(ctx context.Context) (int, error) {
	backoff, maxN := l.initialRetryBackoff, l.initialRetryMax
	if backoff <= 0 {
		backoff = initialRetryBackoffDefault
	}
	if maxN <= 0 {
		maxN = initialRetryMaxDefault
	}
	var lastErr error
	for attempt := 1; attempt <= maxN; attempt++ {
		n, err := l.Run(ctx)
		if err == nil {
			return n, nil
		}
		lastErr = err
		log.Printf("[swapledger] initial attempt %d/%d failed: %v", attempt, maxN, err)
		if attempt == maxN {
			break
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return 0, fmt.Errorf("initial after %d attempts: %w", maxN, lastErr)
}

// StartLoop 日频 ticker（对账 StartLoop 同模式，loop-initial-block 铁律）。
// 阻塞至 ctx 取消。首轮延迟 initialDelay（等 adapter 就绪），独立 goroutine 跑（不阻塞 ticker）。
func (l *Ledger) StartLoop(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// 首轮延迟（等 adapter 就绪），独立 goroutine 跑（不阻塞 ticker loop）。
	// D-108①：首轮失败同日有界重试（runInitial）——24h ticker 兜不了当日缺口。
	safego.Go("ledger:initial", func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(l.initialDelay):
		}
		if n, err := l.runInitial(ctx); err != nil {
			log.Printf("[swapledger] initial: %v", err)
		} else if n > 0 {
			log.Printf("[swapledger] initial: %d cashflows written", n)
		}
	})

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
