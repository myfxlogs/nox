// D-284 S2：飞书自然语言查询存储接口 + notify 包本地类型。
// 接口在 notify 包本地定义，pgstore 适配器实现（避免 notify→pgstore 依赖）。
// 同 PgStoreReplyAdapter / PgStorePriceAlertAdapter 模式。
package notify

import (
	"context"
	"math/big"
	"time"
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
	LatestTRXCycleVi(ctx context.Context) ([]TRXCycleViRow, error)
}

// TRXAccountSummary TRX 账户快照（notify 包本地，sun→TRX 已换算）。
type TRXAccountSummary struct {
	BalanceTRX      float64
	AllowanceTRX    float64
	TronPowerTRX    float64
	VotesTotal      int64
	EnergyDelegated float64
	BandwidthStaked float64
	FetchedAt       time.Time
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

// TRXCycleViRow TRX 投票 Vi 快照行（待领取=Σ vi×user_votes/10^18 用）。
type TRXCycleViRow struct {
	SRAddress string
	Vi        *big.Int
	UserVotes int64
}
