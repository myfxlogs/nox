// Package reconcile 对账自动收尾（D-073+）：OrderHistory 查证接口 + MT5 实现。
package reconcile

import (
	"context"
	"fmt"
	"strconv"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc/metadata"
)

// HistoryOrder 历史单查证结果（已平仓订单的权威字段）。
// ClosePrice=0/Profit=0 = venue 未返回（如实空，不伪造）。
type HistoryOrder struct {
	Ticket     string
	OpenPrice  float64 // D-427 I1：开仓价（消解开仓 UNKNOWN 腿回填用；0=venue 未返回）
	ClosePrice float64
	Profit     float64
	Swap       float64
	Commission float64
	Symbol     string
}

// OrderHistoryQuerier 按 ticket 查证 venue 历史单（对账自动收尾用，D-073+）。
// VenueKey 与 VenueQuerier 同源（"{venue}:{type}"），对账器按 venueKey 匹配查证器。
type OrderHistoryQuerier interface {
	// QueryOrderHistory 查近 fromDays 天历史单，按 ticket 过滤返回；ticket 未找到 = 不在结果中。
	QueryOrderHistory(ctx context.Context, ticket string, fromDays int) (*HistoryOrder, error)
	VenueKey() string
}

// MT5OrderHistoryQuerier MT5 历史单查证器。
type MT5OrderHistoryQuerier struct {
	token string
	mt5   mt5pb.MT5Client
	venue string
}

func NewMT5OrderHistoryQuerier(venue, token string, mt5Client mt5pb.MT5Client) *MT5OrderHistoryQuerier {
	return &MT5OrderHistoryQuerier{token: token, mt5: mt5Client, venue: venue}
}

func (q *MT5OrderHistoryQuerier) VenueKey() string { return q.venue + ":mt5" }

func (q *MT5OrderHistoryQuerier) QueryOrderHistory(ctx context.Context, ticket string, fromDays int) (*HistoryOrder, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	from := time.Now().Add(-time.Duration(fromDays) * 24 * time.Hour).Format("2006-01-02T15:04:05")
	to := time.Now().Format("2006-01-02T15:04:05")
	resp, err := q.mt5.OrderHistory(ctx, &mt5pb.OrderHistoryRequest{
		Id:        q.token,
		From:      from,
		To:        to,
		Ascending: boolPtr(false),
	})
	if err != nil {
		return nil, fmt.Errorf("mt5 order history: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt5 order history: %s", e.GetMessage())
	}
	for _, ord := range resp.GetResult() {
		if strconv.FormatInt(ord.GetTicket(), 10) == ticket {
			return &HistoryOrder{
				Ticket:     ticket,
				OpenPrice:  ord.GetOpenPrice(),
				ClosePrice: ord.GetClosePrice(),
				Profit:     ord.GetProfit(),
				Swap:       ord.GetSwap(),
				Commission: ord.GetCommission(),
				Symbol:     ord.GetSymbol(),
			}, nil
		}
	}
	return nil, nil // 未命中
}

func boolPtr(b bool) *bool { return &b }
