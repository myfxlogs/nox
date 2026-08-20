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
}

// NewPipeline 创建执行管线。
func NewPipeline(reg *Registry, store *pgstore.Store, qbus *bus.QuoteBus, limits RiskLimits) *Pipeline {
	return &Pipeline{registry: reg, store: store, qbus: qbus, limits: limits}
}

// LegSpec 是单腿下单规格。
type LegSpec struct {
	VenueKey       string // "venue:venueType"
	VenueSymbol    string // venue 原生符号
	Side           string // BUY / SELL
	Qty            float64
	PriceExpected  float64 // 下单时本腿可执行价（买=ask 卖=bid，D-060 实施修正②：复盘 deviation 口径需要）
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
	_ = p.store.UpdatePositionStatus(ctx, posID, status)

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

	_ = p.store.UpdatePositionStatus(ctx, posID, "CLOSING")

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
		// 简化 PnL：(closePrice - openPrice) * qty * direction
		dir := 1.0
		if leg.Side == "SELL" {
			dir = -1.0
		}
		if leg.PriceActual != nil {
			totalPnL += (fill.Price - *leg.PriceActual) * leg.Qty * dir
		}
		// swap 入 cashflows
		if fill.Swap != 0 {
			_ = p.store.InsertCashflow(ctx, posID, "swap", leg.Venue, fill.Swap, time.Now())
		}
	}

	_ = p.store.ClosePosition(ctx, posID, totalPnL)
	return nil
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
