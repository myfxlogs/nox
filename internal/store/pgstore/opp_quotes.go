// opp_quotes.go —— D-187：报价源最佳 APR 读取（fin_cex_dci_quote /
// fin_cex_earn_apr 现有采集面直查；窗口对齐各自 retention）。
package pgstore

import (
	"context"

	"github.com/jackc/pgx/v5"
)

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

// MaxEarnAPRByCcy 近 30d 同币种跨 venue 理财报价顶行（溢价闸基准——
// 「权利金跑不赢同币种无风险替代」口径，异币种营销档不入基准）。
// 返 venue+apr 顶行供审计注记；零行→ok=false。
func (s *Store) MaxEarnAPRByCcy(ctx context.Context, ccy string) (string, float64, bool, error) {
	var venue string
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT venue, apr FROM fin_cex_earn_apr
		WHERE ccy=$1 AND ts > now() - interval '30 days'
		ORDER BY apr DESC LIMIT 1`, ccy).Scan(&venue, &v)
	if err == pgx.ErrNoRows {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return venue, v, true, nil
}

// MaxFactValue 近 7d 某 facts kind 最大值（报价面兜底；kind 白名单口径由调用方定）。
func (s *Store) MaxFactValue(ctx context.Context, kind string) (float64, bool, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(max(value),0) FROM facts
		WHERE kind=$1 AND ts > now() - interval '7 days'`, kind).Scan(&v)
	if err != nil {
		return 0, false, err
	}
	return v, v > 0, nil
}

// LatestSpreadSpotRef D-191-R8：fin_cex_spread 最新行（6h 内）买卖均价→现货参照
// （提案快照选档用；无新鲜行返 0——调用方退无界快照不阻塞提案面）。
func (s *Store) LatestSpreadSpotRef(ctx context.Context, symbol string) (float64, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT (price_buy+price_sell)/2 FROM fin_cex_spread
		WHERE symbol=$1 AND ts > now() - interval '6 hours'
		ORDER BY ts DESC LIMIT 1`, symbol).Scan(&v)
	if err != nil {
		return 0, nil // 无行=快照无界参照，降级非错
	}
	return v, nil
}

// LatestEarnAPR venue+ccy 最新理财报价行（idle 产案评估卡证源；缺行→ok=false）。
func (s *Store) LatestEarnAPR(ctx context.Context, venue, ccy string) (float64, bool, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT apr FROM fin_cex_earn_apr WHERE venue=$1 AND ccy=$2
		ORDER BY ts DESC LIMIT 1`, venue, ccy).Scan(&v)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, err
	}
	return v, true, nil
}
