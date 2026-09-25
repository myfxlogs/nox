// perp_funding_admission.go —— D-491 S2/S3：PERP_FUNDING 证据准入表存取 + 评估器
// 滚窗聚合查询。符号口径逐表核实：facts.symbol=canonical（XAUUSDT 形）、
// ticks.symbol=canonical（qbus q.Symbol 落库）、fin_cex_depth.symbol=venue 原生形
// （binance=XAUUSDT/gate=XAU_USDT/okx=XAU-USDT-SWAP——归一在评估器侧做）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// PerpFundingAdmissionRow perp_funding_admission 一行。
type PerpFundingAdmissionRow struct {
	Canonical      string
	Status         string // admitted/pending_depth/review/denied（评估器裁决）
	BestPair       string
	MeanSpread     float64
	SpreadSD       float64
	DirConsistency float64
	DepthMinUSD    *float64 // NULL=未验
	Samples        int
	OwnerOverride  string // ''/pinned/denied——只由 RPC 写
	Note           string
	EvaluatedAt    *time.Time
	UpdatedAt      time.Time
}

// UpsertPerpFundingAdmission 评估器批量回写（幂等）。owner_override 不在 UPDATE
// 列集——业主钉死位只由 SetPerpFundingOverride 写，评估重跑不冲掉（D-491 状态机）。
func (s *Store) UpsertPerpFundingAdmission(ctx context.Context, rows []PerpFundingAdmissionRow) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO perp_funding_admission
			  (canonical, status, best_pair, mean_spread, spread_sd, dir_consistency, depth_min_usd, samples, note, evaluated_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
			ON CONFLICT (canonical) DO UPDATE SET
			  status=EXCLUDED.status, best_pair=EXCLUDED.best_pair,
			  mean_spread=EXCLUDED.mean_spread, spread_sd=EXCLUDED.spread_sd,
			  dir_consistency=EXCLUDED.dir_consistency, depth_min_usd=EXCLUDED.depth_min_usd,
			  samples=EXCLUDED.samples, note=EXCLUDED.note,
			  evaluated_at=EXCLUDED.evaluated_at, updated_at=now()`,
			r.Canonical, r.Status, r.BestPair, r.MeanSpread, r.SpreadSD, r.DirConsistency,
			r.DepthMinUSD, r.Samples, r.Note, r.EvaluatedAt); err != nil {
			return fmt.Errorf("upsert admission %s: %w", r.Canonical, err)
		}
	}
	return nil
}

// ListPerpFundingAdmission 全量行（前端卡 + 检测器快照构建源）。
func (s *Store) ListPerpFundingAdmission(ctx context.Context) ([]PerpFundingAdmissionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT canonical, status, best_pair, mean_spread, spread_sd, dir_consistency,
		       depth_min_usd, samples, owner_override, note, evaluated_at, updated_at
		FROM perp_funding_admission ORDER BY canonical`)
	if err != nil {
		return nil, fmt.Errorf("list admission: %w", err)
	}
	defer rows.Close()
	var out []PerpFundingAdmissionRow
	for rows.Next() {
		var r PerpFundingAdmissionRow
		if err := rows.Scan(&r.Canonical, &r.Status, &r.BestPair, &r.MeanSpread, &r.SpreadSD,
			&r.DirConsistency, &r.DepthMinUSD, &r.Samples, &r.OwnerOverride, &r.Note,
			&r.EvaluatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetPerpFundingOverride 业主钉死三态写入口（”=清除/pinned=钉死准入/denied=钉死拒绝）。
// 行不存在时插空壳行——支持「先钉后评」；下一论评估回填判据列但保留 override。
func (s *Store) SetPerpFundingOverride(ctx context.Context, canonical, override string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO perp_funding_admission (canonical, owner_override) VALUES ($1, $2)
		ON CONFLICT (canonical) DO UPDATE SET owner_override=EXCLUDED.owner_override, updated_at=now()`,
		canonical, override)
	if err != nil {
		return fmt.Errorf("set override %s: %w", canonical, err)
	}
	return nil
}

// ---- D-491 S3：评估器滚窗聚合（DB 侧 GROUP BY，禁拉回全量行内存算）----

// FundingLegStat 某 canonical×venue 的 funding 窗内统计（value=pct_annualized）。
type FundingLegStat struct {
	Symbol string
	Venue  string
	Mean   float64
	SD     float64
	N      int
}

// FundingWindowLegStats funding facts 7d 滚窗腿级统计（per symbol×venue 一次聚合出）。
func (s *Store) FundingWindowLegStats(ctx context.Context, since time.Time) ([]FundingLegStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT symbol, venue, avg(value), coalesce(stddev_samp(value),0), count(*)::int
		FROM facts WHERE kind='funding' AND ts >= $1
		GROUP BY symbol, venue`, since)
	if err != nil {
		return nil, fmt.Errorf("funding leg stats: %w", err)
	}
	defer rows.Close()
	var out []FundingLegStat
	for rows.Next() {
		var r FundingLegStat
		if err := rows.Scan(&r.Symbol, &r.Venue, &r.Mean, &r.SD, &r.N); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FundingPairStat 某 canonical×无序 venue 对的 5min 桶对齐差值统计。
type FundingPairStat struct {
	Symbol   string
	VenueA   string // 字典序小者
	VenueB   string
	MeanDiff float64 // avg(a.v - b.v)（年化百分点；符号含方向）
	SDDiff   float64
	N        int     // 对齐桶数
	DirPct   float64 // a.v > b.v 的桶占比（0..1）
}

// FundingWindowPairStats funding facts 5min 时间桶对齐 → 同 canonical 跨 venue 两两差值聚合。
// 方向一致性 = max(DirPct, 1-DirPct)（评估器侧取主导向）。
func (s *Store) FundingWindowPairStats(ctx context.Context, since time.Time) ([]FundingPairStat, error) {
	rows, err := s.pool.Query(ctx, `
		WITH b AS (
		  SELECT symbol, venue,
		         date_trunc('hour', ts) + (floor(extract(minute FROM ts) / 5)::int * interval '5 minute') AS tb,
		         avg(value) AS v
		  FROM facts WHERE kind='funding' AND ts >= $1
		  GROUP BY symbol, venue, tb)
		SELECT a.symbol, a.venue, b.venue,
		       avg(a.v - b.v), coalesce(stddev_samp(a.v - b.v),0), count(*)::int,
		       avg(CASE WHEN a.v > b.v THEN 1.0 ELSE 0.0 END)
		FROM b a JOIN b ON a.symbol = b.symbol AND a.tb = b.tb AND a.venue < b.venue
		GROUP BY a.symbol, a.venue, b.venue`, since)
	if err != nil {
		return nil, fmt.Errorf("funding pair stats: %w", err)
	}
	defer rows.Close()
	var out []FundingPairStat
	for rows.Next() {
		var r FundingPairStat
		if err := rows.Scan(&r.Symbol, &r.VenueA, &r.VenueB, &r.MeanDiff, &r.SDDiff, &r.N, &r.DirPct); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PerpTickPresence 窗内有 perp 报价的 canonical→venue 集合（腿可执行性判据：
// 防「funding 有数、合约不存在」——ticks.symbol=canonical 口径直接 join）。
func (s *Store) PerpTickPresence(ctx context.Context, since time.Time) (map[string]map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT symbol, venue FROM ticks WHERE venue_type='perp' AND ts >= $1`, since)
	if err != nil {
		return nil, fmt.Errorf("perp tick presence: %w", err)
	}
	defer rows.Close()
	out := make(map[string]map[string]bool)
	for rows.Next() {
		var sym, venue string
		if err := rows.Scan(&sym, &venue); err != nil {
			return nil, err
		}
		if out[sym] == nil {
			out[sym] = make(map[string]bool)
		}
		out[sym][venue] = true
	}
	return out, rows.Err()
}

// LatestCexDepthUSD fin_cex_depth 每 venue×symbol 最新 mid_depth_usd
// （symbol=venue 原生形——调用方按 venue 规则归一 canonical）。
func (s *Store) LatestCexDepthUSD(ctx context.Context) (map[string]map[string]float64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, symbol) venue, symbol, mid_depth_usd
		FROM fin_cex_depth ORDER BY venue, symbol, ts DESC`)
	if err != nil {
		return nil, fmt.Errorf("latest cex depth: %w", err)
	}
	defer rows.Close()
	out := make(map[string]map[string]float64)
	for rows.Next() {
		var venue, sym string
		var usd float64
		if err := rows.Scan(&venue, &sym, &usd); err != nil {
			return nil, err
		}
		if out[venue] == nil {
			out[venue] = make(map[string]float64)
		}
		out[venue][sym] = usd
	}
	return out, rows.Err()
}
