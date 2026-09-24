// venue_symbol_flags.go D-463 S1：fin_venue_symbol_flags 读写面
// （KB→pipeline 结构化投影；KB 叙事不直读交易路径——机器只读投影行）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// VenueSymbolFlag venue×symbol 结构化旗标。level：blocked=纸面入口硬拦；
// warn=毕业建议带旗不拦；resolved_at 非 NULL=已解（不再生效，留审计史）。
type VenueSymbolFlag struct {
	ID         int64
	Venue      string
	Symbol     string
	Level      string
	Reason     string
	KbRef      *string
	CreatedAt  time.Time
	ResolvedAt *time.Time
}

// FlagKey 旗标查找键（venue|symbol；scan 侧消费同一格式）。
func FlagKey(venue, symbol string) string { return venue + "|" + symbol }

// ListActiveFlags 未解旗标全集（resolved_at IS NULL；每轮扫描开头一次载入，
// 判定层 map 消费不逐行查）。
func (s *Store) ListActiveFlags(ctx context.Context) (map[string]VenueSymbolFlag, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, venue, symbol, level, reason, kb_ref, created_at, resolved_at
		FROM fin_venue_symbol_flags WHERE resolved_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list venue symbol flags: %w", err)
	}
	defer rows.Close()
	out := map[string]VenueSymbolFlag{}
	for rows.Next() {
		var f VenueSymbolFlag
		if err := rows.Scan(&f.ID, &f.Venue, &f.Symbol, &f.Level, &f.Reason, &f.KbRef, &f.CreatedAt, &f.ResolvedAt); err != nil {
			return nil, fmt.Errorf("scan venue symbol flag: %w", err)
		}
		out[FlagKey(f.Venue, f.Symbol)] = f
	}
	return out, rows.Err()
}

// InsertFlag 插旗标行（reject 回写/手工落行同口；level 由 CHECK 约束收口）。
func (s *Store) InsertFlag(ctx context.Context, f *VenueSymbolFlag) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_venue_symbol_flags (venue, symbol, level, reason, kb_ref)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		f.Venue, f.Symbol, f.Level, f.Reason, f.KbRef).Scan(&f.ID)
	if err != nil {
		return fmt.Errorf("insert venue symbol flag: %w", err)
	}
	return nil
}

// ResolveFlag 软解旗标（resolved_at=now；审计史保留）。
func (s *Store) ResolveFlag(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE fin_venue_symbol_flags SET resolved_at = now() WHERE id = $1 AND resolved_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("resolve venue symbol flag: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("resolve venue symbol flag: id %d 不存在或已解", id)
	}
	return nil
}
