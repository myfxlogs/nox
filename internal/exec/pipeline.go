// Package exec 的 pipeline.go 实现执行管线（§13.1/§13.3）。
// ExecuteOpportunity: 复核 → 建 position(OPENING) → 双腿并发下单 → 回填状态。
// ClosePosition: 复核 OPEN → 双腿反向平仓 → 回填 realized_pnl → CLOSED。
package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
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
}

// NewPipeline 创建执行管线。
func NewPipeline(reg *Registry, store *pgstore.Store, qbus *bus.QuoteBus, limits RiskLimits) *Pipeline {
	return &Pipeline{registry: reg, store: store, qbus: qbus, limits: limits}
}

// SetPositionVerifier 注入平仓后回读器（D-077 修复⑩ G1）。main 装配后调。
func (p *Pipeline) SetPositionVerifier(v PositionVerifier) { p.verifier = v }

// SetContractSizeResolver 注入合约规格查询器（D-077 修复⑪A M1）。main 装配后调。
func (p *Pipeline) SetContractSizeResolver(r ContractSizeResolver) { p.csResolver = r }

// SetPnLCalibrator 注入历史单权威 PnL 回读器（D-077 修复⑪A M1）。main 装配后调。
func (p *Pipeline) SetPnLCalibrator(c PnLCalibrator) { p.pnlCalibrator = c }

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
func (p *Pipeline) Execute(ctx context.Context, opportunityID, requestID string, legs []LegSpec) (*ExecuteResult, error) {
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

	// 建 position(OPENING)
	posID, err := p.store.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: opportunityID,
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
	for _, r := range results {
		if r.Status == "FILLED" {
			filled++
		}
	}

	var status string
	switch filled {
	case len(legs):
		status = "OPEN"
	case 0:
		status = "FAILED"
	default:
		status = "LEGGED"
	}

	// 回填 legs 到 DB
	for i, r := range results {
		_ = p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
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
		})
	}

	// 更新 position 状态
	if err := p.store.UpdatePositionStatus(ctx, posID, status); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already terminal, skip status=%s (G4)", posID, status)
		} else {
			log.Printf("[exec] position %d status update: %v", posID, err)
		}
	}

	if status == "LEGGED" {
		// critical 告警
		_ = p.store.InsertAlert(ctx, nil, "critical",
			fmt.Sprintf("position %d LEGGED: %d/%d legs filled", posID, filled, len(legs)))
	}

	return &ExecuteResult{
		PositionID: posID,
		Status:     status,
		Legs:       results,
	}, nil
}

