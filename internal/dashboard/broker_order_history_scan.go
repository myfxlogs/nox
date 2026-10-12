// D-181 S1：BackfillSwapHistory RPC——手动回溯 venue swap 历史→facts（src=order_history）。
// 复用 BrokerManager 已连接的 MT5 adapter，通过 running.orderHistoryScan 闭包拉历史单。
// RPC handler 在 service_backfill.go（拆分保 ≤300 行）。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/fact"

	"google.golang.org/grpc/metadata"
)

// HistorySwapOrder 已平仓订单的 swap 相关字段（D-181 S1 回溯用 + D-193-A 扩包 S6 对拍用）。
// CloseTime 为零值 = 未平仓（跳过）；Swap=0 = 无结算（跳过）；Lots<=0 = 异常（跳过）。
// D-193-A 扩包 S6 R1：扩 Commission/Profit/OrderTicket/ClosePrice（对拍 cashflows 用，proto 全在 mt5.pb.go:3977-4040）。
type HistorySwapOrder struct {
	Symbol      string
	Side        string // "BUY"/"SELL"
	Swap        float64
	Lots        float64
	CloseLots   float64 // MT5 部分平仓实际量；MT4 无此字段=Lots
	OpenTime    time.Time
	CloseTime   time.Time
	Commission  float64 // D-193-A S6：对拍 cashflows.commission
	Profit      float64 // D-193-A S6：对拍 realized_pnl（deal profit）
	OrderTicket int64   // D-193-A S6：external_event_id 源
	ClosePrice  float64 // D-193-A S6：raw_amount 换算用
}

// orderHistoryScan 闭包类型：拉指定时间窗内的已平仓订单（D-181 S1）。
// D-418：MT5 改走 OrderHistory 全量（OrderHistoryPagination 网关实测 Unimplemented，
// probe 实证 OrderHistory 可用；数据量放大再议分页）。MT4 走 OrderHistory 全量。
// 返回所有符合时间窗的已平仓订单（含 Swap=0 的，调用方过滤）。
type orderHistoryScan func(ctx context.Context, from, to time.Time) ([]HistorySwapOrder, error)

