// carry_universe_store.go D-336 S2：pgstore.Store 适配（scan.Store 接口实现）。
package scan

import (
	"context"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// PGStore pgstore.Store 适配（NewCarryUniverseStore 装配）。
type PGStore struct{ S *pgstore.Store }

func NewCarryUniverseStore(s *pgstore.Store) *PGStore { return &PGStore{S: s} }

func (p *PGStore) LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	return p.S.LatestFacts(ctx, kind, venue, symbol)
}

func (p *PGStore) LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) {
	return p.S.LatestFactsByKind(ctx, kind)
}

func (p *PGStore) ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error) {
	return p.S.ListSymbolMappings(ctx, venue)
}

// ListSymbolAliases D-464 S1：别名表透传。
func (p *PGStore) ListSymbolAliases(ctx context.Context) (map[string]string, error) {
	return p.S.ListSymbolAliases(ctx)
}

// ListAssetEquiv D-043：等价类映射透传。
func (p *PGStore) ListAssetEquiv(ctx context.Context) ([]pgstore.AssetEquivEntry, error) {
	return p.S.ListAssetEquiv(ctx)
}

// ListCexUniverse D-350：fin_cex_universe 雷达表（spot_carry 第三结构数据源）。
func (p *PGStore) ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error) {
	return p.S.ListCexUniverse(ctx)
}

// ListBrokerAccountStatuses D-388 S1.3：venue→status（broker 托管 venue 非 connected 退配对）。
func (p *PGStore) ListBrokerAccountStatuses(ctx context.Context) (map[string]string, error) {
	return p.S.ListBrokerAccountStatuses(ctx)
}

func (p *PGStore) ReplaceCarryUniverse(ctx context.Context, rows []pgstore.CarryUniverseRow) error {
	return p.S.ReplaceCarryUniverse(ctx, rows)
}

// FundingWindowAvg D-035：funding 窗聚合单条读口透传（7d 滚动均值共享实现的数据源）。
func (p *PGStore) FundingWindowAvg(ctx context.Context, venue, symbol string, since time.Time) (float64, time.Time, int, error) {
	return p.S.FundingWindowAvg(ctx, venue, symbol, since)
}

// FactsWindowStats D-412 F3：窗口均值读口透传（swap 慢变量抗 flap）。
func (p *PGStore) FactsWindowStats(ctx context.Context, kind, venue, symbol string, window time.Duration) (float64, error) {
	return p.S.FactsWindowStats(ctx, kind, venue, symbol, window)
}

// InsertIntraTrade D-425：日内轨透传（独立沙箱轨存取）。
func (p *PGStore) InsertIntraTrade(ctx context.Context, r *pgstore.IntraTrade) (int64, error) {
	return p.S.InsertIntraTrade(ctx, r)
}

// CloseIntraTrade D-425：日内轨出场透传。
func (p *PGStore) CloseIntraTrade(ctx context.Context, id int64, exitPx float64, reason string, slippageBps, pnl float64) error {
	return p.S.CloseIntraTrade(ctx, id, exitPx, reason, slippageBps, pnl)
}

// ListOpenIntraTrades D-425：日内轨在开仓透传。
func (p *PGStore) ListOpenIntraTrades(ctx context.Context) ([]pgstore.IntraTrade, error) {
	return p.S.ListOpenIntraTrades(ctx)
}

// IntraDayRealizedPnl D-425：日内已实现盈亏透传（熔断计账面）。
func (p *PGStore) IntraDayRealizedPnl(ctx context.Context, day time.Time) (float64, error) {
	return p.S.IntraDayRealizedPnl(ctx, day)
}

// TicksWindow D-425：ticks 窗口读面透传（信号与回放门数据源）。
func (p *PGStore) TicksWindow(ctx context.Context, venue, symbol string, since time.Time) ([]pgstore.TickRow, error) {
	return p.S.TicksWindow(ctx, venue, symbol, since)
}

// InsertIntraEvent D-441 S3：日内轨栅栏事件透传（kill/circuit 落库）。
func (p *PGStore) InsertIntraEvent(ctx context.Context, kind, baseline, detail string) error {
	return p.S.InsertIntraEvent(ctx, kind, baseline, detail)
}

// HasIntraEventToday D-441 S3：当日同 kind 去重透传（告警不重发面）。
func (p *PGStore) HasIntraEventToday(ctx context.Context, kind string) (bool, error) {
	return p.S.HasIntraEventToday(ctx, kind)
}

// ListIntraVenues 日内轨 venue 动态派生透传（2026-10-01 业主裁定）。
func (p *PGStore) ListIntraVenues(ctx context.Context) (mt []string, cex []string, err error) {
	return p.S.ListIntraVenues(ctx)
}

// ListSwapFreeAccounts D-028 ④：venue→swap_free 旗（broker_accounts 全量）。
func (p *PGStore) ListSwapFreeAccounts(ctx context.Context) (map[string]bool, error) {
	accounts, err := p.S.ListBrokerAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		if a.SwapFree {
			out[a.Venue] = true
		}
	}
	return out, nil
}
