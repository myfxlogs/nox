// exec.go —— D-191 R3：轮式 DCI 执行轨（binance SAPI 写径，covered-call+cash-put 双向）。
// 触发=业主「go」口令；strike 规则化 %OTM 闸（CALL≥spot×1.10/PUT≤spot×0.93，
// spot=执行时实时读）；期限上限=custody_redeemable.value_num；qty=币安实时余额
// ×custody_share（禁读 personal_positions 静态行）；无达标档→gap 如实报不降级凑单；
// 幂等=fin_dci_order.request_id UNIQUE 单次尝试不重试；失败→fin_condition_gap 请求。
package dciexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"nox/internal/store/pgstore"
)

// sign HMAC-SHA256 hex（binance sapi 同法，包内独立实现 P3 不适用）。
func sign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// Store 执行面依赖（*pgstore.Store 实现；测试走 fake）。
type Store interface {
	ListCapabilityConstraints(ctx context.Context) ([]pgstore.CapabilityConstraint, error)
	ListDCIQuotes(ctx context.Context, venue, underlying, optType, investCcy string, maxTenorDays int, strikeBound float64) ([]pgstore.CexDCIQuote, error)
	CreateDCIOrder(ctx context.Context, o *pgstore.DCIOrder) (int64, bool, error)
	RecentDCIOrderByProposal(ctx context.Context, proposalID int64, window time.Duration) (int64, error)
	ListFailedProposalOrders(ctx context.Context) ([]pgstore.FailedProposalOrder, error)
	OutstandingDCIProduct(ctx context.Context, productRef, optType string) (bool, error)
	DCIOrderLegCount(ctx context.Context, proposalID int64, optType, investCcy string) (int, error)
	DCIOrderIntentCount(ctx context.Context, reqPrefix string) (int, error)
	DCIOrderOpenIntent(ctx context.Context, reqPrefix string) (bool, error)
	FinishDCIOrder(ctx context.Context, id int64, status, response string) error
	// InsertVenueIO earn 写侧划转记账（D-596：申购/赎回 venue 返回端点采集
	// 盲区——写侧成功径自记 fin_venue_io transfer 行）。
	InsertVenueIO(ctx context.Context, rows []pgstore.VenueIO) (int64, error)
	// 交割观察面。
	ListFilledDCIOrders(ctx context.Context) ([]pgstore.FilledDCIOrder, error)
	SettleDCIOrder(ctx context.Context, id int64, ccy string, qty, income float64) (bool, error)
	ClosePersonalPositionsByMarker(ctx context.Context, marker, suffix string) (int64, error)
	ExchangeConfigID(ctx context.Context, venue, venueType string) (int64, error)
	InsertIncomeEvents(ctx context.Context, evs []pgstore.IncomeEvent) (int64, error)
	InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time, venue string) (int64, error)
	OutstandingDCIQty(ctx context.Context, venue, investCcy string) (float64, error)
	ListOpenPersonalPositions(ctx context.Context) ([]pgstore.PersonalPosition, error)
	// DCIOrderByID pending_disposal 清扫损益锚（DCI#id→原单 qty/underlying；
	// 单点读——ListDCIOrders 有 >200→clamp20 静默截断，锚旧单必用此径）。
	DCIOrderByID(ctx context.Context, id int64) (*pgstore.DCIOrder, error)
	UpdatePersonalPositionQtyNote(ctx context.Context, id int64, qty float64, note string) error
	OpenConditionGap(ctx context.Context, g *pgstore.ConditionGap) (int64, error)
	RequestConditionGap(ctx context.Context, id int64) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error
	// D-255：venue 败因坑表读/学面（tryLeg 申购前查坑、败行自动落坑）。
	ListVenueRejects(ctx context.Context, venue, op string) ([]pgstore.VenueReject, error)
	LearnVenueReject(ctx context.Context, venue, op, sig string, window time.Duration) error
	// 提案绑定面。
	ListProposals(ctx context.Context, status string, limit int) ([]pgstore.Proposal, error)
	LatestPendingProposal(ctx context.Context, templates []string) (*pgstore.Proposal, error)
	GetProposal(ctx context.Context, id int64) (*pgstore.Proposal, error)
	// PaperFirstGate/HasPaperEvidence D-593 纸面先行闸（提案→executed 唯一收口接线）。
	PaperFirstGate(ctx context.Context, templateID string, hasPaperEvidence func() (bool, error)) (bool, string, error)
	HasPaperEvidence(ctx context.Context, templateID string) (bool, error)
	SetProposalStatus(ctx context.Context, id int64, status string) error
	// D-258 M4.1：评估卡消费面——无卡/非 viable=不算候选拒单；settle 回写实绩。
	GetOppEval(ctx context.Context, proposalID int64) (*pgstore.OppEval, error)
	UpdateOppEvalActual(ctx context.Context, proposalID int64, apr float64) error
	// D-260 M4.2：生命周期态读面——自动执行闸 state=='live' fail-closed。
	GetStrategyState(ctx context.Context, strategy string) (string, bool, error)
	// D-286：earn 敞口闸读面——池快照（CEX 侧权益占比算）。
	CapitalPoolSnapshots(ctx context.Context) ([]pgstore.CapitalPoolRow, error)
	// D-526：σ 窗口期配置读面（dci_sigma_window_days）。
	AppConfigInt64(ctx context.Context, key string) (int64, bool, error)
	// D-612：轮动判据读面（rotation_* 键族）+持有窗读口（earn 账本最近活动）。
	AppConfigFloat(ctx context.Context, key string) (float64, bool, error)
	LatestEarnActivityAt(ctx context.Context, venue string) (time.Time, bool, error)
	// jit 划转执行件：回转腿 venue 读口+净额账读（jit-swp/jit-ret 差分）。
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	JitNetOutstanding(ctx context.Context, venue string) (float64, error)
	// D-647：jit 收益比对闸读面（earn 桶在册 APR）+闲置回申 armed 面。
	LatestEarnAPR(ctx context.Context, venue, ccy string) (float64, bool, error)
	JitArmedComboCount(ctx context.Context, venue string) (int, error)
}

