// mt5_executor_fee.go —— D-599 S1：MT5 成交后 commission 回查。
// 订单响应通常不携带 commission（费在 deal 上）——成交后 OrderHistory 按 ticket
// 回查，deal 级（DealInternalIn/Out/PartialCloseDeals 的 Commission+Fee）求和；
// deal 行缺席回落订单级 ord.Commission。查不到/查询败=honest 0（调用方惯例：
// 0→NULL 不冒充无费）。沿用 Fill.Commission 通道（负值=成本口径，persist 层
// 既有 r.Commission<0→fee=-r.Commission 处理不变）。
package exec

import (
	"context"
	"log"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc/metadata"
)

// orderHistoryCommission 按 ticket 回查 MT5 历史单的 commission。
// outLeg=false=开仓腿只计 in-deal（入场费）；true=平仓腿只计 out/partial-close
// deal（平仓费）——同 ticket 历史单含 in+out 全量，不分侧会双计入场费。
// S0 探针实证（3001157/XAUUSDm）：OrderHistory 订单带 DealInternalIn/Out 行，
// deal 级 Commission/Fee 字段可读（该户实值 0=零佣账户）；deal 行缺席时
// 开仓腿回落订单级 ord.Commission，平仓腿无独立数据源 → 诚实 0。
func (e *MT5Executor) orderHistoryCommission(ctx context.Context, ticket int64, outLeg bool) float64 {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", e.token)
	resp, err := e.mt5.OrderHistory(ctx, &mt5pb.OrderHistoryRequest{
		Id:   e.token,
		From: time.Now().Add(-24 * time.Hour).Format("2006-01-02T15:04:05"),
		To:   time.Now().Format("2006-01-02T15:04:05"),
	})
	if err != nil {
		log.Printf("[exec] mt5 fee history query ticket=%d venue=%s: %v", ticket, e.venue, err)
		return 0
	}
	if resp.GetError() != nil {
		log.Printf("[exec] mt5 fee history reply ticket=%d venue=%s: %s", ticket, e.venue, resp.GetError().GetMessage())
		return 0
	}
	for _, ord := range resp.GetResult() {
		if ord.GetTicket() != ticket {
			continue
		}
		comm := 0.0
		if outLeg {
			if d := ord.GetDealInternalOut(); d != nil {
				comm += d.GetCommission() + d.GetFee()
			}
			for _, d := range ord.GetPartialCloseDeals() {
				comm += d.GetCommission() + d.GetFee()
			}
			return comm
		}
		if d := ord.GetDealInternalIn(); d != nil {
			return d.GetCommission() + d.GetFee()
		}
		return ord.GetCommission() // deal 行缺席 → 订单级兜底
	}
	return 0 // 未命中（历史窗滞后/票据不符）→ 诚实 0
}
