package pgstore

import "context"

// GetRadarDigests D-389 S2：kind→content_hash 全量读出（digest 边沿判等输入）。
func (s *Store) GetRadarDigests(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, content_hash FROM radar_digest_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, h string
		if err := rows.Scan(&k, &h); err != nil {
			return nil, err
		}
		m[k] = h
	}
	return m, rows.Err()
}

// UpsertRadarDigest D-389 S2：推送成功后落集合 hash（幂等 upsert）。
func (s *Store) UpsertRadarDigest(ctx context.Context, kind, hash string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO radar_digest_state (kind, content_hash) VALUES ($1,$2)
		 ON CONFLICT (kind) DO UPDATE SET content_hash=$2, pushed_at=now()`, kind, hash)
	return err
}
