// Package exec 的 pipeline.go 实现执行管线（§13.1/§13.3）。
// ExecuteOpportunity: 复核 → 建 position(OPENING) → 双腿并发下单 → 回填状态。
// ClosePosition: 复核 OPEN → 双腿反向平仓 → 回填 realized_pnl → CLOSED。
package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/combo"
	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// RiskLimits 风控上限（§12 executor 配置）。
type RiskLimits struct {
	MaxNotionalUSD     float64
	MaxOpenPositions   int
	MaxLegImbalancePct float64
}

// 风控拒单 sentinel error（D-058）。ExecuteOpportunity RPC 据此映射 FAILED+message，非 CodeInternal。
var (
	// ErrMaxOpenPositions 持仓数达上限，拒绝新开仓。
	ErrMaxOpenPositions = errors.New("max open positions reached")
	// ErrDuplicateExecution 该机会已有未平仓持仓，拒绝重复执行（幂等防重）。
	ErrDuplicateExecution = errors.New("opportunity already has an open position")
	// ErrEmptyPositionType 持仓类型为空，拒绝建仓（修复⑭ S4：空 type 撞 positions_type_check，
	// 生产数据完整性隐患——空 type 持仓本不该被创建）。
	ErrEmptyPositionType = errors.New("position type required (empty type would violate positions_type_check)")
	// ErrEmptyLegs legs 为空，拒绝建仓（修复⑭ S4：nil legs 无下单意图，不应落库）。
	ErrEmptyLegs = errors.New("legs required (nil/empty legs has no execution intent)")
	// D-159 W1a R1 S2.3：SWAPARB 精确组合门禁错误。
	ErrComboNotAllowed      = errors.New("swaparb combo not in allowlist")
	ErrQtyExceedsLimit      = errors.New("swaparb leg qty exceeds allowlist limit")
	ErrGrossExposureExceeds = errors.New("swaparb gross exposure exceeds allowlist limit")
)

// Pipeline 是执行管线（§13.1）。
type Pipeline struct {
	registry *Registry
	store    *pgstore.Store
	qbus     *bus.QuoteBus
	limits   RiskLimits
	// verifier 平仓后 ticket 回读（D-077 修复⑩ G1）。nil = 未接线（测试，跳过回读）。
	verifier PositionVerifier
	// csResolver 合约规格查询（D-077 修复⑪A M1 平仓 PnL 权威化）。nil = CS 回退 1.0。
	csResolver ContractSizeResolver
	// pnlCalibrator 历史单权威 PnL 回读（D-077 修复⑪A M1 回读校准）。nil = 用同步公式。
	pnlCalibrator PnLCalibrator
	// fxResolver 计价币→USD 换算（D-105 T2 平仓 PnL 计价币换算）。nil = 非 USD 计价 fx 不可得。
	fxResolver FxResolver
	// revalidateAbsBps 复核劣化绝对阈值 bps（D-077 修复⑪B N4，ARB_REVALIDATE_ABS_BPS 默认 2.0）。
	revalidateAbsBps float64
	// revalidateRelPct 复核劣化相对阈值 %（D-077 修复⑪B N4，ARB_REVALIDATE_REL_PCT 默认 20）。
	revalidateRelPct float64
	// mtDisasterSlPct MT 灾难 SL 距离百分比（D-146 S1，ARB_MT_DISASTER_SL_PCT 默认 2.0）。
	// ≤0 = 禁用（不挂 SL）。仅 MT 腿（:mt4/:mt5）下单时用，CEX 腿忽略。
	mtDisasterSlPct float64

	// D-159 W1a R1 S2.3：SWAPARB 精确组合门禁——真实名义额检查。
	// swapArbComboAllow 非 nil 且 posType=SWAPARB 时，建 position 前校验：
	//   1. (canonical, venueBuy, venueSell) 在 allowlist
	//   2. 双腿名义额 ≤ spec.MaxLongQty/MaxShortQty（>0 才校验）
	//   3. 双腿名义额合计 USD ≤ spec.MaxGrossExposureUSD（>0 才校验）
	// notionalUSD 用于 qty→USD 换算（config.OpportunityNotionalUSD）。
	// nil = 未接线（gate 层 + dashboard 手动入口兜底，pipeline 不重复检查）。
	swapArbComboAllow []ComboAllowSpec
	notionalUSD       float64
}

