// Package venue 提供 venue 侧持仓/查询的共享领域类型；被 adapter / collect /
// reconcile / exec / dashboard 共用，本包不得 import 任何 internal 业务包。
package venue

import (
	"context"
	"time"
)

// Querier 查询 venue 实际持仓的统一接口。
// VenueKey 提供查询器身份（"{venue}:{type}"）：离线跟踪需要 0 持仓成功时也知道 venue（D-052）。
type Querier interface {
	QueryOpenedOrders(ctx context.Context) ([]Order, error)
	VenueKey() string // "{venue}:{venueType}"
}

// Order venue 侧的持仓记录。
// Comment 用于判别订单归属：ARB 源订单（exec 下单 comment 前缀）vs 系统外单。
type Order struct {
	Ticket    string
	Symbol    string
	Side      string // BUY / SELL
	Volume    float64
	OpenPrice float64
	Venue     string
	Comment   string
	// CEX 聚合仓 position_side（OKX long/short/net；Gate/Binance 从 size 正负推导）。
	// MT 留空（MT 按 ticket 匹配，无需 position_side）。
	PosSide string

	// WP3（D-051）加性扩展：对外视图（ListBrokerPositions）需要的权威字段，对账器原有逻辑不动。
	VenueKey   string  // "{venue}:{venueType}"（query 时已知类型）
	Profit     float64 // venue 权威浮盈（Order.Profit）
	Swap       float64
	Commission float64
	OpenedAt   time.Time // 开仓时间（零值 = venue 未返回）
	Magic      int64     // MT5 ExpertId / MT4 MagicNumber（0 = 无，D-052）

	// 微件③：CEX perp per-position margin（venue 权威，替换 0=未知语义）。
	// binance positionRisk initialMargin / okx positions margin；MT4/MT5=0（走 D-124 账户分摊）。
	InitialMargin float64 // 初始保证金（占用保证金，USD/USDT）
	MaintMargin   float64 // 维持保证金（USD/USDT）
	// broker 端止损 SL 值（MT5 Order.StopLoss / MT4 Order.StopLoss；CEX=0 无概念）。
	// 透出到 BrokerPosition.sl 供 D-146 S1 灾难 SL 生产验证闭环。
	StopLoss float64

	// 帧携现价（OnOrderProfit 帧同次发布取价，一帧一价一钟）。
	// PriceAt=0 = 无帧源（REST 回读行/报价流断）= 诚实空态；REST 路径恒零不填。
	CurrentPrice float64
	PriceAt      time.Time // 帧内报价时戳（quote tick 自身 ts）
	// 帧携双价（同 lastQuote tick 的 bid/ask，与 CurrentPrice 同帧同钟）；
	// 0=该侧价缺（诚实空态，current_price 回退 CurrentPrice 中价）。
	PriceBid float64
	PriceAsk float64
}
