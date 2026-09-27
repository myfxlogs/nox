// D-284 S3：pgstore.Store → notify.FeishuQueryStore 适配器。
// 同 PgStoreReplyAdapter / PgStorePriceAlertAdapter 模式（含 compile-time 接口断言）。
package notify

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// PgStoreQueryAdapter 包装 pgstore.Store 实现 notify.FeishuQueryStore 接口。
type PgStoreQueryAdapter struct {
	Store *pgstore.Store
}

func (a *PgStoreQueryAdapter) FeishuTarget(ctx context.Context) (chatID, openID string, err error) {
	return a.Store.FeishuTarget(ctx)
}

func (a *PgStoreQueryAdapter) LastTRXAccount(ctx context.Context) (TRXAccountSummary, error) {
	acc, err := a.Store.LastTRXAccount(ctx)
	if err != nil {
		return TRXAccountSummary{}, err
	}
	return TRXAccountSummary{
		BalanceTRX:       float64(acc.BalanceSun) / 1e6,
		AllowanceTRX:     float64(acc.AllowanceSun) / 1e6,
		PendingRewardTRX: float64(acc.RewardNumSun) / 1e6,
		TronPowerTRX:     float64(acc.TronPower) / 1e6,
		VotesTotal:       acc.VotesTotal,
		EnergyDelegated:  float64(acc.EnergyDelegated) / 1e6,
		BandwidthStaked:  float64(acc.BandwidthStaked) / 1e6,
		FetchedAt:        acc.FetchedAt,
	}, nil
}

func (a *PgStoreQueryAdapter) LastCMCPriceMid(ctx context.Context, instrumentID string) (float64, time.Time, error) {
	return a.Store.LastCMCPriceMidWithTime(ctx, instrumentID)
}

func (a *PgStoreQueryAdapter) LastCMCAccountSummary(ctx context.Context) (CMCAccountSummary, error) {
	s, err := a.Store.LastCMCAccountSummary(ctx)
	if err != nil {
		return CMCAccountSummary{}, err
	}
	return CMCAccountSummary{
		TotalAccountValue:     s.TotalAccountValue,
		TotalCashBalance:      s.TotalCashBalance,
		TotalUnrealisedPnL:    s.TotalUnrealisedPnL,
		CloseoutPrice:         s.CloseoutPrice,
		DistanceToCloseoutPct: s.DistanceToCloseoutPct,
		FetchedAt:             s.FetchedAt,
	}, nil
}

func (a *PgStoreQueryAdapter) LastCMCPosition(ctx context.Context, instrumentID string) (CMCPositionSummary, error) {
	p, err := a.Store.LastCMCPositionByInstrument(ctx, instrumentID)
	if err != nil {
		return CMCPositionSummary{}, err
	}
	return CMCPositionSummary{
		InstrumentID:  p.InstrumentID,
		Direction:     p.Direction,
		Quantity:      p.Quantity,
		EntryPrice:    p.EntryPrice,
		UnrealisedPnL: p.UnrealisedPnL,
		FetchedAt:     p.FetchedAt,
	}, nil
}

func (a *PgStoreQueryAdapter) SumTRXTxRentalAll(ctx context.Context) (float64, error) {
	sun, err := a.Store.SumTRXTxRentalAll(ctx)
	return float64(sun) / 1e6, err
}

func (a *PgStoreQueryAdapter) SumTRXTxVoteClaimAll(ctx context.Context) (float64, error) {
	sun, err := a.Store.SumTRXTxVoteClaimAll(ctx)
	return float64(sun) / 1e6, err
}

// SumTodayIncome 今日 UTC 0:00 起的 energy+voting TRX + interest USD。
// rental/vote_claim 按 ts 实时聚合（不依赖 rollup）；overnight 传指针（nil=不设界）。
func (a *PgStoreQueryAdapter) SumTodayIncome(ctx context.Context) (energyTrx, votingTrx, interestUSD float64, err error) {
	now := time.Now().UTC()
	start := now.Truncate(24 * time.Hour)
	rentalSun, err := a.Store.SumTRXTxRentalByRange(ctx, start, now)
	if err != nil {
		return 0, 0, 0, err
	}
	voteSun, err := a.Store.SumTRXTxVoteClaimByRange(ctx, start, now)
	if err != nil {
		return 0, 0, 0, err
	}
	interestUSD, err = a.Store.SumCMCOvernightByRange(ctx, &start, &now)
	if err != nil {
		return 0, 0, 0, err
	}
	return float64(rentalSun) / 1e6, float64(voteSun) / 1e6, interestUSD, nil
}

// D-528 S6：宏观两工具数据面直通（pgstore 行类型零转换）。
func (a *PgStoreQueryAdapter) MacroEventsBetween(ctx context.Context, from, to time.Time) ([]pgstore.MacroEvent, error) {
	return a.Store.MacroEventsBetween(ctx, from, to)
}

func (a *PgStoreQueryAdapter) LatestCot(ctx context.Context, instrument string, limit int) ([]pgstore.CotPosition, error) {
	return a.Store.LatestCot(ctx, instrument, limit)
}

// compile-time: *PgStoreQueryAdapter 实现 FeishuQueryStore 接口。
var _ FeishuQueryStore = (*PgStoreQueryAdapter)(nil)