// NewPipeline 创建执行管线。
func NewPipeline(reg *Registry, store *pgstore.Store, qbus *bus.QuoteBus, limits RiskLimits) *Pipeline {
	return &Pipeline{
		registry:         reg,
		store:            store,
		qbus:             qbus,
		limits:           limits,
		revalidateAbsBps: 2.0, // D-077 修复⑪B N4 默认值（SetRevalidateThresholds 可覆盖）
		revalidateRelPct: 20.0,
	}
}

// SetRevalidateThresholds 设置复核劣化双阈值（D-077 修复⑪B N4，env 化）。
func (p *Pipeline) SetRevalidateThresholds(absBps, relPct float64) {
	p.revalidateAbsBps = absBps
	p.revalidateRelPct = relPct
}

// SetMTDisasterSlPct 注入 MT 灾难 SL 距离百分比（D-146 S1，ARB_MT_DISASTER_SL_PCT）。
// ≤0 = 禁用（MT 腿不挂 SL，回退现行为）。main 装配后调。
func (p *Pipeline) SetMTDisasterSlPct(pct float64) { p.mtDisasterSlPct = pct }

// SetPositionVerifier 注入平仓后回读器（D-077 修复⑩ G1）。main 装配后调。
func (p *Pipeline) SetPositionVerifier(v PositionVerifier) { p.verifier = v }

// SetContractSizeResolver 注入合约规格查询器（D-077 修复⑪A M1）。main 装配后调。
func (p *Pipeline) SetContractSizeResolver(r ContractSizeResolver) { p.csResolver = r }

// SetPnLCalibrator 注入历史单权威 PnL 回读器（D-077 修复⑪A M1）。main 装配后调。
func (p *Pipeline) SetPnLCalibrator(c PnLCalibrator) { p.pnlCalibrator = c }

// SetFxResolver 注入计价币→USD 换算器（D-105 T2）。main 装配后调；*pgstore.Store 满足接口。
func (p *Pipeline) SetFxResolver(r FxResolver) { p.fxResolver = r }

// SetSwapArbComboAllow 注入 SWAPARB 精确组合门禁 allowlist + notionalUSD（D-159 W1a R1 S2.3）。
// nil/空 = 未接线（gate 层 + dashboard 手动入口兜底，pipeline 不重复检查）。
func (p *Pipeline) SetSwapArbComboAllow(allow []ComboAllowSpec, notionalUSD float64) {
	p.swapArbComboAllow = allow
	p.notionalUSD = notionalUSD
}

// LegSpec 是单腿下单规格。
type LegSpec struct {
	VenueKey      string // "venue:venueType"
	VenueSymbol   string // venue 原生符号
	Side          string // BUY / SELL
	Qty           float64
	PriceExpected float64 // 下单时本腿可执行价（买=ask 卖=bid，D-060 实施修正②：复盘 deviation 口径需要）
}

// ExecuteResult 是执行结果。
type ExecuteResult struct {
	PositionID int64
	Status     string // OPEN / LEGGED / FAILED
	Legs       []LegResult
}

// LegResult 是单腿成交结果。
type LegResult struct {
	VenueKey   string
	OrderID    string
	Price      float64
	Qty        float64
	Status     string // FILLED / FAILED
	Swap       float64
	Commission float64
}

