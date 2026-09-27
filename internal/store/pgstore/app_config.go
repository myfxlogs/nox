// app_config.go —— D-368 S2：运维旋钮 KV 表读写（app_config，Bucket B 首批）。
// Load 失败由调用方 startup fatal（fail-closed：配置源读不出=不开，同 D-338 白名单语义）；
// Upsert 为运维 SQL 通道备用封装（RPC 写通道留后续）。
package pgstore

import (
	"context"
	"fmt"
)

// LoadAppConfig 全表读出 KV（量级小，无需分页）。返回 map[key]value。
func (s *Store) LoadAppConfig(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM app_config`)
	if err != nil {
		return nil, fmt.Errorf("load app config: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// UpsertAppConfig 写/更新单键（ON CONFLICT 更新 value+updated_at+note）。运维 SQL 通道用。
func (s *Store) UpsertAppConfig(ctx context.Context, key, value, note string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_config (key, value, note)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now(), note = EXCLUDED.note`,
		key, value, note)
	if err != nil {
		return fmt.Errorf("upsert app config %s: %w", key, err)
	}
	return nil
}

// AppConfigEmpty 表空判定（D-515 S4 播种闸：仅空表播种一次，非空纯读不覆盖）。
func (s *Store) AppConfigEmpty(ctx context.Context) (bool, error) {
	var empty bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM app_config)`).Scan(&empty)
	return empty, err
}

// InsertAppConfigIfAbsent 播种写入（ON CONFLICT DO NOTHING——非空表/已存键不动）。
func (s *Store) InsertAppConfigIfAbsent(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app_config (key, value) VALUES ($1,$2) ON CONFLICT (key) DO NOTHING`, key, value)
	return err
}
