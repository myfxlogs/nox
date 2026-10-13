// strategy_lifecycle.go —— D-260 M4.2：fin_strategy 状态机 + fin_strategy_criteria 读面。
//
// 状态机=唯一存活裁决（candidate→paper→graduated→live | demoted/retired）；
// 判据=数据行（'*' 全局 + 策略专属，专属与全局同评）。零 LLM 于本路径。
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// StrategyCriteria 判据行投影（lifecycle.Criterion 逐字段同形——adapter 映射）。
type StrategyCriteria struct {
	Strategy string
	Kind     string
	Pred     string
	Params   map[string]any
}

// ListStrategyCriteria 拉取某策略在某 kind 下的判据行（'*' 全局 + 专属并存同评）。
func (s *Store) ListStrategyCriteria(ctx context.Context, strategy, kind string) ([]StrategyCriteria, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT strategy, kind, pred, params FROM fin_strategy_criteria
		WHERE kind = $1 AND (strategy = '*' OR strategy = $2)
		ORDER BY id`, kind, strategy)
	if err != nil {
		return nil, fmt.Errorf("list strategy criteria: %w", err)
	}
	defer rows.Close()
	var out []StrategyCriteria
	for rows.Next() {
		var c StrategyCriteria
		var params []byte
		if err := rows.Scan(&c.Strategy, &c.Kind, &c.Pred, &params); err != nil {
			return nil, fmt.Errorf("scan strategy criteria: %w", err)
		}
		c.Params = map[string]any{}
		if len(params) > 0 {
			if err := json.Unmarshal(params, &c.Params); err != nil {
				return nil, fmt.Errorf("criteria params json: %w", err)
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetStrategyState 状态迁移（CAS 语义=upsert；同步 strategy_stats.disabled 投影一致）。
// decidedBy 审计（system=判据自动/committee=委员会人工）。
// 首次进 live 落「可晋级」info 告警——只提示不触发执行（自动执行=扫描拍独立路径）。
func (s *Store) SetStrategyState(ctx context.Context, strategy, state, decidedBy string, hits []string) error {
	var prev string
	err := s.pool.QueryRow(ctx, `SELECT state FROM fin_strategy WHERE strategy=$1`, strategy).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("strategy state read: %w", err)
	}
	disabled := state != "live"
	hitsJSON, err := json.Marshal(hits)
	if err != nil {
		return fmt.Errorf("hit rules json: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("strategy state tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO fin_strategy (strategy, state, hit_rules, decided_at, decided_by, updated_at)
		VALUES ($1,$2,$3,now(),$4,now())
		ON CONFLICT (strategy) DO UPDATE SET state=$2, hit_rules=$3,
			decided_at=now(), decided_by=$4, updated_at=now()`,
		strategy, state, hitsJSON, decidedBy); err != nil {
		return fmt.Errorf("fin_strategy upsert: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO strategy_stats (strategy, disabled, disabled_reason, updated_at)
		VALUES ($1,$2,$3,now())
		ON CONFLICT (strategy) DO UPDATE SET disabled=$2,
			disabled_reason=CASE WHEN $2 THEN $3 ELSE strategy_stats.disabled_reason END, updated_at=now()`,
		strategy, disabled, "lifecycle:"+state); err != nil {
		return fmt.Errorf("strategy_stats sync: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("strategy state commit: %w", err)
	}
	if state == "live" && prev != "live" {
		// 首次真金晋级=告警仅提示（票面④）；自动执行由扫描拍独立判 state+评估卡。
		msg := fmt.Sprintf("策略 %s 晋级 live（首次真金晋级——告警仅提示，本拍不自动执行）", strategy)
		if err := s.InsertAlertDedup(ctx, "info", msg, 24*time.Hour); err != nil {
			return fmt.Errorf("live promotion alert: %w", err)
		}
	}
	return nil
}

// GetStrategyState 策略当前态（行缺=ok=false→caller fail-closed 判 disabled）。
func (s *Store) GetStrategyState(ctx context.Context, strategy string) (string, bool, error) {
	var st string
	err := s.pool.QueryRow(ctx, `SELECT state FROM fin_strategy WHERE strategy=$1`, strategy).Scan(&st)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get strategy state: %w", err)
	}
	return st, true, nil
}

// EnsureStrategyRows 覆盖闸：strategy_stats 全集 + 启用模板键补 fin_strategy 行
// （新键=candidate 待评估——非 live 即 disabled fail-closed）。幂等。
func (s *Store) EnsureStrategyRows(ctx context.Context, templates []string) (int, error) {
	// strategy_stats 键=已在飞策略（有结算实绩才入 stats）——in-flight=live
	// （候选态起判会切断存量在跑者；disabled 行→retired 与迁移同义）。
	res, err := s.pool.Exec(ctx, `
		INSERT INTO fin_strategy (strategy, state, decided_by)
		SELECT strategy, CASE WHEN disabled THEN 'retired' ELSE 'live' END, 'lifecycle-sync'
		FROM strategy_stats
		ON CONFLICT (strategy) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("ensure strategy rows: %w", err)
	}
	n := int(res.RowsAffected())
	for _, t := range templates {
		res, err := s.pool.Exec(ctx, `
			INSERT INTO fin_strategy (strategy, state, decided_by)
			VALUES ($1,'candidate','lifecycle-sync')
			ON CONFLICT (strategy) DO NOTHING`, t)
		if err != nil {
			return n, fmt.Errorf("ensure template row: %w", err)
		}
		n += int(res.RowsAffected())
	}
	return n, nil
}