// Execute 执行机会（§13.1）。
// opportunityID + requestID 幂等：重复提交返回已有 position。
func (p *Pipeline) Execute(ctx context.Context, opportunityID, requestID, posType string, legs []LegSpec) (*ExecuteResult, error) {
	// 风控⓪：fail-fast 入口守卫（修复⑭ S4）。空 type 撞 positions_type_check 约束、nil legs 无
	// 下单意图——两者均不应落库，在触达任何 DB 写或风控查询前拒绝（生产数据完整性隐患）。
	if posType == "" {
		return nil, ErrEmptyPositionType
	}
	if len(legs) == 0 {
		return nil, ErrEmptyLegs
	}

	// 风控①：持仓上限（D-058）。MaxOpenPositions>0 才启用；OPEN+OPENING+LEGGED 计数超限拒单。
	// check-then-act 无 DB 级原子性——当前单用户人工确认，接受窗口竞态（全自动才需约束）。
	if p.limits.MaxOpenPositions > 0 {
		n, err := p.store.CountOpenPositions(ctx)
		if err != nil {
			return nil, fmt.Errorf("count open positions: %w", err)
		}
		if n >= p.limits.MaxOpenPositions {
			return nil, ErrMaxOpenPositions
		}
	}

	// 风控②：幂等防重（D-058）。同机会已有未平仓持仓（OPEN/OPENING/LEGGED）拒绝重复执行，
	// 防双击/重试重复建仓；CLOSED/FAILED 可重入（新的执行意图）。requestID 级幂等需新列，暂以
	// opportunityID 级覆盖重复提交的主要危害，留待未来。
	dup, err := p.store.HasOpenPositionByOpportunity(ctx, opportunityID)
	if err != nil {
		return nil, fmt.Errorf("check existing position: %w", err)
	}
	if dup {
		return nil, ErrDuplicateExecution
	}

	// 风控③：下单前预检（D-121）：对每条腿调 executor.Precheck（如果实现了 Prechecker），
	// 任一腿预检失败 → 不建 position 不下单，直接返回错误（防 LEGGED）。
	// gate testnet 部分合约缺失（如 TRX_USDT）→ 不预检则一腿成交一腿失败→LEGGED。
	for _, leg := range legs {
		exe, ok := p.registry.Get(leg.VenueKey)
		if !ok {
			return nil, fmt.Errorf("precheck: no executor for venue %s", leg.VenueKey)
		}
		if pc, ok := exe.(Prechecker); ok {
			if err := pc.Precheck(ctx, Order{
				Symbol: leg.VenueSymbol,
				Side:   leg.Side,
				Qty:    leg.Qty,
				Venue:  leg.VenueKey,
			}); err != nil {
				return nil, fmt.Errorf("precheck leg %s: %w", leg.VenueKey, err)
			}
		}
	}

	// 风控④：D-159 W1a R1 S2.3 SWAPARB 精确组合门禁——真实名义额检查。
	// swapArbComboAllow 非 nil 且 posType=SWAPARB 时，建 position 前校验：
	//   1. (canonical, venueBuy, venueSell) 在 allowlist
	//   2. 双腿名义额 ≤ spec.MaxLongQty/MaxShortQty（>0 才校验）
	//   3. 双腿名义额合计 USD ≤ spec.MaxGrossExposureUSD（>0 才校验）
	// nil/空 = 未接线（gate 层 + dashboard 手动入口兜底）。
	if posType == "SWAPARB" && len(p.swapArbComboAllow) > 0 {
		if err := p.checkSwapArbComboAllow(ctx, opportunityID, legs); err != nil {
			return nil, err
		}
	}

	// 建 position(OPENING)——type 在创建时即写入（修复⑭ S4：原创建空 type 撞 positions_type_check，
	// 靠调用方事后 UpdatePositionMeta 补 type，空窗期约束已拒）。
	posID, err := p.store.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: opportunityID,
		Type:          posType,
		Status:        "OPENING",
	})
	if err != nil {
		return nil, fmt.Errorf("create position: %w", err)
	}

	// 双腿并发下单
	results := make([]LegResult, len(legs))
	var wg sync.WaitGroup
	for i, leg := range legs {
		wg.Add(1)
		go func(idx int, l LegSpec) {
			defer wg.Done()
			results[idx] = p.executeLeg(ctx, l)
		}(i, leg)
	}
	wg.Wait()

	// 判定最终状态
	filled := 0
	unknown := 0
	for _, r := range results {
		if r.Status == "FILLED" {
			filled++
		}
		if r.Status == "UNKNOWN" {
			unknown++
		}
	}

	var status string
	var isUnknown bool
	switch {
	case unknown > 0:
		// D-159 W1a R2：MT 超时→CLOSING+uncertainty_reason 过渡（SSOT I1，不新增 DB 枚举）。
		// 不平仓不假 FAILED——可能已成交，假 FAILED 会触发平仓=反向开新仓。
		status = "CLOSING"
		isUnknown = true
	case filled == len(legs):
		status = "OPEN"
	case filled == 0:
		status = "FAILED"
	default:
		status = "LEGGED"
	}

	// 回填 legs 到 DB（D-108 A1 fail-closed：腿写库失败 → error+critical 告警，孤儿不可留）
	for i, r := range results {
		if err := p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID:    posID,
			LegNo:         int16(i),
			Phase:         "OPEN",
			Venue:         legs[i].VenueKey,
			Symbol:        legs[i].VenueSymbol,
			Side:          legs[i].Side,
			Qty:           legs[i].Qty,
			PriceExpected: legs[i].PriceExpected,
			PriceActual:   &r.Price,
			OrderID:       r.OrderID,
			Status:        r.Status,
		}); err != nil {
			msg := fmt.Sprintf("leg persist failed position=%d leg=%d venue=%s: %v", posID, i, legs[i].VenueKey, err)
			log.Printf("[exec] %s", msg)
			_ = p.store.InsertAlert(ctx, nil, "critical", msg)
			return &ExecuteResult{PositionID: posID, Status: "FAILED", Legs: results}, fmt.Errorf("leg persist: %w", err)
		}
	}

	// 更新 position 状态
	if err := p.store.UpdatePositionStatus(ctx, posID, status); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already terminal, skip status=%s (G4)", posID, status)
		} else {
			log.Printf("[exec] position %d status update: %v", posID, err)
		}
	}
	// D-159 W1a R2：UNKNOWN 过渡写 uncertainty_reason（status=CLOSING，供前端/对账识别）。
	// 识别 UNKNOWN position：status='CLOSING' AND uncertainty_reason IS NOT NULL
	// 前端/对账用此条件区分正常平仓中 vs UNKNOWN 待确认
	if isUnknown {
		var unknownLegs []string
		for i, r := range results {
			if r.Status == "UNKNOWN" {
				unknownLegs = append(unknownLegs, fmt.Sprintf("leg%d(%s)", i, legs[i].VenueKey))
			}
		}
		reason := fmt.Sprintf("MT OrderSend timeout: %s", strings.Join(unknownLegs, ", "))
		if err := p.store.SetPositionUncertainty(ctx, posID, reason); err != nil {
			log.Printf("[exec] position %d uncertainty write: %v", posID, err)
		}
		_ = p.store.InsertAlert(ctx, &posID, "critical",
			fmt.Sprintf("position %d UNKNOWN: %s (manual verify required)", posID, reason))
	}

	if status == "LEGGED" {
		// D-127 P3b：纯 CEX 组合 LEGGED 自动收平——对 FILLED 腿立即反向 MARKET 平仓，
		// 消除裸敞口（#380/#383 人工 35+ 分钟兜底）。含 MT 腿的组合不动（SWAPARB 有独立平仓链路）。
		if allCEXLegs(legs) {
			autoFlattenCtx := context.WithoutCancel(ctx) // 收平不随 Execute 超时中断
			flattened, flattenErr := p.autoFlattenLEGGED(autoFlattenCtx, posID, legs, results)
			if flattenErr != nil {
				log.Printf("[exec] position %d LEGGED auto-flatten failed: %v (manual fallback)", posID, flattenErr)
			}
			if flattened {
				_ = p.store.UpdatePositionStatus(autoFlattenCtx, posID, "CLOSED")
				_ = p.store.InsertAlert(autoFlattenCtx, &posID, "critical",
					fmt.Sprintf("position %d LEGGED auto-flattened (open legs closed, root cause: partial fill)", posID))
				status = "CLOSED"
			} else {
				_ = p.store.InsertAlert(autoFlattenCtx, &posID, "critical",
					fmt.Sprintf("position %d LEGGED: %d/%d legs filled, auto-flatten incomplete", posID, filled, len(legs)))
			}
		} else {
			// critical 告警
			_ = p.store.InsertAlert(ctx, nil, "critical",
				fmt.Sprintf("position %d LEGGED: %d/%d legs filled", posID, filled, len(legs)))
		}
	}

	return &ExecuteResult{
		PositionID: posID,
		Status:     status,
		Legs:       results,
	}, nil
}