// mt5OrderHistoryScan 构造 MT5 全量扫描闭包（D-181 S1 → D-418 哑管复活）。
// OrderHistory 非分页一次拉窗口全量（probe 实测窗口内 total=6，无分页压力）。
// 时间戳字段 OpenTimestampUTC/CloseTimestampUTC=int64 毫秒（KB mt5-order-history-timestamps-millis），
// 非 pagination 记录里的 timestamppb CloseTime/OpenTime。
func mt5OrderHistoryScan(client mt5pb.MT5Client, token string) orderHistoryScan {
	return func(ctx context.Context, from, to time.Time) ([]HistorySwapOrder, error) {
		ctx = metadata.AppendToOutgoingContext(ctx, "id", token)
		ascending := false
		resp, err := client.OrderHistory(ctx, &mt5pb.OrderHistoryRequest{
			Id:        token,
			From:      from.Format("2006-01-02T15:04:05"),
			To:        to.Format("2006-01-02T15:04:05"),
			Ascending: &ascending,
		})
		if err != nil {
			return nil, fmt.Errorf("mt5 order history: %w", err)
		}
		if e := resp.GetError(); e != nil {
			return nil, fmt.Errorf("mt5 order history: %s", e.GetMessage())
		}
		var all []HistorySwapOrder
		for _, ord := range resp.GetResult() {
			// D-427 I4：OrderType 白名单 fail-closed（Balance/Credit/未知枚举跳过）。
			switch ord.GetOrderType() {
			case mt5pb.OrderType_OrderType_Buy, mt5pb.OrderType_OrderType_Sell,
				mt5pb.OrderType_OrderType_BuyLimit, mt5pb.OrderType_OrderType_SellLimit,
				mt5pb.OrderType_OrderType_BuyStop, mt5pb.OrderType_OrderType_SellStop,
				mt5pb.OrderType_OrderType_BuyStopLimit, mt5pb.OrderType_OrderType_SellStopLimit,
				mt5pb.OrderType_OrderType_CloseBy:
			default:
				continue
			}
			// 主路径 int64 毫秒（KB 实证）；兜底 timestamppb（网关若混填两型不误杀）。
			closeT, openT := time.Time{}, time.Time{}
			if ms := ord.GetCloseTimestampUTC(); ms != 0 {
				closeT = time.UnixMilli(ms).UTC()
			} else if ts := ord.GetCloseTime(); ts != nil {
				closeT = ts.AsTime()
			}
			if closeT.IsZero() {
				continue // 未平仓
			}
			if ms := ord.GetOpenTimestampUTC(); ms != 0 {
				openT = time.UnixMilli(ms).UTC()
			} else if ts := ord.GetOpenTime(); ts != nil {
				openT = ts.AsTime()
			}
			if openT.IsZero() {
				continue
			}
			side := "BUY"
			if ord.GetOrderType() == mt5pb.OrderType_OrderType_Sell {
				side = "SELL"
			}
			closeLots := ord.GetCloseLots()
			if closeLots <= 0 {
				closeLots = ord.GetLots() // 回退全量手数
			}
			all = append(all, HistorySwapOrder{
				Symbol:      ord.GetSymbol(),
				Side:        side,
				Swap:        ord.GetSwap(),
				Lots:        ord.GetLots(),
				CloseLots:   closeLots,
				OpenTime:    openT,
				CloseTime:   closeT,
				Commission:  ord.GetCommission(),
				Profit:      ord.GetProfit(),
				OrderTicket: ord.GetTicket(),
				ClosePrice:  ord.GetClosePrice(),
			})
		}
		return all, nil
	}
}

// computeUsdLotDay D-181 S1：单订单 usdLotDay 换算。
// usdLotDay = Swap / (closeLots × holdDays)；holdDays < 1 → 返回 ok=false（日内单跳过）。
func computeUsdLotDay(o HistorySwapOrder) (float64, bool) {
	holdDays := o.CloseTime.Sub(o.OpenTime).Hours() / 24
	if holdDays < 1 {
		return 0, false
	}
	lots := o.CloseLots
	if lots <= 0 {
		lots = o.Lots
	}
	if lots <= 0 {
		return 0, false
	}
	return o.Swap / (lots * holdDays), true
}

// aggregateHistoryToUsdLotDay D-181 S1：按 (symbol, side) 聚合 usdLotDay 值取中位数 → facts。
// 输入：已过滤的 HistorySwapOrder 列表（Swap≠0 && holdDays≥1）。
// 输出：swap_settled_long/short facts，src=order_history，ts=now。
func aggregateHistoryToUsdLotDay(orders []HistorySwapOrder, venue string, now time.Time) []fact.Fact {
	type groupKey struct {
		symbol string
		side   string
	}
	groups := make(map[groupKey][]float64)
	for _, o := range orders {
		v, ok := computeUsdLotDay(o)
		if !ok {
			continue
		}
		k := groupKey{symbol: o.Symbol, side: o.Side}
		groups[k] = append(groups[k], v)
	}
	var facts []fact.Fact
	for k, vals := range groups {
		med := medianFloats(vals)
		kind := fact.KindSwapSettledLong
		if k.side != "BUY" {
			kind = fact.KindSwapSettledShort
		}
		facts = append(facts, fact.Fact{
			Kind:   kind,
			Venue:  venue,
			Symbol: k.symbol,
			Value:  med,
			Unit:   fact.UnitUsdLotDay,
			Ts:     now,
			Src:    "order_history",
		})
	}
	return facts
}

// medianFloats D-181 S1：浮点切片中位数（复用 swap_settlement_profile.go median 但独立避免依赖）。
func medianFloats(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
