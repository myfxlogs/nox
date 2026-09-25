// venue_exposure_caps.go D-475a S1：fin_venue_exposure_caps 读写面
// （approve 敞口闸载体；cap=0=冻结新批准，不动存量 enabled 组合——B-016 语义）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// VenueExposureCap venue_key 级敞口上限。cap_usd=0+enabled=冻结新批准；
// enabled=false=闸停用（行仍在档）。
type VenueExposureCap struct {
	VenueKey  string
	CapUSD    float64
	Enabled   bool
	Note      string
	UpdatedAt time.Time
}

// ListVenueExposureCaps 全量载入 map 消费（approve 每单一次载入不逐行查）。
func (s *Store) ListVenueExposureCaps(ctx context.Context) (map[string]VenueExposureCap, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT venue_key, cap_usd, enabled, note, updated_at FROM fin_venue_exposure_caps`)
	if err != nil {
		return nil, fmt.Errorf("list venue exposure caps: %w", err)
	}
	defer rows.Close()
	out := map[string]VenueExposureCap{}
	for rows.Next() {
		var c VenueExposureCap
		if err := rows.Scan(&c.VenueKey, &c.CapUSD, &c.Enabled, &c.Note, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan venue exposure cap: %w", err)
		}
		out[c.VenueKey] = c
	}
	return out, rows.Err()
}

// UpsertVenueExposureCap 写/改闸（负 cap 由 CHECK 拒；updated_at 落写时刻）。
func (s *Store) UpsertVenueExposureCap(ctx context.Context, c *VenueExposureCap) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled, note)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (venue_key) DO UPDATE SET cap_usd=$2, enabled=$3, note=$4, updated_at=now()`,
		c.VenueKey, c.CapUSD, c.Enabled, c.Note)
	if err != nil {
		return fmt.Errorf("upsert venue exposure cap %s: %w", c.VenueKey, err)
	}
	return nil
}
