// pgstore.Store → notify.FeishuQueryStore 适配器。
// 同 PgStoreReplyAdapter 模式（含 compile-time 接口断言）。
package notify

import (
	"context"
	"time"

	"nox/internal/store/pgstore"
)

// PgStoreQueryAdapter 包装 pgstore.Store 实现 notify.FeishuQueryStore 接口。
type PgStoreQueryAdapter struct {
	Store *pgstore.Store
	// PriceNow D-633 现态价注入件（qbus 装配——venue+symbol→bid/ask/ok）。
	PriceNow func(venue, symbol string) (bid, ask float64, ok bool)
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

func (a *PgStoreQueryAdapter) TickMid(ctx context.Context, venue, symbol string) (float64, bool) {
	// D-633 现态源注入优先（cmd/core 由 qbus 装配 PriceNow——nil 回落 DB 旧径）。
	if a.PriceNow != nil {
		if bid, ask, ok := a.PriceNow(venue, symbol); ok && bid > 0 && ask > 0 {
			return (bid + ask) / 2, true
		}
	}
	return a.Store.LatestTickMid(ctx, venue, symbol) // miss→持久末帧回退（D-162）
}

func (a *PgStoreQueryAdapter) SumTRXTxRentalAll(ctx context.Context) (float64, error) {
	sun, err := a.Store.SumTRXTxRentalAll(ctx)
	return float64(sun) / 1e6, err
}

func (a *PgStoreQueryAdapter) SumTRXTxVoteClaimAll(ctx context.Context) (float64, error) {
	sun, err := a.Store.SumTRXTxVoteClaimAll(ctx)
	return float64(sun) / 1e6, err
}

// SumTodayIncome 今日 UTC 0:00 起的 energy+voting TRX（overnight 成分随 CMC 域剔除）。
// rental/vote_claim 按 ts 实时聚合（不依赖 rollup）。
func (a *PgStoreQueryAdapter) SumTodayIncome(ctx context.Context) (energyTrx, votingTrx float64, err error) {
	now := time.Now().UTC()
	start := now.Truncate(24 * time.Hour)
	rentalSun, err := a.Store.SumTRXTxRentalByRange(ctx, start, now)
	if err != nil {
		return 0, 0, err
	}
	voteSun, err := a.Store.SumTRXTxVoteClaimByRange(ctx, start, now)
	if err != nil {
		return 0, 0, err
	}
	return float64(rentalSun) / 1e6, float64(voteSun) / 1e6, nil
}

// 宏观两工具数据面直通（pgstore 行类型零转换）。
func (a *PgStoreQueryAdapter) MacroEventsBetween(ctx context.Context, from, to time.Time) ([]pgstore.MacroEvent, error) {
	return a.Store.MacroEventsBetween(ctx, from, to)
}

// ④：ack 拦截面直通。
func (a *PgStoreQueryAdapter) AckAllAlertEpisodes(ctx context.Context) (int, error) {
	return a.Store.AckAllAlertEpisodes(ctx)
}
func (a *PgStoreQueryAdapter) AckAlertEpisodeByPushMsg(ctx context.Context, msgID string) (string, bool, error) {
	return a.Store.AckAlertEpisodeByPushMsg(ctx, msgID)
}
func (a *PgStoreQueryAdapter) AckUnackedAlertsByKey(ctx context.Context, key string) (int, error) {
	return a.Store.AckUnackedAlertsByKey(ctx, key)
}
func (a *PgStoreQueryAdapter) AckAll(ctx context.Context) error { return a.Store.AckAll(ctx) }

// compile-time: *PgStoreQueryAdapter 实现 FeishuQueryStore 接口。
var _ FeishuQueryStore = (*PgStoreQueryAdapter)(nil)

// StrategiesRunning D-201①：OPEN 双腿仓位（type=策略族）+OPEN 个人持仓。
func (a *PgStoreQueryAdapter) StrategiesRunning(ctx context.Context) ([]pgstore.Position, []pgstore.PersonalPosition, error) {
	poss, err := a.Store.ListPositions(ctx, "OPEN", 200, 0)
	if err != nil {
		return nil, nil, err
	}
	pers, err := a.Store.ListPersonalPositions(ctx, "OPEN", 200, 0)
	if err != nil {
		return nil, nil, err
	}
	return poss, pers, nil
}

// FinanceSummary D-201①：托管池 BTC 份额+position_daily_income 今日/累计+在期 DCI。
func (a *PgStoreQueryAdapter) FinanceSummary(ctx context.Context) (FeishuFinanceSummary, error) {
	var s FeishuFinanceSummary
	pers, err := a.Store.ListPersonalPositions(ctx, "OPEN", 200, 0)
	if err != nil {
		return s, err
	}
	for i := range pers {
		if pers[i].Asset == "BTC" && pers[i].Qty != nil {
			s.CustodyBTCQty += *pers[i].Qty
		}
	}
	s.TodayEnergyTRX, s.TodayVotingTRX, err = a.SumTodayIncome(ctx)
	// TodayInterestUSD：overnight 成分随 CMC 域剔除，如实置 0（渲染层「未记账」）。
	if err != nil {
		return s, err
	}
	_, _, s.TotalInterestUSD, err = a.Store.SumDailyIncomeByRange(ctx, nil, nil)
	if err != nil {
		return s, err
	}
	if s.IncomeDays, err = a.Store.CountDailyIncomeByRange(ctx, nil, nil); err != nil {
		return s, err
	}
	days, err := a.Store.ListDailyIncome(ctx, nil, nil, 10000, 0)
	if err != nil {
		return s, err
	}
	var pnl float64
	var hasPnl bool
	for i := range days {
		if days[i].PnlUSD != nil {
			pnl += *days[i].PnlUSD
			hasPnl = true
		}
	}
	if hasPnl {
		s.TotalPnlUSD = &pnl
	}
	orders, err := a.Store.ListDCIOrders(ctx, 200)
	if err != nil {
		return s, err
	}
	now := time.Now()
	for i := range orders {
		o := &orders[i]
		if o.Status != "filled" {
			continue
		}
		if o.TenorDays != nil && !o.CreatedAt.AddDate(0, 0, *o.TenorDays).After(now) {
			continue // 已到期
		}
		s.OpenDCI = append(s.OpenDCI, *o)
	}
	return s, nil
}

// ListDCIOrders D-201①：近 N 单（id DESC；response 已 SQL 截≤300 字符）。
func (a *PgStoreQueryAdapter) ListDCIOrders(ctx context.Context, limit int) ([]pgstore.DCIOrder, error) {
	return a.Store.ListDCIOrders(ctx, limit)
}

// AIReadOnlyDSN D-201②：app_secrets NOX_AI_PG_DSN 热读（轮换未重启即探真值，
// 同 wire_selfwatch probeAnalystRO 口径）。空=未配置→工具如实报。
func (a *PgStoreQueryAdapter) AIReadOnlyDSN(ctx context.Context) (string, error) {
	secrets, err := a.Store.LoadAppSecrets(ctx)
	if err != nil {
		return "", err
	}
	return secrets["NOX_AI_PG_DSN"], nil
}
