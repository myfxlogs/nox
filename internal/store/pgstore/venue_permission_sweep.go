// venue_permission_sweep.go D-632：删对象径级联清扫（D-631 数据生命周期分层的删径件）。
// broker_accounts/exchange_configs 删径调用——同键（venue_key 复合键域）权限/配置面行
// 死配置禁囤活表，删行留痕=计数+alerts 告警承载（审计域保留先例 D-150③）：
//
//	autotrade_allow_rules  kind='venue' AND value=key（canonical 行=符号白名单与 venue 键无关零触）
//	xasset_allow_combos    venue_key_buy=key OR venue_key_sell=key（0142 生成列精确匹配）
//	fin_venue_exposure_caps venue_key=key（PRIMARY KEY 即键域）
//	fin_condition_gap      未决行(subject=key) 翻 waived 留痕——禁硬删（请求箱审计语义）；
//	                       同键重入册复活径=venueGapSweep ReopenConditionGap（waived 已入转移集）
//
// 历史台账/快照/income 层零触（D-631 ledger 层=append-only 历史，不在本扫域）。
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// VenuePermissionSweep 级联清扫逐面计数（全零=合法无残面）。
type VenuePermissionSweep struct {
	AllowRules int
	Combos     int
	Caps       int
	GapsWaived int
}

// Any 有任一面清扫行。
func (sw VenuePermissionSweep) Any() bool {
	return sw.AllowRules+sw.Combos+sw.Caps+sw.GapsWaived > 0
}

func (sw VenuePermissionSweep) String() string {
	return fmt.Sprintf("allow_rules=%d combos=%d caps=%d gaps_waived=%d",
		sw.AllowRules, sw.Combos, sw.Caps, sw.GapsWaived)
}

// SweepVenuePermissionSurface 同键权限面级联清扫（单事务四_face；非零计数落 warn 告警
// 留痕——删户/删 venue 是显式操作，权限面变化须可追溯）。key=复合 venue_key。
func (s *Store) SweepVenuePermissionSurface(ctx context.Context, key string) (VenuePermissionSweep, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return VenuePermissionSweep{}, fmt.Errorf("sweep begin: %w", err)
	}
	defer tx.Rollback(ctx)
	sw, err := sweepVenuePermissionSurfaceTx(ctx, tx, key)
	if err != nil {
		return VenuePermissionSweep{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return VenuePermissionSweep{}, fmt.Errorf("sweep commit: %w", err)
	}
	if err := s.alertPermissionSweep(ctx, key, sw); err != nil {
		return sw, err
	}
	return sw, nil
}

// alertPermissionSweep 非零清扫→warn 告警留痕（审计域；零计数=无事发生不噪）。
func (s *Store) alertPermissionSweep(ctx context.Context, key string, sw VenuePermissionSweep) error {
	if !sw.Any() {
		return nil
	}
	msg := fmt.Sprintf("[删除级联] venue_key=%s 权限面清扫 %s（D-631 死配置禁囤活表）", key, sw)
	if err := s.InsertAlert(ctx, nil, "warn", msg); err != nil {
		return fmt.Errorf("sweep alert: %w", err)
	}
	return nil
}

// sweepVenuePermissionSurfaceTx 事务体内四_face 扫清（DeleteExchangeConfigCascade 同事务复用）。
func sweepVenuePermissionSurfaceTx(ctx context.Context, tx pgx.Tx, key string) (VenuePermissionSweep, error) {
	var sw VenuePermissionSweep
	tag, err := tx.Exec(ctx, `DELETE FROM autotrade_allow_rules WHERE kind='venue' AND value=$1`, key)
	if err != nil {
		return sw, fmt.Errorf("sweep allow rules: %w", err)
	}
	sw.AllowRules = int(tag.RowsAffected())
	tag, err = tx.Exec(ctx,
		`DELETE FROM xasset_allow_combos WHERE venue_key_buy=$1 OR venue_key_sell=$1`, key)
	if err != nil {
		return sw, fmt.Errorf("sweep combos: %w", err)
	}
	sw.Combos = int(tag.RowsAffected())
	tag, err = tx.Exec(ctx, `DELETE FROM fin_venue_exposure_caps WHERE venue_key=$1`, key)
	if err != nil {
		return sw, fmt.Errorf("sweep caps: %w", err)
	}
	sw.Caps = int(tag.RowsAffected())
	tag, err = tx.Exec(ctx, `
		UPDATE fin_condition_gap SET status='waived', resolved_at=now(),
		       resolve_note='删径级联自动 waive（venue_key 已删，禁硬删留痕）'
		WHERE subject=$1 AND status IN ('open','requested')`, key)
	if err != nil {
		return sw, fmt.Errorf("sweep gaps: %w", err)
	}
	sw.GapsWaived = int(tag.RowsAffected())
	return sw, nil
}
