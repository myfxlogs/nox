// D-335 S2：XASSET 入口安全边际闸读接口（monitor 单源：entry+noise 同行读）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LatestXAssetEntryGate 读该 pair 最新行的 entry_spread_bps+noise_bps+harvest_floor_bps
// （D-341 S1 扩展：收割线同行读；NULL→hasFloor=false）。
// 无行、行龄>maxAge、noise_bps IS NULL（样本<12 未成噪）→ ok=false（调用方 fail-open）。
// pair 键以 monitor 工具构造为准（现值 "XAUUSDT-XAU"）。
func (s *Store) LatestXAssetEntryGate(ctx context.Context, pair string, maxAge time.Duration) (entryBps, noiseBps, harvestFloorBps float64, hasFloor, ok bool, err error) {
	var entry, noise, floor *float64
	var ts time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT entry_spread_bps, noise_bps, harvest_floor_bps, ts
		FROM fin_xasset_monitor
		WHERE pair = $1
		ORDER BY ts DESC
		LIMIT 1`, pair).Scan(&entry, &noise, &floor, &ts)
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

// EnsureEntryGateSnapshot D-341 S1：开仓学习样本锚——两写点（D-327 同位）后调用，
// 读开仓时点 monitor 最新行落 positions.entry_noise_bps/entry_harvest_floor_bps（IS NULL 幂等）。
// monitor 缺行/未成噪 → 不写（学习锚 NULL=诚实缺样本，收割出口走 NULL 不触发语义）。
func (s *Store) EnsureEntryGateSnapshot(ctx context.Context, posID int64, pair string, maxAge time.Duration) error {
	entry, noise, floor, hasFloor, ok, err := s.LatestXAssetEntryGate(ctx, pair, maxAge)
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
func ExitKindFromReason(reason string) string {
	switch {
	case strings.HasPrefix(reason, "disaster"): // D-378b：置于 contains 族前——reason 串含 basis/carry/stop 子串会被截胡
		return "disaster"
	case strings.HasPrefix(reason, "harvest"):
		return "harvest"
	case strings.Contains(reason, "basis loss"), strings.Contains(reason, "basis stop"):
		return "stop"
	case strings.Contains(reason, "exec_drift"): // D-378a S2：执行质量破产（置于 carry/funding 前，防未来 reason 串扰误判）
		return "exec_drift"
	case strings.Contains(reason, "carry"), strings.Contains(reason, "funding"):
		return "carry_death"
	case strings.Contains(reason, "manual"):
		return "manual"
	default:
		return "other"
	}
}
