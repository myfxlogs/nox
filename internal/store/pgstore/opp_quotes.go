// opp_quotes.go —— D-187：报价源最佳 APR 读取（fin_cex_dci_quote /
// fin_cex_earn_apr 现有采集面直查；窗口对齐各自 retention）。
package pgstore

import "context"

// MaxDCIAPY 近 30d DCI 报价最佳 apy（指定 underlying 过滤，空串=全资产）。
func (s *Store) MaxDCIAPY(ctx context.Context, underlying string) (float64, bool, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(max(apy),0) FROM fin_cex_dci_quote
		WHERE ts > now() - interval '30 days' AND ($1='' OR underlying=$1)`,
		underlying).Scan(&v)
	if err != nil {
		return 0, false, err
	}
	return v, v > 0, nil
}

// MaxEarnAPR 近 30d 理财报价最佳 apr。
func (s *Store) MaxEarnAPR(ctx context.Context) (float64, bool, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(max(apr),0) FROM fin_cex_earn_apr
		WHERE ts > now() - interval '30 days'`).Scan(&v)
	if err != nil {
		return 0, false, err
	}
	return v, v > 0, nil
}
