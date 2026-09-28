// calib.go —— D-533 S2：校准计分板存储面（月度桶 upsert 幂等 + 提案 UNIQUE 防重 +
// active 取值读面）。红线：本文件只落计分与提案，无任何激活路径（active 翻位=业主 SQL 动作）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// CalibScoreRow 计分板行（月度一桶）。
type CalibScoreRow struct {
	MonthYM    string    `json:"month_ym"`
	Dim        string    `json:"dim"` // commission/slippage/funding
	Venue      string    `json:"venue"`
	Symbol     string    `json:"symbol"` // canonical 基名
	SampleN    int       `json:"sample_n"`
	MAE        float64   `json:"mae"`
	Bias       float64   `json:"bias"` // actual-predicted（bps，正=低估成本）
	P90Abs     float64   `json:"p90_abs"`
	Status     string    `json:"status"` // ok/insufficient
	ComputedAt time.Time `json:"computed_at"`
}

// CalibParam 版本化参数行（提案/种值共用）。
type CalibParam struct {
	ID           int64
	ParamKey     string
	Venue        string
	Symbol       string
	ValueBps     float64
	SourceMonth  string
	SampleN      int
	MAE          float64
	Bias         float64
	ProposedAt   time.Time
	ActivatedAt  *time.Time
	ActivatedBy  string
	PrevValueBps *float64
	Active       bool
}

// UpsertCalibScoreboard 批量写计分桶（PK 冲突覆盖统计值+computed_at，重跑幂等）。
func (s *Store) UpsertCalibScoreboard(ctx context.Context, rows []CalibScoreRow) (int, error) {
	n := 0
	for _, r := range rows {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO calib_scoreboard (month_ym, dim, venue, symbol, sample_n, mae, bias, p90_abs, status, computed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
			ON CONFLICT (month_ym, dim, venue, symbol) DO UPDATE SET
			  sample_n=EXCLUDED.sample_n, mae=EXCLUDED.mae, bias=EXCLUDED.bias,
			  p90_abs=EXCLUDED.p90_abs, status=EXCLUDED.status, computed_at=now()`,
			r.MonthYM, r.Dim, r.Venue, r.Symbol, r.SampleN, r.MAE, r.Bias, r.P90Abs, r.Status)
		if err != nil {
			return n, fmt.Errorf("upsert calib_scoreboard: %w", err)
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// ListCalibScoreboard 指定月份计分桶（dashboard 透出用，dim+venue+symbol 排序）。
func (s *Store) ListCalibScoreboard(ctx context.Context, monthYM string) ([]CalibScoreRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT month_ym, dim, venue, symbol, sample_n, COALESCE(mae,0), COALESCE(bias,0), COALESCE(p90_abs,0), status, computed_at
		FROM calib_scoreboard WHERE month_ym=$1 ORDER BY dim, venue, symbol`, monthYM)
	if err != nil {
		return nil, fmt.Errorf("list calib_scoreboard: %w", err)
	}
	defer rows.Close()
	var out []CalibScoreRow
	for rows.Next() {
		var r CalibScoreRow
		if err := rows.Scan(&r.MonthYM, &r.Dim, &r.Venue, &r.Symbol, &r.SampleN, &r.MAE, &r.Bias, &r.P90Abs, &r.Status, &r.ComputedAt); err != nil {
			return nil, fmt.Errorf("scan calib_scoreboard: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveCalibParams 全量 active 参数（取值链缓存数据源）；key=`param_key|venue|symbol`。
func (s *Store) ActiveCalibParams(ctx context.Context) (map[string]float64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT param_key, venue, symbol, value_bps FROM calib_params WHERE active`)
	if err != nil {
		return nil, fmt.Errorf("active calib_params: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var k, venue, symbol string
		var v float64
		if err := rows.Scan(&k, &venue, &symbol, &v); err != nil {
			return nil, fmt.Errorf("scan calib_params: %w", err)
		}
		out[k+"|"+venue+"|"+symbol] = v
	}
	return out, rows.Err()
}

// UpsertCalibProposal 提案行（UNIQUE 幂等键冲突跳过=同键同月不重提）；返回是否新提。
func (s *Store) UpsertCalibProposal(ctx context.Context, p CalibParam) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO calib_params (param_key, venue, symbol, value_bps, source_month, sample_n, mae, bias, active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,false)
		ON CONFLICT (param_key, venue, symbol, source_month) DO NOTHING`,
		p.ParamKey, p.Venue, p.Symbol, p.ValueBps, p.SourceMonth, p.SampleN, p.MAE, p.Bias)
	if err != nil {
		return false, fmt.Errorf("upsert calib_proposal: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
