package pgstore

import (
	"context"
	"fmt"
)

// VenuesByCanonical D-170：承载该 canonical 的全部 venue（broker_symbol_map 去重列）。
// marketstate 跨 venue 参照系——独苗=单 venue，≥2=可互证。
func (s *Store) VenuesByCanonical(ctx context.Context, canonical string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT venue FROM broker_symbol_map WHERE canonical = $1`, canonical)
	if err != nil {
		return nil, fmt.Errorf("venues by canonical %s: %w", canonical, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
