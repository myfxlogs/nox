// swaparb_allow.go —— D-515 §3：SWAPARB 组合门禁表化热读（kind=allow/deny 双轨同表）。
// 语义保真：deny 集/空 allow=fail-closed/AutoAllow 默认 false 全不动（消费侧语义归 exec/
// autotrade，本层只管行存取）。带外 SQL 改行由 kick+60s ticker 续命（D-443/D-469b 先例）。
package pgstore

import (
	"context"
	"fmt"
)

// SwapArbAllowRow swaparb_allow_combos 行（限额 NULL=不限，与 env 解析空字段=0 语义对齐）。
type SwapArbAllowRow struct {
	ID          int64
	Kind        string // allow/deny
	Canonical   string
	VenueLong   string
	VenueShort  string
	MaxLongQty  *float64
	MaxShortQty *float64
	MaxGrossUSD *float64
	Note        string
}

// LoadSwapArbAllowCombos 执行视图（enabled=true 行，按 kind 分流）。读失败 → error
// （调用方 fail-closed：白名单读不出=不开）。
func (s *Store) LoadSwapArbAllowCombos(ctx context.Context) (allow, deny []SwapArbAllowRow, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, canonical, venue_long, venue_short,
		       max_long_qty, max_short_qty, max_gross_exposure_usd, note
		FROM swaparb_allow_combos WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("load swaparb_allow_combos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r SwapArbAllowRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Canonical, &r.VenueLong, &r.VenueShort,
			&r.MaxLongQty, &r.MaxShortQty, &r.MaxGrossUSD, &r.Note); err != nil {
			return nil, nil, err
		}
		switch r.Kind {
		case "allow":
			allow = append(allow, r)
		case "deny":
			deny = append(deny, r)
		}
	}
	return allow, deny, rows.Err()
}

// SwapArbAllowCombosEmpty 表空判定（D-515 S5 播种闸：仅空表播种一次）。
func (s *Store) SwapArbAllowCombosEmpty(ctx context.Context) (bool, error) {
	var empty bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM swaparb_allow_combos)`).Scan(&empty)
	return empty, err
}

// InsertSwapArbAllowCombo 播种写入（ON CONFLICT 唯一键 DO NOTHING——幂等）。
func (s *Store) InsertSwapArbAllowCombo(ctx context.Context, r SwapArbAllowRow) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO swaparb_allow_combos
		  (kind, canonical, venue_long, venue_short, max_long_qty, max_short_qty, max_gross_exposure_usd, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (kind, canonical, venue_long, venue_short) DO NOTHING`,
		r.Kind, r.Canonical, r.VenueLong, r.VenueShort, r.MaxLongQty, r.MaxShortQty, r.MaxGrossUSD, r.Note)
	return err
}