// Executor binance DCI 申购执行器（凭据由装配侧从 exchange_configs 注入）。
type Executor struct {
	Store   Store
	BnKey   string
	BnSec   string
	Sapi    string // sapi base（申购写径）
	APIBase string // api base（余额/现货读径）
	Client  *http.Client
	Now     func() time.Time // 测试可覆
	// 测试注入面（nil=真实 HTTP 实现）：
	PriceFn            func(ctx context.Context, symbol string) (float64, error)
	BalanceFn          func(ctx context.Context, asset string) (float64, error) // 现货 free+locked
	EarnFn             func(ctx context.Context, asset string) ([]EarnPosition, error)
	RedeemFn           func(ctx context.Context, productID string, amount float64) error
	EarnProductsFn     func(ctx context.Context, ccy string) ([]EarnProduct, error)                         // earn 活期产品面（D-286；nil=实 GET——binance 径）
	EarnSubscribeFn    func(ctx context.Context, productID string, amount float64) error                    // earn 申购写径（nil=实 POST——binance 径）
	EarnCreds          map[string]EarnCred                                                                  // earn 多 venue 凭据（exchange_configs 装配注入）
	EarnRails          map[string]EarnRail                                                                  // 测试注面：venue→rail 替真发（nil=按 EarnCreds 建）
	StreamFn           func() bool                                                                          // 流存活闸（nil=dephealth 实读）
	SigmaFn            func(ctx context.Context) (float64, error)                                           // D-526：σ_daily 测试注口（nil=venue 日 K 实读）
	HoldingFn          func(ctx context.Context, productRef, optType string) (bool, error)                  // venue 持仓读（D-198 返修①；nil=实 GET）
	PositionsFn        func(ctx context.Context) ([]DCIPosition, error)                                     // D-207 venue 持仓全列表（settle 观察；nil=实 GET）
	SettledPositionsFn func(ctx context.Context) ([]DCIPosition, error)                                     // M3-④ venue SETTLED 终态行全列表（settle 死端切源；nil=实 GET）
	SpotSellFn         func(ctx context.Context, symbol string, qty float64) (float64, error)               // D-517 接货市价卖回（nil=实 POST /v3/order）
	SpotFiltersFn      func(ctx context.Context, symbol string) (step, minQty float64, prec int, err error) // nil=实 GET /v3/exchangeInfo（LOT_SIZE）
	KBDir              string                                                                               // 拒单消费闸：经验库条目目录（空=禁附行；读败 fail-open）
	// TradingFreeFn/JitTransferFn jit 测试注口（回转可动读/划转写径；nil=实件）。
	TradingFreeFn func(ctx context.Context, venue, venueType string) (float64, error)
	JitTransferFn func(ctx context.Context, venue, ccy string, amt float64, from, to string) error
	// ModeGate D-620⑤：真金单闸控制面（*gate.ControlPlane 实形；nil=提案执行拒「控制面未接线」）。
	ModeGate ModeGate
	// LiveEquityMapFn 浓度透视分母实拉钩（venue:type:ccy→equity 判定拍单次调用；
	// nil=透视段跳过非阻塞；err→warn「实拉败」剔分母——D-604 判定点读径）。
	LiveEquityMapFn func(ctx context.Context) (map[string]float64, error)
}

// ModeGate D-620⑤：提案执行控制面接口（dciexec 不依赖 gate 具体型的测试注口）。
type ModeGate interface {
	CheckGate(ctx context.Context) (mode string, allowed bool, reason string, err error)
}
