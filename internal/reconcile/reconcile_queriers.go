package reconcile

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// VenueQuerier 查询 venue 实际持仓的统一接口。
// VenueKey 提供查询器身份（"{venue}:{type}"）：离线跟踪需要 0 持仓成功时也知道 venue（D-052）。
type VenueQuerier interface {
	QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error)
	VenueKey() string // "{venue}:{venueType}"
}

// Store 对账器所需的 store 方法子集（D-073+ 接口化，便于单测 fakeStore）。
// pgstore.Store 实现该接口（结构性 typing，无需显式声明）。
type Store interface {
	ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	CreatePositionLeg(ctx context.Context, leg *pgstore.PositionLeg) error
	ClosePosition(ctx context.Context, id int64, realizedPnl float64) error
	UpdatePositionStatus(ctx context.Context, id int64, status string) error
	InsertAlert(ctx context.Context, posID *int64, level, msg string) error
	// D-159 W2a R3-9：settlement 失败标记 settle_failed 到 position.note（可重试状态）。
	SetPositionNote(ctx context.Context, id int64, note string) error
}

// VenueOrder venue 侧的持仓记录。
// Comment 用于判别订单归属：ARB 源订单（exec.OrderComment 前缀）vs 系统外单。
type VenueOrder struct {
	Ticket    string
	Symbol    string
	Side      string // BUY / SELL
	Volume    float64
	OpenPrice float64
	Venue     string
	Comment   string

	// WP3（D-051）加性扩展：对外视图（ListBrokerPositions）需要的权威字段，对账器原有逻辑不动。
	VenueKey   string  // "{venue}:{venueType}"（query 时已知类型）
	Profit     float64 // venue 权威浮盈（Order.Profit）
	Swap       float64
	Commission float64
	OpenedAt   time.Time // 开仓时间（零值 = venue 未返回）
	Magic      int64     // MT5 ExpertId / MT4 MagicNumber（0 = 无，D-052）

	// D-146 S5 微件③：CEX perp per-position margin（venue 权威，替换 0=未知语义）。
	// binance positionRisk initialMargin / okx positions margin；MT4/MT5=0（走 D-124 账户分摊）。
	InitialMargin float64 // 初始保证金（占用保证金，USD/USDT）
	MaintMargin   float64 // 维持保证金（USD/USDT）
	// D-155：broker 端止损 SL 值（MT5 Order.StopLoss / MT4 Order.StopLoss；CEX=0 无概念）。
	// 透出到 BrokerPosition.sl 供 D-146 S1 灾难 SL 生产验证闭环。
	StopLoss float64
}

// AccountInfo 一个账户的资金视图（D-053）。CFD 来自 MT AccountSummary；CEX 来自签名余额查询。
// CEX 的 Margin/FreeMargin/Credit/Profit = 0（无对应概念，前端按 AssetType=cex 置「—」）。
type AccountInfo struct {
	VenueKey   string // "{venue}:{venueType}"
	Venue      string
	VenueType  string // mt5 / mt4 / spot
	AssetType  string // cfd / cex
	Balance    float64
	Equity     float64
	Margin     float64
	FreeMargin float64
	Credit     float64
	Profit     float64
	Leverage   float64
	Currency   string
	Simulated  bool   // CEX 模拟盘标记（D-156 V2：CEXAccountQuerier 填充 q.simulated；CFD 零值 false）
	Website    string // D-157 V3：官网（CEX 从 ExchangeConfig.website 透传；CFD 从 BrokerAccount.website 透传）
}

// AccountQuerier 查询单个账户资金的统一接口（D-053）。与 VenueQuerier 并列，同用 VenueKey 身份。
// 查询失败 → online=false（monitor 保留最近快照，复用 D-052 离线语义）。
type AccountQuerier interface {
	Account(ctx context.Context) (*AccountInfo, error)
	VenueKey() string
}

// accountSummaryToInfo 从 MT5 AccountSummary 映射账户视图（D-053）。
func accountSummaryToInfoMT5(venue, venueType string, sum *mt5pb.AccountSummary) *AccountInfo {
	return &AccountInfo{
		VenueKey:   venue + ":" + venueType,
		Venue:      venue,
		VenueType:  venueType,
		AssetType:  "cfd",
		Balance:    sum.GetBalance(),
		Credit:     sum.GetCredit(),
		Profit:     sum.GetProfit(),
		Equity:     sum.GetEquity(),
		Margin:     sum.GetMargin(),
		FreeMargin: sum.GetFreeMargin(),
		Leverage:   sum.GetLeverage(),
		Currency:   sum.GetCurrency(),
	}
}

// accountSummaryToInfoMT4 从 MT4 AccountSummary 映射账户视图（字段与 MT5 同形，生成类型不同）。
func accountSummaryToInfoMT4(venue, venueType string, sum *mt4.AccountSummary) *AccountInfo {
	return &AccountInfo{
		VenueKey:   venue + ":" + venueType,
		Venue:      venue,
		VenueType:  venueType,
		AssetType:  "cfd",
		Balance:    sum.GetBalance(),
		Credit:     sum.GetCredit(),
		Profit:     sum.GetProfit(),
		Equity:     sum.GetEquity(),
		Margin:     sum.GetMargin(),
		FreeMargin: sum.GetFreeMargin(),
		Leverage:   sum.GetLeverage(),
		Currency:   sum.GetCurrency(),
	}
}

