// cex_backfill_gate.go —— D-577 S2/S3：CEX 平仓「超时但成交」历史回填。
// 首选源=cex_private_events 已持久化 WS 帧（usertrades+position_closes）；
// miss→GateTradeHistory REST 兜底。MT5 ticket 语义不可平移（开/平不同 order_id），
// 按 contract+反向 side+ClosingStartedAt 窗聚合，聚合量闸 fail-closed 防半成交污染。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// GateFill gate futures usertrade/my_trades 行（WS 帧与 REST 同形子集）。
// size 带符号（买正卖负）；price/fee 实弹为 JSON 字符串。
type GateFill struct {
	CreateTime float64 `json:"create_time"`
	Contract   string  `json:"contract"`
	Size       float64 `json:"size"`
	Price      string  `json:"price"`
	Fee        string  `json:"fee"`
}

// GatePosClose gate position_close/position_closes 行（WS 帧与 REST 同形子集）。
type GatePosClose struct {
	Time     float64 `json:"time"`
	Contract string  `json:"contract"`
	Side     string  `json:"side"`
	PnL      string  `json:"pnl"`
}

// cexCloseFill CEX 腿历史回填编排（D-577）：WS 落库帧首选 → miss → GateTradeHistory REST 兜底。
// 返 (nil,nil)=双源皆无命中（调用方走 fail-closed 出口）；聚合量污染=error。
func (p *Pipeline) cexCloseFill(ctx context.Context, leg pgstore.PositionLeg, since time.Time) (*HistoryPnL, error) {
	exe, ok := p.registry.Get(leg.Venue)
	if !ok {
		return nil, fmt.Errorf("no executor for venue %s", leg.Venue)
	}
	ce, ok := exe.(*CEXExecutor)
	if !ok {
		return nil, fmt.Errorf("venue %s executor 非 CEXExecutor（CEX 回填不适用）", leg.Venue)
	}
	// 聚合量须按 quanto 折基础币比对 leg.EffectiveQty——spec 读败 fail-closed（不猜换算）。
	spec, err := ce.gateSpecFetch(ctx, leg.Symbol)
	if err != nil {
		return nil, fmt.Errorf("gate contract spec for backfill: %w", err)
	}
	hp, err := p.cexCloseFillFromEvents(ctx, leg, since, spec.Quanto)
	if err != nil || hp != nil {
		return hp, err
	}
	th, ok := exe.(GateTradeHistory)
	if !ok {
		return nil, nil
	}
	fills, err := th.QueryMyTrades(ctx, leg.Symbol, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("gate my_trades backfill: %w", err)
	}
	hp, err = cexAggregateCloseFills(leg, fills, spec.Quanto)
	if err != nil || hp == nil {
		return hp, err
	}
	if pc, perr := th.QueryPositionClose(ctx, leg.Symbol); perr == nil && pc != nil {
		if pnl, perr := strconv.ParseFloat(pc.PnL, 64); perr == nil {
			hp.Profit = pnl
		}
	}
	return hp, nil
}

// cexCloseFillFromEvents 从 cex_private_events 聚合 CEX 平仓成交（首选源）：
// usertrades 帧（contract=leg.Symbol、平仓方向=leg.Side 反向、received_at>=since）
// VWAP→ClosePrice、fee 合计→Commission；position_closes 帧最新一条 pnl→Profit。
// 零命中返 (nil,nil)；聚合量偏差>1% 视污染返 error（fail-closed 宁缺毋滥）。
func (p *Pipeline) cexCloseFillFromEvents(ctx context.Context, leg pgstore.PositionLeg, since time.Time, quanto float64) (*HistoryPnL, error) {
	rows, err := p.store.ListCexPrivateEventsSince(ctx,
		keydomain.BareOf(leg.Venue), keydomain.TypeOf(leg.Venue), "futures.usertrades", since)
	if err != nil {
		return nil, fmt.Errorf("cex events usertrades: %w", err)
	}
	var fills []GateFill
	for _, ev := range rows {
		var items []GateFill
		if json.Unmarshal(ev.Payload, &items) == nil {
			fills = append(fills, items...)
		}
	}
	hp, err := cexAggregateCloseFills(leg, fills, quanto)
	if err != nil || hp == nil {
		return hp, err
	}
	if pnl, ok := p.cexPositionClosePnL(ctx, leg, since); ok {
		hp.Profit = pnl
	}
	return hp, nil
}

// cexPositionClosePnL position_closes 帧内最新一条 contract 匹配的 pnl。
func (p *Pipeline) cexPositionClosePnL(ctx context.Context, leg pgstore.PositionLeg, since time.Time) (float64, bool) {
	rows, err := p.store.ListCexPrivateEventsSince(ctx,
		keydomain.BareOf(leg.Venue), keydomain.TypeOf(leg.Venue), "futures.position_closes", since)
	if err != nil {
		return 0, false
	}
	var closes []GatePosClose
	for _, ev := range rows {
		var items []GatePosClose
		if json.Unmarshal(ev.Payload, &items) == nil {
			closes = append(closes, items...)
		}
	}
	var best *GatePosClose
	for i := range closes {
		if closes[i].Contract != leg.Symbol {
			continue
		}
		if best == nil || closes[i].Time > best.Time {
			best = &closes[i]
		}
	}
	if best == nil {
		return 0, false
	}
	pnl, err := strconv.ParseFloat(best.PnL, 64)
	return pnl, err == nil
}

// cexAggregateCloseFills 同口径聚合：contract+平仓方向（开仓反向）过滤 →
// VWAP→ClosePrice、fee 合计→Commission；合约张数×quanto 折基础币后与
// leg.EffectiveQty 比对（≤1% 容差），超差=error 防半成交污染。
func cexAggregateCloseFills(leg pgstore.PositionLeg, fills []GateFill, quanto float64) (*HistoryPnL, error) {
	var qtyC, pv, fee float64
	for _, f := range fills {
		if f.Contract != leg.Symbol {
			continue
		}
		// SELL 腿（空仓）平仓=BUY=正 size；BUY 腿平仓=负 size。
		if leg.Side == "SELL" {
			if f.Size <= 0 {
				continue
			}
		} else if f.Size >= 0 {
			continue
		}
		price, _ := strconv.ParseFloat(f.Price, 64)
		if price <= 0 {
			continue
		}
		abs := math.Abs(f.Size)
		qtyC += abs
		pv += abs * price
		c, _ := strconv.ParseFloat(f.Fee, 64)
		fee += c
	}
	if qtyC <= 0 {
		return nil, nil
	}
	eff := leg.EffectiveQty()
	baseQty := qtyC * quanto
	if eff > 0 && math.Abs(baseQty-eff) > 0.01*eff {
		return nil, fmt.Errorf("cex close fill qty %.6f vs leg qty %.6f（聚合量超差>1%%，半成交污染 fail-closed）", baseQty, eff)
	}
	return &HistoryPnL{ClosePrice: pv / qtyC, Commission: fee}, nil
}
