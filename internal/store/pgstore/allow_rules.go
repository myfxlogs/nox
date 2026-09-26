// allow_rules.go D-443 S1：autotrade_allow_rules 加载器（venue/canonical 单表双 kind）。
// 加载失败 → 调用方 startup fatal（fail-closed 同 xasset 先例）；venue 空集=全拒（最保守），
// canonical 空集=全放行（D-122 语义不动）。simulated 闸对齐 environment（live 只读 false）。
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LoadAutotradeAllowRules 按 kind 分流两 map（ venues/canonicals 均非 nil——空集合法态）。
func (s *Store) LoadAutotradeAllowRules(ctx context.Context, simulated bool) (venues, canonicals map[string]bool, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, value FROM autotrade_allow_rules
		WHERE enabled AND simulated = $1`, simulated)
	if err != nil {
		return nil, nil, fmt.Errorf("load autotrade allow rules: %w", err)
	}
	defer rows.Close()
	venues, canonicals = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, nil, err
		}
		switch kind {
		case "venue":
			venues[value] = true
		case "canonical":
			canonicals[value] = true
		}
	}
	return venues, canonicals, rows.Err()
}

// AutotradeAllowRule D-444 S2：autotrade_allow_rules 全列行（管理读面用）。
type AutotradeAllowRule struct {
	ID        int64
	Kind      string // venue / canonical
	Value     string
	Enabled   bool
	Simulated bool
	Note      string
}

// ListAutotradeAllowRulesAll 全量行（含 disabled+simulated；ORDER BY kind,value）。
func (s *Store) ListAutotradeAllowRulesAll(ctx context.Context) ([]AutotradeAllowRule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, value, enabled, simulated, note FROM autotrade_allow_rules ORDER BY kind, value`)
	if err != nil {
		return nil, fmt.Errorf("list autotrade allow rules: %w", err)
	}
	defer rows.Close()
	var out []AutotradeAllowRule
	for rows.Next() {
		var r AutotradeAllowRule
		if err := rows.Scan(&r.ID, &r.Kind, &r.Value, &r.Enabled, &r.Simulated, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAutotradeAllowRuleEnabled 翻转 enabled（note 非空才覆写）；id 不存在→ErrNoRows 上抛。
func (s *Store) SetAutotradeAllowRuleEnabled(ctx context.Context, id int64, enabled bool, note string) (AutotradeAllowRule, error) {
	var r AutotradeAllowRule
	err := s.pool.QueryRow(ctx, `
		UPDATE autotrade_allow_rules
		SET enabled=$2, note=CASE WHEN $3 != '' THEN $3 ELSE note END
		WHERE id=$1
		RETURNING id, kind, value, enabled, simulated, note`,
		id, enabled, note).Scan(&r.ID, &r.Kind, &r.Value, &r.Enabled, &r.Simulated, &r.Note)
	if err != nil {
		return r, fmt.Errorf("set autotrade allow rule #%d: %w", id, err)
	}
	return r, nil
}

// ---- D-492 S2：白名单增删（UI 管理面；enabled 翻转同表语义不变）----

// FindAutotradeAllowRule 按唯一键 (kind,value,simulated) 查行（nil=不存在）——去重判重 +
// Insert ON CONFLICT 空拳后的回查共用。同 key 双 sim/real 行合法共存故三列键语义正确。
func (s *Store) FindAutotradeAllowRule(ctx context.Context, kind, value string, simulated bool) (*AutotradeAllowRule, error) {
	var r AutotradeAllowRule
	err := s.pool.QueryRow(ctx, `
		SELECT id, kind, value, enabled, simulated, note FROM autotrade_allow_rules
		WHERE kind=$1 AND value=$2 AND simulated=$3`, kind, value, simulated).
		Scan(&r.ID, &r.Kind, &r.Value, &r.Enabled, &r.Simulated, &r.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("find autotrade allow rule %s=%s sim=%v: %w", kind, value, simulated, err)
	}
	return &r, nil
}

// AutotradeAllowRuleByID 按 id 查行（nil=不存在）——Delete 前置 enabled 校验用。
func (s *Store) AutotradeAllowRuleByID(ctx context.Context, id int64) (*AutotradeAllowRule, error) {
	var r AutotradeAllowRule
	err := s.pool.QueryRow(ctx, `
		SELECT id, kind, value, enabled, simulated, note FROM autotrade_allow_rules WHERE id=$1`, id).
		Scan(&r.ID, &r.Kind, &r.Value, &r.Enabled, &r.Simulated, &r.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get autotrade allow rule #%d: %w", id, err)
	}
	return &r, nil
}

// InsertAutotradeAllowRule 幂等插入（表已有 UNIQUE (kind,value,simulated)——DO NOTHING
// 零竞态；撞键时空拳返回 nil——调用方回查 Find 判定复用/新增）。
func (s *Store) InsertAutotradeAllowRule(ctx context.Context, kind, value string, simulated bool, note string) (*AutotradeAllowRule, error) {
	var r AutotradeAllowRule
	err := s.pool.QueryRow(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated, note)
		VALUES ($1,$2,true,$3,$4)
		ON CONFLICT (kind, value, simulated) DO NOTHING
		RETURNING id, kind, value, enabled, simulated, note`,
		kind, value, simulated, note).Scan(&r.ID, &r.Kind, &r.Value, &r.Enabled, &r.Simulated, &r.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // 撞键空拳——并发/同 key 已有行，调用方回查
		}
		return nil, fmt.Errorf("insert autotrade allow rule %s=%s: %w", kind, value, err)
	}
	return &r, nil
}

// DeleteAutotradeAllowRule 按 id 删行（调用方先 AutotradeAllowRuleByID 校验 enabled=false）。
func (s *Store) DeleteAutotradeAllowRule(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM autotrade_allow_rules WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete autotrade allow rule #%d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete autotrade allow rule #%d: %w", id, pgx.ErrNoRows)
	}
	return nil
}

// ---- D-492 S3：simulated 服务端推导的底层证据查询（禁取首行——显式 WHERE 过滤）----

// ExchangeConfigHasRow exchange_configs 是否存在 (venue,venue_type,simulated) 匹配行。
// 同 venue:type 双 sim/real 配置合法并存——按 env 取对应行，无匹配=推导失败（KB
// exchange-config-pick-simulated-filter：禁「取首行不滤 simulated」）。
func (s *Store) ExchangeConfigHasRow(ctx context.Context, venue, venueType string, simulated bool) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM exchange_configs WHERE venue=$1 AND venue_type=$2 AND simulated=$3)`,
		venue, venueType, simulated).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("exchange config exists %s:%s sim=%v: %w", venue, venueType, simulated, err)
	}
	return ok, nil
}

