// CN-FI 信号状态 + 信号配置访问（从 cnfi.go 拆出，保 cnfi.go ≤300 行）。
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"
)

// UpsertCNFISignalState persists signal evaluation output (observe-only 期 observed_state 恒 observe，C11).
func (s *Store) UpsertCNFISignalState(ctx context.Context, key, observedState string, lastValue float64, sampleN int, observedAsOf, evidenceRef string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_signal_state (key, observed_state, last_value, sample_n, observed_as_of, updated_at, evidence_ref)
		VALUES ($1, $2, $3, $4, $5, now(), $6)
		ON CONFLICT (key) DO UPDATE
		   SET observed_state = EXCLUDED.observed_state,
		       last_value = EXCLUDED.last_value,
		       sample_n = EXCLUDED.sample_n,
		       observed_as_of = EXCLUDED.observed_as_of,
		       updated_at = now(),
		       evidence_ref = EXCLUDED.evidence_ref`,
		key, observedState, lastValue, sampleN, observedAsOf, evidenceRef)
	return err
}

// CNFILayerTargets reads the four-layer target ratio config (key='layer_targets').
// Returns layer→pct; missing config = empty map (caller renders nothing rather than guessing).
func (s *Store) CNFILayerTargets(ctx context.Context) (map[string]float64, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT value::text FROM cnfi_signal_config WHERE key='layer_targets'`).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return map[string]float64{}, nil
		}
		return nil, err
	}
	var m map[string]float64
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("cnfi layer_targets json: %w", err)
	}
	return m, nil
}

// UpsertCNFISignalConfigRaw writes a signal_config value (研究席位经部署流程使用).
func (s *Store) UpsertCNFISignalConfigRaw(ctx context.Context, key string, value json.RawMessage, updatedBy, evidenceRef string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO cnfi_signal_config (key, value, updated_at, updated_by, evidence_ref)
		VALUES ($1, $2, now(), $3, $4)
		ON CONFLICT (key) DO UPDATE
		   SET value = EXCLUDED.value, updated_at = now(),
		       updated_by = EXCLUDED.updated_by, evidence_ref = EXCLUDED.evidence_ref`,
		key, value, updatedBy, evidenceRef)
	return err
}

// CNFISignalSnapshotJSON reads all signal states as a JSON snapshot (行动时点证据).
func (s *Store) CNFISignalSnapshotJSON(ctx context.Context) (json.RawMessage, error) {
	var raw json.RawMessage
	rows, err := s.pool.Query(ctx,
		`SELECT COALESCE(jsonb_agg(jsonb_build_object('key',key,'state',observed_state,'value',last_value,'sample_n',sample_n,'as_of',observed_as_of))::text,'[]')
		 FROM cnfi_signal_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
	}
	return raw, rows.Err()
}

// CNFISignalConfigJSON reads one signal_config value as raw json (” key → nil).
func (s *Store) CNFISignalConfigJSON(ctx context.Context, key string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx,
		`SELECT value::text FROM cnfi_signal_config WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

// --- D-259 P2: signal_config 驱动筛选与预警 ---

// CNFIFundSignalConfig holds the P2 fund-signal thresholds (D-259 §1).
// 阈值全在 config（P3 单一真相源），计算路径禁硬编码。
type CNFIFundSignalConfig struct {
	UnderperfBps  float64 // 预警①：基金 1 月收益跑输层内中位 ≥此值（bp）
	LayerDDPct    float64 // 预警②：层内组合 3 月最大回撤 ≥此值（%）
	RankShift     int     // 预警④：持仓基金层内排名单日变动 ≥此值（位）
	AlertsEnabled bool    // observe-only 总开关：false=只记 state 不发 alerts
}

// CNFIFundSignalConfigDefaults returns conservative defaults (observe-only 初值，D-259 §1).
// 无配置时使用此值 + log（禁静默）。
func CNFIFundSignalConfigDefaults() CNFIFundSignalConfig {
	return CNFIFundSignalConfig{
		UnderperfBps:  20,
		LayerDDPct:    0.5,
		RankShift:     2,
		AlertsEnabled: false,
	}
}

// LoadCNFIFundSignalConfig reads P2 thresholds from cnfi_signal_config; missing key = default + log.
func (s *Store) LoadCNFIFundSignalConfig(ctx context.Context) (CNFIFundSignalConfig, error) {
	cfg := CNFIFundSignalConfigDefaults()
	keys := map[string]any{
		"underperf_bps":  &cfg.UnderperfBps,
		"layer_dd_pct":   &cfg.LayerDDPct,
		"rank_shift":     &cfg.RankShift,
		"alerts_enabled": &cfg.AlertsEnabled,
	}
	for key := range keys {
		raw, err := s.CNFISignalConfigJSON(ctx, key)
		if err != nil {
			return cfg, fmt.Errorf("cnfi fund signal config %s: %w", key, err)
		}
		if len(raw) == 0 {
			log.Printf("cnfi: signal config %s missing, using default", key)
			continue
		}
		if err := json.Unmarshal(raw, keys[key]); err != nil {
			return cfg, fmt.Errorf("cnfi fund signal config %s json: %w", key, err)
		}
	}
	return cfg, nil
}

// CNFISignalStateValue reads the last_value of a signal state (nil if not found; D-259 rank_prev).
func (s *Store) CNFISignalStateValue(ctx context.Context, key string) (*float64, error) {
	var v *float64
	err := s.pool.QueryRow(ctx,
		`SELECT last_value::float8 FROM cnfi_signal_state WHERE key=$1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

// CNFIActiveSignalKeys returns signal state keys with observed_as_of within the last `days` days.
// 用于 passes_screen 判定（D-259 §3）：state 存在且近期=基金处于预警状态。
func (s *Store) CNFIActiveSignalKeys(ctx context.Context, days int) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key FROM cnfi_signal_state WHERE observed_as_of >= CURRENT_DATE - $1::int`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}
