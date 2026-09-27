// app_secrets.go —— D-515 §1.2：密钥类配置密文 KV 通道（AES-256-GCM nonce‖ct）。
// 闸门：value 永不进日志/错误串；Load 失败/MASTER_KEY 缺+表非空 → 调用方 boot fatal。
package pgstore

import (
	"context"
	"fmt"

	"arb/internal/cryptoutil"
)

// LoadAppSecrets 全表解密读出 KV。表非空且 ARB_MASTER_KEY 缺 → error（fail-closed）；
// 单行解密失败 → error（错 key/篡改=配置不可用）。错误串只带 key 名不带值。
func (s *Store) LoadAppSecrets(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value_enc FROM app_secrets`)
	if err != nil {
		return nil, fmt.Errorf("load app secrets: %w", err)
	}
	defer rows.Close()
	var key []byte
	keyReady := false
	out := make(map[string]string)
	for rows.Next() {
		var k string
		var enc []byte
		if err := rows.Scan(&k, &enc); err != nil {
			return nil, err
		}
		if !keyReady {
			key, _ = cryptoutil.MasterKey()
			keyReady = true
			if key == nil {
				rows.Close()
				return nil, fmt.Errorf("app_secrets non-empty but ARB_MASTER_KEY missing (fail-closed)")
			}
		}
		v, err := cryptoutil.Open(key, enc)
		if err != nil {
			return nil, fmt.Errorf("app_secrets key %s: %w", k, err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// UpsertAppSecret 写/更新单键密文（ON CONFLICT 更新 value_enc+updated_at+note）。
// seedConfigFromEnv/运维 SQL 通道用；调用方负责加密，本函数不触明文。
func (s *Store) UpsertAppSecret(ctx context.Context, key string, valueEnc []byte, note string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app_secrets (key, value_enc, note) VALUES ($1,$2,$3)
		 ON CONFLICT (key) DO UPDATE SET value_enc=EXCLUDED.value_enc, note=EXCLUDED.note, updated_at=now()`,
		key, valueEnc, note)
	return err
}

// AppSecretsEmpty 表空判定（D-515 S4 播种闸）。
func (s *Store) AppSecretsEmpty(ctx context.Context) (bool, error) {
	var empty bool
	err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM app_secrets)`).Scan(&empty)
	return empty, err
}

// InsertAppSecretIfAbsent 播种写入（ON CONFLICT DO NOTHING）。
func (s *Store) InsertAppSecretIfAbsent(ctx context.Context, key string, valueEnc []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app_secrets (key, value_enc) VALUES ($1,$2) ON CONFLICT (key) DO NOTHING`, key, valueEnc)
	return err
}