// checkSwapArbComboAllow D-159 W1a R1 S2.3：SWAPARB 精确组合门禁——真实名义额检查。
// 从 opportunityID 解析 (canonical, venueBuy, venueSell)，匹配 allowlist 后校验 qty/gross。
// grossUSD = Σ(qty × CS × price × fx)（SSOT 要求真实名义额，非 notionalUSD 近似）。
// CS/FX 缺失 → fail-closed 拒绝（不回退 1.0）；legs 中 BUY 腿=venueLong，SELL 腿=venueShort。
func (p *Pipeline) checkSwapArbComboAllow(ctx context.Context, opportunityID string, legs []LegSpec) error {
	comboKey := combo.FromOppID(opportunityID)
	// comboKey 格式 "STRATEGY:canonical:venueLong:venueShort"，venueKey 含 ":"（如 "acct:mt5"）。
	// SplitN(":",4) 会把 venueLong 截断 → 用全切分重组：strategy|canonical|acct|plat|acct|plat（6 段）。
	allParts := strings.Split(comboKey, ":")
	if len(allParts) < 6 {
		return fmt.Errorf("%w: cannot parse combo key %q (want 6+ parts, got %d)", ErrComboNotAllowed, comboKey, len(allParts))
	}
	canonical := allParts[1]
	venueBuy := allParts[2] + ":" + allParts[3]
	venueSell := allParts[4] + ":" + allParts[5]
	spec := MatchComboAllow(p.swapArbComboAllow, canonical, venueBuy, venueSell)
	if spec == nil {
		return fmt.Errorf("%w: %s %s×%s", ErrComboNotAllowed, canonical, venueBuy, venueSell)
	}
	var longQty, shortQty float64
	for _, leg := range legs {
		if leg.Side == "BUY" {
			longQty = leg.Qty
		} else if leg.Side == "SELL" {
			shortQty = leg.Qty
		}
	}
	if spec.MaxLongQty > 0 && longQty > spec.MaxLongQty {
		return fmt.Errorf("%w: long %.4f > %.4f", ErrQtyExceedsLimit, longQty, spec.MaxLongQty)
	}
	if spec.MaxShortQty > 0 && shortQty > spec.MaxShortQty {
		return fmt.Errorf("%w: short %.4f > %.4f", ErrQtyExceedsLimit, shortQty, spec.MaxShortQty)
	}
	// D-159 W1a R1 S2.3 返修：真实名义额 = qty × CS × price × fx（SSOT 要求，非 notionalUSD 近似）。
	// CS/FX 缺失 → fail-closed 拒绝（不回退 1.0）。
	if spec.MaxGrossExposureUSD > 0 {
		quoteCcy := notional.QuoteCcy(canonical)
		var grossUSD float64
		for _, leg := range legs {
			cs := 1.0
			if p.csResolver != nil {
				v, ok := p.csResolver.ContractSize(leg.VenueKey, leg.VenueSymbol)
				if !ok || v <= 0 {
					return fmt.Errorf("%w: CS missing for %s %s (fail-closed)", ErrComboNotAllowed, leg.VenueKey, leg.VenueSymbol)
				}
				cs = v
			} else {
				return fmt.Errorf("%w: csResolver not wired (fail-closed)", ErrComboNotAllowed)
			}
			fx := 1.0
			if quoteCcy != "" && quoteCcy != "USD" {
				if p.fxResolver != nil {
					rate, ok := p.fxResolver.USDPer(ctx, quoteCcy, time.Now())
					if !ok || rate <= 0 {
						return fmt.Errorf("%w: fx missing for %s (fail-closed)", ErrComboNotAllowed, quoteCcy)
					}
					fx = rate
				} else {
					return fmt.Errorf("%w: fxResolver not wired for non-USD quote ccy %s (fail-closed)", ErrComboNotAllowed, quoteCcy)
				}
			}
			grossUSD += leg.Qty * cs * leg.PriceExpected * fx
		}
		if grossUSD > spec.MaxGrossExposureUSD {
			return fmt.Errorf("%w: gross $%.2f > $%.2f", ErrGrossExposureExceeds, grossUSD, spec.MaxGrossExposureUSD)
		}
	}
	return nil
}