// isARBOrder 判断 broker 订单是否为当前 ARB 系统开出。
// 当前 exec 下单 comment 统一为 exec.OrderComment（"arb"）→ 前缀匹配即 ARB 源。
// 其余一律系统外单：遗留 ca_/cx_/tri_+hex 格式（旧系统 D-033）、手工单 UUID、空 comment。
func isARBOrder(comment string) bool {
	return strings.HasPrefix(comment, exec.OrderComment)
}

// tsToTime nil-safe 转换：nil Timestamp → time.Time{}（开仓时间零值 = venue 未返回）。
func tsToTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// MT4Querier 通过 MT4 gRPC 查询持仓。
type MT4Querier struct {
	token string
	mt4   mt4.MT4Client
	venue string
}

func NewMT4Querier(venue, token string, mt4Client mt4.MT4Client) *MT4Querier {
	return &MT4Querier{venue: venue, token: token, mt4: mt4Client}
}

func (q *MT4Querier) VenueKey() string { return q.venue + ":mt4" }

// Account 查 MT4 账户资金（AccountSummary，D-053）。
func (q *MT4Querier) Account(ctx context.Context) (*AccountInfo, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt4.AccountSummary(ctx, &mt4.AccountSummaryRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt4 account summary: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt4 account summary: %s", e.GetMessage())
	}
	return accountSummaryToInfoMT4(q.venue, "mt4", resp.GetResult()), nil
}

func (q *MT4Querier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt4.OpenedOrders(ctx, &mt4.OpenedOrdersRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt4 opened orders: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt4 opened orders: %s", e.GetMessage())
	}
	var out []VenueOrder
	for _, ord := range resp.GetResult() {
		side := "BUY"
		if ord.GetType() == mt4.Op_Op_Sell {
			side = "SELL"
		}
		out = append(out, VenueOrder{
			Ticket:     strconv.FormatInt(int64(ord.GetTicket()), 10),
			Symbol:     ord.GetSymbol(),
			Side:       side,
			Volume:     ord.GetLots(),
			OpenPrice:  ord.GetOpenPrice(),
			Venue:      q.venue,
			Comment:    ord.GetComment(),
			VenueKey:   q.venue + ":mt4",
			Profit:     ord.GetProfit(),
			Swap:       ord.GetSwap(),
			Commission: ord.GetCommission(),
			OpenedAt:   tsToTime(ord.GetOpenTime()),
			Magic:      int64(ord.GetMagicNumber()),
			StopLoss:   ord.GetStopLoss(), // D-155：MT4 Order.StopLoss 透传
		})
	}
	return out, nil
}

// MT5Querier 通过 MT5 gRPC 查询持仓。
type MT5Querier struct {
	token string
	mt5   mt5pb.MT5Client
	venue string
}

func NewMT5Querier(venue, token string, mt5Client mt5pb.MT5Client) *MT5Querier {
	return &MT5Querier{venue: venue, token: token, mt5: mt5Client}
}

func (q *MT5Querier) VenueKey() string { return q.venue + ":mt5" }

// Account 查 MT5 账户资金（AccountSummary，D-053）。
func (q *MT5Querier) Account(ctx context.Context) (*AccountInfo, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt5.AccountSummary(ctx, &mt5pb.AccountSummaryRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt5 account summary: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt5 account summary: %s", e.GetMessage())
	}
	return accountSummaryToInfoMT5(q.venue, "mt5", resp.GetResult()), nil
}

func (q *MT5Querier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt5.OpenedOrders(ctx, &mt5pb.OpenedOrdersRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt5 opened orders: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt5 opened orders: %s", e.GetMessage())
	}
	var out []VenueOrder
	for _, ord := range resp.GetResult() {
		side := "BUY"
		if ord.GetOrderType() == mt5pb.OrderType_OrderType_Sell {
			side = "SELL"
		}
		out = append(out, VenueOrder{
			Ticket:     strconv.FormatInt(ord.GetTicket(), 10),
			Symbol:     ord.GetSymbol(),
			Side:       side,
			Volume:     ord.GetLots(),
			OpenPrice:  ord.GetOpenPrice(),
			Venue:      q.venue,
			Comment:    ord.GetComment(),
			VenueKey:   q.venue + ":mt5",
			Profit:     ord.GetProfit(),
			Swap:       ord.GetSwap(),
			Commission: ord.GetCommission(),
			OpenedAt:   tsToTime(ord.GetOpenTime()),
			Magic:      ord.GetExpertId(),
			StopLoss:   ord.GetStopLoss(), // D-155：MT5 Order.StopLoss 透传
		})
	}
	return out, nil
}
