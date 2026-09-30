package reconcile

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/adapter"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
	"arb/internal/venue"

	"google.golang.org/grpc/metadata"
)

// Store 对账器所需的 store 方法子集（D-073+ 接口化，便于单测 fakeStore）。
// pgstore.Store 实现该接口（结构性 typing，无需显式声明）。
type Store interface {
	ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	CreatePositionLeg(ctx context.Context, leg *pgstore.PositionLeg) error
	// D-599 S2：realizedPnl=净额；feesUSD/feeQuality 随写（nil+""=无费信息 NULL）。
	ClosePosition(ctx context.Context, id int64, realizedPnl float64, feesUSD *float64, feeQuality string) error
	UpdatePositionStatus(ctx context.Context, id int64, status string) error
	InsertAlert(ctx context.Context, ruleID *int64, level, msg string) error
	// D-245：平仓触发源持久化（三入口调用侧；写失败只 log 不阻断）。
	InsertAutotradeLog(ctx context.Context, l *pgstore.AutotradeLog) error
	// D-159 W2a R3-9：settlement 失败标记 settle_failed 到 position.note（可重试状态）。
	SetPositionNote(ctx context.Context, id int64, note string) error
	// D-159 W3a R4：auto_close 兜底分支补写 swap/commission cashflows（per-leg 幂等键带 LegNo）。
	InsertCashflow(ctx context.Context, posID int64, legNo int16, kind, venue string, amount float64, ts time.Time) error
	// D-421 §1.5：UNKNOWN 消解读写面。
	ListUnknownLegs(ctx context.Context, limit int) ([]pgstore.PositionLeg, error)
	ResolveLegFill(ctx context.Context, legID int64, price, qty, feeUSD float64) error
	ResolveLegFail(ctx context.Context, legID int64) error
	SetPositionUncertainty(ctx context.Context, id int64, reason string) error
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
	// Holdings 币种明细（D-273 S2：CEX 现货采集器填充，qty>0； earn 持币判断与余额展开行的数据源）。
	// CFD 账户无此概念，恒 nil。
	Holdings []*v1.Holding
	// BufferMultiple D-405 S2：CEX「距强平缓冲倍数」=equity/MMR，写时归一
	// （binance=marginBalance/maintMargin；okx=mgnRatio/100）。<=0=未采（MT/spot 恒 0）。
	BufferMultiple float64
}

// AccountQuerier 查询单个账户资金的统一接口（D-053）。与 venue.Querier 并列，同用 VenueKey 身份。
// 查询失败 → online=false（monitor 保留最近快照，复用 D-052 离线语义）。
type AccountQuerier interface {
	Account(ctx context.Context) (*AccountInfo, error)
	VenueKey() string
}

// isARBOrder 判断 broker 订单是否为当前 ARB 系统开出。
// 当前 exec 下单 comment 统一为 exec.OrderComment（"arb"）→ 前缀匹配即 ARB 源。
// 其余一律系统外单：遗留 ca_/cx_/tri_+hex 格式（旧系统 D-033）、手工单 UUID、空 comment。
func isARBOrder(comment string) bool {
	return strings.HasPrefix(comment, exec.OrderComment)
}

// MT5Querier 通过 MT5 gRPC 查询持仓。
// ServerTZQuerier D-337 S2：MT5 venue server 时区查询可选实现（MT4/CEX 不实现 → swap 倒计时诚实空态 nil）。
type ServerTZQuerier interface {
	ServerTimezone(ctx context.Context) (offsetHours float64, err error)
}

type MT5Querier struct {
	token string
	mt5   mt5pb.MT5Client
	venue string
}

func NewMT5Querier(venue, token string, mt5Client mt5pb.MT5Client) *MT5Querier {
	return &MT5Querier{venue: venue, token: token, mt5: mt5Client}
}

func (q *MT5Querier) VenueKey() string { return keydomain.Join(q.venue, "mt5") }

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

// ServerTimezone D-337 S2：server 时区偏移（小时，含 DST 在效偏移）。镜像 QueryOpenedOrders 的会话透传。
func (q *MT5Querier) ServerTimezone(ctx context.Context) (float64, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt5.ServerTimezone(ctx, &mt5pb.ServerTimezoneRequest{Id: q.token})
	if err != nil {
		return 0, fmt.Errorf("mt5 server timezone: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return 0, fmt.Errorf("mt5 server timezone: %v", e)
	}
	return resp.GetResult(), nil
}

func (q *MT5Querier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt5.OpenedOrders(ctx, &mt5pb.OpenedOrdersRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt5 opened orders: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt5 opened orders: %s", e.GetMessage())
	}
	return adapter.MT5OrdersToOrders(q.venue, resp.GetResult()), nil
}
