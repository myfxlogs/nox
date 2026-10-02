// exchange_configs 存储（docs/CEX交易所后台管理.md D-054）。
// 凭据明文存储（业主拍板，同 broker_accounts）；列表 RPC 只回掩码，secret 不回传（D-043）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/cryptoutil"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ExchangeConfig exchange_configs 行。
type ExchangeConfig struct {
	ID         int64
	Venue      string // binance / okx
	VenueType  string // spot
	Label      string
	APIKey     string
	APISecret  string
	Passphrase string
	BaseURL    string
	Simulated  bool   // 模拟盘标记位（D-516：demo 域收编后仅存数据语义——消费端 fail-closed 跳过）
	Website    string // D-157 V3：交易所官网（DB 存储；空 = 未设置）
	CreatedAt  time.Time
}

// InsertExchangeConfig 插入交易所配置并回填 id、created_at；venue+venue_type 冲突返回错误（UNIQUE 约束）。
func (s *Store) InsertExchangeConfig(ctx context.Context, c *ExchangeConfig) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at
	`, c.Venue, c.VenueType, c.Label, c.APIKey, c.APISecret, c.Passphrase, c.BaseURL, c.Simulated, c.Website).Scan(&c.ID, &c.CreatedAt)
	return err
}

// ListExchangeConfigs 全量交易所配置（按创建时间序）。
// D-515：enc 列存在 → 解密回填 APIKey/APISecret/Passphrase（读面全走解密，明文列恒空）；
// enc 行存在但 ARB_MASTER_KEY 缺失/解失败 → error 上抛（fail-closed，调用方 boot fatal）。
func (s *Store) ListExchangeConfigs(ctx context.Context) ([]ExchangeConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, created_at, website,
		       api_key_enc, api_secret_enc, passphrase_enc
		FROM exchange_configs ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExchangeConfig
	var key []byte
	keyReady := false
	for rows.Next() {
		var c ExchangeConfig
		var kEnc, sEnc, pEnc []byte
		if err := rows.Scan(&c.ID, &c.Venue, &c.VenueType, &c.Label,
			&c.APIKey, &c.APISecret, &c.Passphrase, &c.BaseURL, &c.Simulated, &c.CreatedAt, &c.Website,
			&kEnc, &sEnc, &pEnc); err != nil {
			return nil, err
		}
		if kEnc != nil || sEnc != nil || pEnc != nil {
			if !keyReady {
				key, keyReady = cryptoutil.MasterKey()
			}
			if !keyReady {
				return nil, fmt.Errorf("exchange_configs enc rows present but ARB_MASTER_KEY missing (fail-closed)")
			}
			if c.APIKey, err = openOrEmpty(key, kEnc); err != nil {
				return nil, fmt.Errorf("exchange_config %d api_key: %w", c.ID, err)
			}
			if c.APISecret, err = openOrEmpty(key, sEnc); err != nil {
				return nil, fmt.Errorf("exchange_config %d api_secret: %w", c.ID, err)
			}
			if c.Passphrase, err = openOrEmpty(key, pEnc); err != nil {
				return nil, fmt.Errorf("exchange_config %d passphrase: %w", c.ID, err)
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// openOrEmpty enc=NULL → ""；非空 → Open。
func openOrEmpty(key, blob []byte) (string, error) {
	if blob == nil {
		return "", nil
	}
	return cryptoutil.Open(key, blob)
}

// SealExchangeKeys boot 自封（D-515 §2）：明文列非空且 enc 空 → 加密写回+明文置 ”
// （T8 审计留痕=列保留）。幂等：enc 已置位行跳过；enc 空且明文空=未配凭据跳过。
// MASTER_KEY 缺失且存在需封行 → error（调用方 boot fatal）。
func (s *Store) SealExchangeKeys(ctx context.Context) (sealed int, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, api_key, api_secret, passphrase FROM exchange_configs
		WHERE (api_key <> '' AND api_key_enc IS NULL)
		   OR (api_secret <> '' AND api_secret_enc IS NULL)
		   OR (COALESCE(passphrase,'') <> '' AND passphrase_enc IS NULL)`)
	if err != nil {
		return 0, fmt.Errorf("seal scan: %w", err)
	}
	type row struct {
		id      int64
		k, s, p string
	}
	var pending []row
	for rows.Next() {
		var r row
		var pass *string
		if err := rows.Scan(&r.id, &r.k, &r.s, &pass); err != nil {
			rows.Close()
			return 0, err
		}
		if pass != nil {
			r.p = *pass
		}
		pending = append(pending, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	key, ok := cryptoutil.MasterKey()
	if !ok {
		return 0, fmt.Errorf("%d exchange_configs rows need sealing but ARB_MASTER_KEY missing (fail-closed)", len(pending))
	}
	seal := func(plain string) ([]byte, error) {
		if plain == "" {
			return nil, nil
		}
		return cryptoutil.Seal(key, plain)
	}
	for _, r := range pending {
		kEnc, err := seal(r.k)
		if err != nil {
			return sealed, err
		}
		sEnc, err := seal(r.s)
		if err != nil {
			return sealed, err
		}
		pEnc, err := seal(r.p)
		if err != nil {
			return sealed, err
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE exchange_configs SET
				api_key_enc = COALESCE(api_key_enc, $2),
				api_secret_enc = COALESCE(api_secret_enc, $3),
				passphrase_enc = COALESCE(passphrase_enc, $4),
				api_key = '', api_secret = '', passphrase = ''
			WHERE id = $1`, r.id, kEnc, sEnc, pEnc); err != nil {
			return sealed, fmt.Errorf("seal id=%d: %w", r.id, err)
		}
		sealed++
	}
	return sealed, nil
}

// DeleteExchangeConfig 按 id 删除。
func (s *Store) DeleteExchangeConfig(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM exchange_configs WHERE id = $1`, id)
	return err
}

// ErrExchangeConflict UNIQUE 冲突判定。
func ErrExchangeConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// SetExchangeLabel 设置交易所别名；空串 = 清除（D-157 V3）。
func (s *Store) SetExchangeLabel(ctx context.Context, id int64, label string) error {
	_, err := s.pool.Exec(ctx, `UPDATE exchange_configs SET label = $1 WHERE id = $2`, label, id)
	return err
}

// SetExchangeWebsite 设置交易所官网；空串 = 清除（D-157 V3.1）。
func (s *Store) SetExchangeWebsite(ctx context.Context, id int64, website string) error {
	_, err := s.pool.Exec(ctx, `UPDATE exchange_configs SET website = $1 WHERE id = $2`, website, id)
	return err
}

// ExchangeTakerFeeBps D-014 S2：CEX 腿 econ 费率源——exchange_configs.taker_fee_bps
// （venue 实测值，业主可改）。venue+venue_type 严格匹配；无行/NULL → ok=false fail-closed。
func (s *Store) ExchangeTakerFeeBps(ctx context.Context, venue, venueType string) (float64, bool, error) {
	var v *float64
	err := s.pool.QueryRow(ctx,
		`SELECT taker_fee_bps FROM exchange_configs WHERE venue=$1 AND venue_type=$2
		 ORDER BY created_at DESC, id DESC LIMIT 1`, venue, venueType).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

// ExchangeMakerFeeBps D-018 后段：CEX 腿 maker 费率源——exchange_configs.maker_fee_bps
// （venue 官方费率页事实）。venue+venue_type 严格匹配；无行/NULL → ok=false
// （调用方回落 taker 保守口径，不 fail-closed）。
func (s *Store) ExchangeMakerFeeBps(ctx context.Context, venue, venueType string) (float64, bool, error) {
	var v *float64
	err := s.pool.QueryRow(ctx,
		`SELECT maker_fee_bps FROM exchange_configs WHERE venue=$1 AND venue_type=$2
		 ORDER BY created_at DESC, id DESC LIMIT 1`, venue, venueType).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}
