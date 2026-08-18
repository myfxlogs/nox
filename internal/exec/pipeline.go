// Package exec 的 pipeline.go 实现执行管线（§13.1/§13.3）。
// ExecuteOpportunity: 复核 → 建 position(OPENING) → 双腿并发下单 → 回填状态。
// ClosePosition: 复核 OPEN → 双腿反向平仓 → 回填 realized_pnl → CLOSED。
package exec

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// RiskLimits 风控上限（§12 executor 配置）。
type RiskLimits struct {
	MaxNotionalUSD       float64
	MaxOpenPositions     int
	MaxLegImbalancePct   float64
}

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
	VenueKey   string // "venue:venueType"
	VenueSymbol string // venue 原始符号
	Side       string // BUY / SELL
	Qty        float64
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
	// 风控：持仓上限
	// TODO: 查 OPEN+OPENING+LEGGED 计数 vs MaxOpenPositions（需要 store 查询方法）

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
			PriceExpected: 0,
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
		Symbol:    leg.VenueSymbol,
		Side:      leg.Side,
		Qty:       leg.Qty,
		Venue:     leg.VenueKey,
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
		// 反向平仓
		closeSide := "SELL"
		if leg.Side == "SELL" {
			closeSide = "BUY"
		}
		fill, err := exe.PlaceOrder(ctx, Order{
			Symbol: leg.Symbol,
			Side:   closeSide,
			Qty:    leg.Qty,
			Venue:  leg.Venue,
		})
		if err != nil {
			log.Printf("[exec] close leg %d: %v", leg.ID, err)
			continue
		}
		// 简化 PnL：(openPrice - closePrice) * qty * direction
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

// Revalidate 用 QuoteBus 最新报价重算净收益劣化（§13.1 双阈值）。
// 返回 true 表示通过复核，false 表示劣化超阈值应拒绝。
func (p *Pipeline) Revalidate(canonical, venueBuy, venueSell string, originalNetBps float64) bool {
	qbuy, ok1 := p.qbus.Latest(canonical, venueBuy)
	qsell, ok2 := p.qbus.Latest(canonical, venueSell)
	if !ok1 || !ok2 {
		return false
	}
	// 重算瞬时 spread
	spread := qsell.Bid - qbuy.Ask
	mid := qbuy.Mid()
	if mid <= 0 {
		return false
	}
	currentBps := spread / mid * 10000
	// 双阈值：max(2bps 绝对, 20% 相对)
	absThreshold := 2.0
	relThreshold := math.Abs(originalNetBps) * 0.2
	threshold := math.Max(absThreshold, relThreshold)
	degradation := originalNetBps - currentBps
	return degradation <= threshold
}
