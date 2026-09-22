// carry_universe_store.go D-336 S2：pgstore.Store 适配（scan.Store 接口实现）。
package scan

import (
	"context"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// PGStore pgstore.Store 适配（NewCarryUniverseStore 装配）。
type PGStore struct{ S *pgstore.Store }

func NewCarryUniverseStore(s *pgstore.Store) *PGStore { return &PGStore{S: s} }

func (p *PGStore) LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	return p.S.LatestFacts(ctx, kind, venue, symbol)
}

func (p *PGStore) ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error) {
	return p.S.ListSymbolMappings(ctx, venue)
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