func (p *Pipeline) executeLeg(ctx context.Context, leg LegSpec) LegResult {
	exe, ok := p.registry.Get(leg.VenueKey)
	if !ok {
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	fill, err := exe.PlaceOrder(ctx, Order{
		Symbol: leg.VenueSymbol,
		Side:   leg.Side,
		Qty:    leg.Qty,
		Venue:  leg.VenueKey,
	})
	if err != nil {
		log.Printf("[exec] leg %s %s %s: %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	return LegResult{
		VenueKey:   leg.VenueKey,
		OrderID:    fill.OrderID,
		Price:      fill.Price,
		Qty:        fill.Qty,
		Status:     "FILLED",
		Swap:       fill.Swap,
		Commission: fill.Commission,
	}
}

// ClosePosition 平仓（§13.3）。
// D-068 修复：MT5 hedging 模式下 PlaceOrder 反向下单 = 开新仓不是平仓。
// MT5/MT4 腿用 CloseOrder（OrderClose 按 ticket 平仓）；CEX 腿用 PlaceOrder 反向减仓（D-059）。
func (p *Pipeline) ClosePosition(ctx context.Context, posID int64) error {
	legs, err := p.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("list legs: %w", err)
	}

	if err := p.store.UpdatePositionStatus(ctx, posID, "CLOSING"); err != nil {
		if errors.Is(err, pgstore.ErrTerminalState) {
			log.Printf("[exec] position %d already terminal, skip CLOSING (G4)", posID)
			return err
		}
		log.Printf("[exec] position %d CLOSING update: %v", posID, err)
	}

	var totalPnL float64
	for _, leg := range legs {
		if leg.Status != "FILLED" {
			continue
		}
		exe, ok := p.registry.Get(leg.Venue)
		if !ok {
			continue
		}
		// D-068 修复：MT5/MT4 用 CloseOrder（按 ticket 平仓）；CEX 用 PlaceOrder 反向减仓。
		var fill Fill
		var closeErr error
		if strings.HasPrefix(leg.Venue, "mt5:") || strings.HasSuffix(leg.Venue, ":mt5") ||
			strings.HasPrefix(leg.Venue, "mt4:") || strings.HasSuffix(leg.Venue, ":mt4") {
			fill, closeErr = exe.CloseOrder(ctx, OrderRef{OrderID: leg.OrderID, Venue: leg.Venue})
		} else {
			// CEX 反向减仓（D-059 posSide 修正）
			closeSide := "SELL"
			if leg.Side == "SELL" {
				closeSide = "BUY"
			}
			var positionSide string
			switch leg.Side {
			case "BUY":
				positionSide = "long"
			case "SELL":
				positionSide = "short"
			}
			fill, closeErr = exe.PlaceOrder(ctx, Order{
				Symbol:       leg.Symbol,
				Side:         closeSide,
				Qty:          leg.Qty,
				Venue:        leg.Venue,
				PositionSide: positionSide,
			})
		}
		if closeErr != nil {
			log.Printf("[exec] close leg %d (ticket=%s venue=%s): %v", leg.ID, leg.OrderID, leg.Venue, closeErr)
			continue
		}
		// D-069：记录 CLOSE phase leg（平仓价/订单ID/方向持久化，供成交历史复盘）
		closeSide := "SELL"
		if leg.Side == "SELL" {
			closeSide = "BUY"
		}
		closePrice := fill.Price
		_ = p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID:    posID,
			LegNo:         leg.LegNo,
			Phase:         "CLOSE",
			Venue:         leg.Venue,
			Account:       leg.Account,
			Symbol:        leg.Symbol,
			Side:          closeSide,
			Qty:           leg.Qty,
			PriceExpected: leg.PriceExpected,
			PriceActual:   &closePrice,
			OrderID:       fill.OrderID,
			Status:        "FILLED",
		})
		// D-077 修复⑪A M1：平仓 PnL 权威化——删简化公式，改双路。
		// 同步路径（即刻）：(Fill.Price − 开仓价) × qty × CS × 方向 + Fill.Swap − Fill.Commission
		// CS 经服务器规格换算（修掉漏 CS 的 100 倍错误）；CEX 腿 CS=1（qty 即基础币）。
		legPnL := p.calcLegPnL(leg, fill)
		totalPnL += legPnL
		// swap 入 cashflows
		if fill.Swap != 0 {
			_ = p.store.InsertCashflow(ctx, posID, "swap", leg.Venue, fill.Swap, time.Now())
		}
		// D-069：commission 入 cashflows（负号=成本）
		if fill.Commission != 0 {
			_ = p.store.InsertCashflow(ctx, posID, "commission", leg.Venue, -fill.Commission, time.Now())
		}
	}

	// D-077 修复⑩ G1：平仓后 ticket 回读——按 ticket 回读各 venue 在仓订单，
	// ticket 仍在 → 不置 CLOSED + critical「broker 残留腿」（防 DB 假 CLOSED 留孤儿）。
	if p.verifier != nil {
		if residual := p.checkResidualLegs(ctx, legs); len(residual) > 0 {
			msg := fmt.Sprintf("broker 残留腿 position=%d: %s", posID, strings.Join(residual, ", "))
			log.Printf("[exec] %s", msg)
			_ = p.store.InsertAlert(ctx, &posID, "critical", msg)
			// 不置 CLOSED，留 OPEN 状态等对账自动收尾兜底
			return fmt.Errorf("residual legs after close: %s", strings.Join(residual, ", "))
		}
	}

	// D-077 修复⑪A M1 回读校准：历史单权威 PnL 覆盖同步路径公式。
	// 命中 → 用权威值（Profit+Swap−Commission，与 auto_close.go:99 统一）；未命中 → 用同步公式。
	if authPnL, ok := p.calibratePnL(ctx, legs); ok {
		log.Printf("[exec] position %d PnL calibrated: sync=%.2f → auth=%.2f", posID, totalPnL, authPnL)
		totalPnL = authPnL
	}

	if err := p.store.ClosePosition(ctx, posID, totalPnL); err != nil {
		log.Printf("[exec] close position %d store update: %v", posID, err)
	}
	return nil
}

