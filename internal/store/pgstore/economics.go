package pgstore

import (
	"context"
	"errors"
	"fmt"

	"arb/internal/capital"
	"github.com/jackc/pgx/v5"
)

// GetActualEconomics D-193 §4.2：读 position_settlements 返回实际净收益。
// 不存在 settlement 行 → 返回 (ActualEconomics{}, capital.ErrUnknownFact)。
// legacy 行（net_realized_usd IS NULL）→ PnlQuality='legacy', NetRealizedUSD=nil。
func (s *Store) GetActualEconomics(ctx context.Context, positionID int64) (capital.ActualEconomics, error) {
	var ae capital.ActualEconomics
	var pricePnl, cashflowsNet, netRealized *float64
	var pnlQuality, completenessStatus *string
	err := s.pool.QueryRow(ctx,
		`SELECT price_pnl_usd, cashflows_net_usd, net_realized_usd, pnl_quality, completeness_status
		 FROM position_settlements WHERE position_id = $1`, positionID).Scan(
		&pricePnl, &cashflowsNet, &netRealized, &pnlQuality, &completenessStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return capital.ActualEconomics{}, capital.ErrUnknownFact
		}
		return capital.ActualEconomics{}, fmt.Errorf("get actual economics pos=%d: %w", positionID, err)
	}
	ae.PricePnlUSD = pricePnl
	ae.CashflowsNetUSD = cashflowsNet
	ae.NetRealizedUSD = netRealized
	if pnlQuality != nil {
		ae.PnlQuality = *pnlQuality
	} else {
		ae.PnlQuality = "unknown"
	}
	if completenessStatus != nil {
		ae.CompletenessStatus = *completenessStatus
	}
	return ae, nil
}

// Expected 实现 capital.EconomicCalculator.Expected（pgstore 层暂用纯函数，无 DB 依赖）。
func (s *Store) Expected(ctx context.Context, hyp capital.Hypothesis, input capital.EconomicInput) (capital.ExpectedEconomics, error) {
	return capital.ComputeExpected(input), nil
}

// Actual 实现 capital.EconomicCalculator.Actual。
func (s *Store) Actual(ctx context.Context, positionID int64) (capital.ActualEconomics, error) {
	return s.GetActualEconomics(ctx, positionID)
}

// 编译时验证 *Store 满足 capital.EconomicCalculator。
var _ capital.EconomicCalculator = (*Store)(nil)
