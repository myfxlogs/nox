// D-284 S2：飞书自然语言查询存储接口 + notify 包本地类型。
// 接口在 notify 包本地定义，pgstore 适配器实现（避免 notify→pgstore 依赖）。
// 同 PgStoreReplyAdapter / PgStorePriceAlertAdapter 模式。
package notify

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// FeishuQueryStore 飞书查询数据源接口（pgstore 适配器实现）。
type FeishuQueryStore interface {
	FeishuTarget(ctx context.Context) (chatID, openID string, err error)
	LastTRXAccount(ctx context.Context) (TRXAccountSummary, error)
	LastCMCPriceMid(ctx context.Context, instrumentID string) (float64, time.Time, error)
	LastCMCAccountSummary(ctx context.Context) (CMCAccountSummary, error)
	LastCMCPosition(ctx context.Context, instrumentID string) (CMCPositionSummary, error)
	SumTodayIncome(ctx context.Context) (energyTrx, votingTrx, interestUSD float64, err error)
	SumTRXTxRentalAll(ctx context.Context) (float64, error)
	SumTRXTxVoteClaimAll(ctx context.Context) (float64, error)
	// D-528 S6：宏观两工具数据面（返回 pgstore 行类型，零复制）。
	MacroEventsBetween(ctx context.Context, from, to time.Time) ([]pgstore.MacroEvent, error)
	// D-201：飞书 NLQ 三读法（策略在跑/财务摘要/近期订单）+ RO 只读串热读。
	StrategiesRunning(ctx context.Context) ([]pgstore.Position, []pgstore.PersonalPosition, error)
	FinanceSummary(ctx context.Context) (FeishuFinanceSummary, error)
	ListDCIOrders(ctx context.Context, limit int) ([]pgstore.DCIOrder, error)
	AIReadOnlyDSN(ctx context.Context) (string, error)
	// D-168 ④：ack 拦截面——episode + alerts.acked 同步。
	AckAllAlertEpisodes(ctx context.Context) (int, error)
	AckAlertEpisodeByPushMsg(ctx context.Context, msgID string) (key string, ok bool, err error)
	AckUnackedAlertsByKey(ctx context.Context, key string) (int, error)
	AckAll(ctx context.Context) error
}

// TRXAccountSummary TRX 账户快照（notify 包本地，sun→TRX 已换算）。
type TRXAccountSummary struct {
	BalanceTRX       float64
	AllowanceTRX     float64
	PendingRewardTRX float64 // D-285：tronscan rewardNum（待领取真值，TRX）
	TronPowerTRX     float64
	VotesTotal       int64
	EnergyDelegated  float64
	BandwidthStaked  float64
	FetchedAt        time.Time
}

// CMCAccountSummary CMC 账户摘要。
type CMCAccountSummary struct {
	TotalAccountValue     float64
	TotalCashBalance      float64
	TotalUnrealisedPnL    float64
	CloseoutPrice         float64
	DistanceToCloseoutPct float64
	FetchedAt             time.Time
}

// CMCPositionSummary CMC 持仓摘要。
type CMCPositionSummary struct {
	InstrumentID  string
	Direction     string
	Quantity      float64
	EntryPrice    float64
	UnrealisedPnL float64
	FetchedAt     time.Time
}

// FeishuFinanceSummary D-201：财务摘要（托管池 BTC 份额+今日/累计收益+在期 DCI）。
// 无字段如实报——零值字段渲染层标注「未记账」不命名 pnl 防 LLM 幻觉。
type FeishuFinanceSummary struct {
	CustodyBTCQty    float64 // personal_positions OPEN 的 BTC 行 qty 合计（托管池表象）
	TodayEnergyTRX   float64
	TodayVotingTRX   float64
	TodayInterestUSD float64
	TotalInterestUSD float64 // position_daily_income 累计 interest_usd
	TotalPnlUSD      *float64
	IncomeDays       int                // 记账天数
	OpenDCI          []pgstore.DCIOrder // filled 且未到期（tenor 缺=计入如实报）
}