// BrokerAccountIsReal 按 venue+platform 取最新账号 is_real 三态（nil=未知/无账号）。
// venue 列=venue_key 主名段（如 3001157:mt5 → venue='3001157' platform=1）。
func (s *Store) BrokerAccountIsReal(ctx context.Context, venue string, platform int) (*bool, error) {
	var isReal *bool
	err := s.pool.QueryRow(ctx, `
		SELECT is_real FROM broker_accounts WHERE venue=$1 AND venue_type=(CASE $2::int WHEN 0 THEN 'mt4' ELSE 'mt5' END)
		ORDER BY id DESC LIMIT 1`, venue, platform).Scan(&isReal)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("broker account is_real venue=%s platform=%d: %w", venue, platform, err)
	}
	return isReal, nil
}

// CanonicalSeenAnywhere canonical 存在性探测（fin_cex_universe base/symbol ∪
// broker_symbol_map canonical）——无权威注册表，双查尽力；未命中仅 warn 不拒。
func (s *Store) CanonicalSeenAnywhere(ctx context.Context, canonical string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM fin_cex_universe WHERE base=$1 OR symbol=$1)
		    OR EXISTS(SELECT 1 FROM broker_symbol_map WHERE canonical=$1)`, canonical).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("canonical seen %s: %w", canonical, err)
	}
	return ok, nil
}
