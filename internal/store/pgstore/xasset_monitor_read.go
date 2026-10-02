// D-335 S2：XASSET 入口安全边际闸读接口（monitor 单源：entry+noise 同行读）。
// D-423：查询键三元组化（pair, cex_venue, mt_venue）——跨 venue 借用他所簿统计=伪安全
// 边际/伪收割线；调用方按仓/机会的 venue 直查，查不到=诚实缺（fail-closed 不借用）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LatestXAssetEntryGate 读该 (pair,cexVenue,mtVenue) 最新行的 entry_spread_bps+
// noise_bps+harvest_floor_bps（D-341 S1 扩展：收割线同行读；NULL→hasFloor=false）。
// 无行、行龄>maxAge、noise_bps IS NULL（样本<12 未成噪）→ ok=false。
func (s *Store) LatestXAssetEntryGate(ctx context.Context, pair, cexVenue, mtVenue string, maxAge time.Duration) (entryBps, noiseBps, harvestFloorBps float64, hasFloor, ok bool, err error) {
	var entry, noise, floor *float64
	var ts time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT entry_spread_bps, noise_bps, harvest_floor_bps, ts
		FROM fin_xasset_monitor
		WHERE pair = $1 AND cex_venue = $2 AND mt_venue = $3
		ORDER BY ts DESC
		LIMIT 1`, pair, cexVenue, mtVenue).Scan(&entry, &noise, &floor, &ts)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, 0, 0, false, false, nil
		}
		return 0, 0, 0, false, false, fmt.Errorf("latest xasset entry gate: %w", err)
	}
	if entry == nil || noise == nil || time.Since(ts) > maxAge {
		return 0, 0, 0, false, false, nil
	}
	if floor != nil {
		return *entry, *noise, *floor, true, true, nil
	}
	return *entry, *noise, 0, false, true, nil
}

// XAssetMonitorRowNear D-404 S1a（R1 返修版）：开仓时点 monitor 行回放——取
// **ts <= opened_at 的最新行**的 entry_spread_bps（冻结锚 floor/noise 由
// EnsureEntryGateSnapshot 写自开仓时点之前最新行，capture 必须与该行同源配对；
// |Δt| 最近行会取到开仓后样本=跨样本混配，498 实证 capture 转负→line 比锚宽误杀）。
// 行龄>maxGap（opened_at−rowTs）/无行 → ok=false（调用方回退代理路径，不用错尺度）。
// D-423：查询键三元组化（pair,cexVenue,mtVenue）。
func (s *Store) XAssetMonitorRowNear(ctx context.Context, pair, cexVenue, mtVenue string, ts time.Time, maxGap time.Duration) (entrySpreadBps float64, ok bool, err error) {
	var rowTs time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT entry_spread_bps, ts
		FROM fin_xasset_monitor
		WHERE pair = $1 AND cex_venue = $2 AND mt_venue = $3 AND ts <= $4
		ORDER BY ts DESC
		LIMIT 1`, pair, cexVenue, mtVenue, ts).Scan(&entrySpreadBps, &rowTs)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("xasset monitor row near: %w", err)
	}
	if ts.Sub(rowTs) > maxGap {
		return 0, false, nil
	}
	return entrySpreadBps, true, nil
}

// HasXAssetMonitorRow D-419 S3：该 (pair,cexVenue,mtVenue) 三元组是否曾有 monitor 行
// （纯存在性无 maxAge——「簿从未有过行」与「簿陈旧」是不同事实，本哨兵只报前者；
// 观测件调用方查询失败 fail-open 跳过不告警）。
func (s *Store) HasXAssetMonitorRow(ctx context.Context, pair, cexVenue, mtVenue string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM fin_xasset_monitor
		 WHERE pair=$1 AND cex_venue=$2 AND mt_venue=$3 LIMIT 1`,
		pair, cexVenue, mtVenue).Scan(&n)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return false, nil
		}
		return false, fmt.Errorf("has xasset monitor row: %w", err)
	}
	return true, nil
}

// EnsureEntryGateSnapshot D-341 S1：开仓学习样本锚——两写点（D-327 同位）后调用，
// 读开仓时点 monitor 最新行落 positions.entry_noise_bps/entry_harvest_floor_bps（IS NULL 幂等）。
// monitor 缺行/未成噪 → 不写（学习锚 NULL=诚实缺样本，收割出口走 NULL 不触发语义）。
// D-423：按 (pair,cexVenue,mtVenue) 三元组查——venue 键不可得传空串=自然 miss 不写。
func (s *Store) EnsureEntryGateSnapshot(ctx context.Context, posID int64, pair, cexVenue, mtVenue string, maxAge time.Duration) error {
	entry, noise, floor, hasFloor, ok, err := s.LatestXAssetEntryGate(ctx, pair, cexVenue, mtVenue, maxAge)
	if err != nil || !ok {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE positions SET entry_noise_bps = $1, entry_harvest_floor_bps = $2
		 WHERE id = $3 AND entry_noise_bps IS NULL`,
		noise, func() *float64 {
			if hasFloor {
				return &floor
			}
			return nil
		}(), posID)
	if err != nil {
		return fmt.Errorf("entry gate snapshot: %w", err)
	}
	_ = entry
	return nil
}

// UpdatePositionExitKind D-341 S3：周期学习样本——exit_kind（harvest/stop/carry_death/manual）
// 单写点=logCloseTrigger reason 前缀映射 + manual RPC。IS NULL 幂等（首因为准）。
func (s *Store) UpdatePositionExitKind(ctx context.Context, posID int64, kind string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE positions SET exit_kind = $1 WHERE id = $2 AND exit_kind IS NULL`, kind, posID)
	return err
}

// ExitKindFromReason D-341 S3：close reason 前缀 → exit_kind（logCloseTrigger 单映射点）。
// D-013 ③：返回值全部走权威常量；legged_unwind/incident_close 不经此函数（终态归因覆盖写）。
func ExitKindFromReason(reason string) string {
	switch {
	case strings.HasPrefix(reason, "disaster"): // D-378b：置于 contains 族前——reason 串含 basis/carry/stop 子串会被截胡
		return ExitKindDisaster
	case strings.HasPrefix(reason, "harvest"):
		return ExitKindHarvest
	case strings.Contains(reason, "basis loss"), strings.Contains(reason, "basis stop"):
		return ExitKindStop
	case strings.Contains(reason, "rotate"): // D-378a 轮转触发同集（含于 exec 质量族，映射先于 carry）
		return ExitKindRotate
	case strings.Contains(reason, "exec_drift"): // D-378a S2：执行质量破产（置于 carry/funding 前，防未来 reason 串扰误判）
		return ExitKindExecDrift
	case strings.Contains(reason, "carry"), strings.Contains(reason, "funding"):
		return ExitKindCarryDeath
	case strings.Contains(reason, "manual"):
		return ExitKindManual
	default:
		return ExitKindOther
	}
}