// ListStrategiesByState 某态策略名集（晋级评估/报表读面）。
func (s *Store) ListStrategiesByState(ctx context.Context, state string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT strategy FROM fin_strategy WHERE state=$1 ORDER BY strategy`, state)
	if err != nil {
		return nil, fmt.Errorf("list strategies by state: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan strategy state: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DisableStrategy 禁用策略（自动淘汰）。
func (s *Store) DisableStrategy(ctx context.Context, strategy, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("disable strategy tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO strategy_stats (strategy, disabled, disabled_reason, updated_at)
		VALUES ($1, true, $2, now())
		ON CONFLICT (strategy) DO UPDATE SET disabled = true, disabled_reason = $2, updated_at = now()`,
		strategy, reason)
	if err != nil {
		return fmt.Errorf("disable strategy: %w", err)
	}
	// D-260 M4.2：状态机=唯一存活裁决——禁用同步 fin_strategy.state='retired'，
	// reason 入 hit_rules 命中痕（幂等 upsert）。
	_, err = tx.Exec(ctx, `
		INSERT INTO fin_strategy (strategy, state, hit_rules, decided_at, decided_by, updated_at)
		VALUES ($1, 'retired', to_jsonb($2::text), now(), 'system', now())
		ON CONFLICT (strategy) DO UPDATE SET state = 'retired',
			hit_rules = fin_strategy.hit_rules || to_jsonb($2::text),
			decided_at = now(), decided_by = 'system', updated_at = now()`,
		strategy, reason)
	if err != nil {
		return fmt.Errorf("disable strategy fin_strategy: %w", err)
	}
	return tx.Commit(ctx)
}

// IsStrategyDisabled 检查策略是否被禁用。
// D-260：状态机投影 OR 兜底——fin_strategy 行存且 state!='live'→disabled；
// 另 strategy_stats.disabled=true 同停（写径双侧同步，OR 语义=严 fail-closed）。
func (s *Store) IsStrategyDisabled(ctx context.Context, strategy string) (bool, error) {
	var disabled bool
	err := s.pool.QueryRow(ctx, `
		SELECT (fs.state IS NOT NULL AND fs.state <> 'live') OR COALESCE(ss.disabled, false)
		FROM (SELECT 1) x
		LEFT JOIN fin_strategy fs ON fs.strategy = $1
		LEFT JOIN strategy_stats ss ON ss.strategy = $1`, strategy).Scan(&disabled)
	if err != nil {
		return false, fmt.Errorf("check strategy disabled: %w", err)
	}
	return disabled, nil
}

// ListDisabledStrategies 返回所有 disabled=true 的策略名集合。
// 供 engine 检测层排除 disabled 策略产出的机会（§1 自动淘汰闭环：disable → 不执行 + 不产机会 + 不展示）。
func (s *Store) ListDisabledStrategies(ctx context.Context) (map[string]bool, error) {
	// D-260：状态机投影 OR 兜底——fin_strategy 非 live 行 + stats.disabled 行并集。
	rows, err := s.pool.Query(ctx, `
		SELECT strategy FROM fin_strategy WHERE state <> 'live'
		UNION
		SELECT strategy FROM strategy_stats WHERE disabled = true`)
	if err != nil {
		return nil, fmt.Errorf("list disabled strategies: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan disabled strategy: %w", err)
		}
		out[name] = true
	}
	return out, rows.Err()
}
