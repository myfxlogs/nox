// app_config.go —— D-368 S2：运维旋钮 KV 表读写（app_config，Bucket B 首批）。
// Load 失败由调用方 startup fatal（fail-closed：配置源读不出=不开，同 D-338 白名单语义）；
// Upsert 为运维 SQL 通道备用封装（RPC 写通道留后续）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
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

// AppConfigInt64 D-032 ②：单键整型直读（事故仓 ack 游标等热生效键——
// UpdateAppConfig 写库即效，不等重启）。行缺 → ok=false（调用方按缺省语义处理）；值坏 → error。
func (s *Store) AppConfigInt64(ctx context.Context, key string) (int64, bool, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_config WHERE key = $1`, key).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("app config int64 %s: %w", key, err)
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, false, fmt.Errorf("app config int64 %s: parse %q: %w", key, v, perr)
	}
	return n, true, nil
}

// AppConfigFloat 单键浮点直读（D-048：探针名义上限热读；行缺→ok=false，值坏→error）。
func (s *Store) AppConfigFloat(ctx context.Context, key string) (float64, bool, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_config WHERE key = $1`, key).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("app config float %s: %w", key, err)
	}
	f, perr := strconv.ParseFloat(v, 64)
	if perr != nil {
		return 0, false, fmt.Errorf("app config float %s: parse %q: %w", key, v, perr)
	}
	return f, true, nil
}

// AppConfigRow 全列行（D-523 键视图：note/updated_at 透出审计）。
type AppConfigRow struct {
	Key       string
	Value     string
	Note      string
	UpdatedAt time.Time
}

// ListAppConfigRows 全列读出（管理面用；LoadAppConfig 是 boot 轻量面不动）。
func (s *Store) ListAppConfigRows(ctx context.Context) ([]AppConfigRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value, COALESCE(note, ''), updated_at FROM app_config ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list app config rows: %w", err)
	}
	defer rows.Close()
	var out []AppConfigRow
	for rows.Next() {
		var r AppConfigRow
		if err := rows.Scan(&r.Key, &r.Value, &r.Note, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