// checkResidualLegs 平仓后按 ticket 回读各 venue 在仓订单，返回残留腿描述（D-077 修复⑩ G1）。
// 只检查 FILLED 的 OPEN phase 腿（CLOSE phase 是刚写的平仓腿，不查）。
// verifier 不可用/超时 → 返回空（fail-open：不阻断平仓流程，对账自动收尾兜底）。
func (p *Pipeline) checkResidualLegs(ctx context.Context, legs []pgstore.PositionLeg) []string {
	if p.verifier == nil {
		return nil // 未接线 → 跳过回读
	}
	// 按 venue 分组待查 ticket
	venueTickets := map[string][]string{} // venueKey → []ticket
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" || leg.OrderID == "" {
			continue
		}
		venueTickets[leg.Venue] = append(venueTickets[leg.Venue], leg.OrderID)
	}
	if len(venueTickets) == 0 {
		return nil
	}

	var residual []string
	for venueKey, tickets := range venueTickets {
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		orders, err := p.verifier.QueryOpenedOrders(vctx, venueKey)
		cancel()
		if err != nil {
			log.Printf("[exec] verify residual %s: %v (fail-open, skip)", venueKey, err)
			continue
		}
		// 构建 ticket → 在仓 集合
		opened := make(map[string]bool, len(orders))
		for _, o := range orders {
			opened[o.Ticket] = true
		}
		for _, ticket := range tickets {
			if opened[ticket] {
				residual = append(residual, fmt.Sprintf("%s ticket=%s", venueKey, ticket))
			}
		}
	}
	return residual
}

// calcLegPnL 同步路径计算单腿平仓 PnL（D-077 修复⑪A M1）。
// 公式：(Fill.Price − 开仓价) × qty × CS × 方向 + Fill.Swap − Fill.Commission
// CS：MT 腿经 csResolver 查服务器规格（修掉漏 CS 的 100 倍错误）；CEX 腿 CS=1。
// 开仓价缺失 → PnL=0（不猜，留对账纠偏）。
func (p *Pipeline) calcLegPnL(leg pgstore.PositionLeg, fill Fill) float64 {
	if leg.PriceActual == nil {
		return 0
	}
	cs := 1.0
	if p.csResolver != nil {
		if v, ok := p.csResolver.ContractSize(leg.Venue, leg.Symbol); ok && v > 0 {
			cs = v
		}
	}
	dir := 1.0
	if leg.Side == "SELL" {
		dir = -1.0
	}
	priceDiff := (fill.Price - *leg.PriceActual) * leg.Qty * cs * dir
	return priceDiff + fill.Swap - fill.Commission
}

// calibratePnL 回读校准：按 ticket 查历史单权威 PnL（D-077 修复⑪A M1 回读校准）。
// 命中 HistoryOrder.Profit 时以其为权威覆盖同步路径公式结果。
// calibrator 未接线/未找到 → 返回 ok=false（用同步路径公式结果）。
func (p *Pipeline) calibratePnL(ctx context.Context, legs []pgstore.PositionLeg) (float64, bool) {
	if p.pnlCalibrator == nil {
		return 0, false
	}
	var totalPnL float64
	calibrated := 0
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" || leg.OrderID == "" {
			continue
		}
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		hp, err := p.pnlCalibrator.QueryHistoryPnL(vctx, leg.Venue, leg.OrderID, 3)
		cancel()
		if err != nil || hp == nil {
			continue
		}
		// 权威 PnL = Profit + Swap − Commission（与 auto_close.go:99 真值路径统一，P3）
		totalPnL += hp.Profit + hp.Swap - hp.Commission
		calibrated++
	}
	if calibrated == 0 {
		return 0, false
	}
	return totalPnL, true
}

// Revalidate 用 QuoteBus 最新报价重算毛收益劣化（§13.1 双阈值；D-051 修③：毛口径比较）。
// 成本侧（佣金/滑点）执行前后不变，净收益劣化完全由毛位移驱动 → 用 originalGrossBps 与重算
// currentBps（spread/mid×10000）同口径比较才正确。返回 true 通过复核，false 劣化超阈值应拒绝。
// D-068 修复：FUNDING_HEDGE 收益来自 funding rate 差不是价格差，价格差近 0 是正常的，
// 用价格差复核会误拒 → FUNDING_HEDGE 只检查报价新鲜度（QuoteBus 有报价即可），跳过价差劣化。
func (p *Pipeline) Revalidate(canonical, venueBuy, venueSell, oppType string, originalGrossBps float64) bool {
	qbuy, ok1 := p.qbus.Latest(canonical, venueBuy)
	qsell, ok2 := p.qbus.Latest(canonical, venueSell)
	if !ok1 || !ok2 {
		return false // 报价缺失 = 不通过
	}
	// FUNDING_HEDGE：只检查报价新鲜度，跳过价格差劣化（funding rate 秒级不变）
	if oppType == "FUNDING_HEDGE" {
		return true
	}
	// CROSS_VENUE：重算瞬时毛 spread
	spread := qsell.Bid - qbuy.Ask
	mid := qbuy.Mid()
	if mid <= 0 {
		return false
	}
	currentBps := spread / mid * 10000
	// 双阈值：max(2bps 绝对, 20% 相对)
	absThreshold := 2.0
	relThreshold := math.Abs(originalGrossBps) * 0.2
	threshold := math.Max(absThreshold, relThreshold)
	degradation := originalGrossBps - currentBps
	return degradation <= threshold
}
