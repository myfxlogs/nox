-- 0151_config_to_pg.sql —— D-515 配置迁 PG（设计稿 §1-3）：
-- ①app_secrets 密文 KV 通道（overlay 解密注入，MASTER_KEY fail-closed）；
-- ②exchange_configs 密钥三列加密（AES-256-GCM enc 列，明文列保留清空留痕）；
-- ③swaparb_allow_combos 表化热读（kind=allow/deny 双轨同表）。

CREATE TABLE IF NOT EXISTS app_secrets (
  key        TEXT PRIMARY KEY,
  value_enc  BYTEA NOT NULL,
  note       TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE app_secrets IS 'D-515：密钥类配置密文通道（AES-256-GCM nonce‖ct，key=env ARB_MASTER_KEY；禁写日志禁回显）';
COMMENT ON COLUMN app_secrets.key IS 'env 键名（FEISHU_*/AI_*/LONGBRIDGE_* 等密钥类）';
COMMENT ON COLUMN app_secrets.value_enc IS 'AES-256-GCM 密文（nonce 前缀拼接）';
COMMENT ON COLUMN app_secrets.note IS '运维注记';

ALTER TABLE exchange_configs
  ADD COLUMN IF NOT EXISTS api_key_enc BYTEA,
  ADD COLUMN IF NOT EXISTS api_secret_enc BYTEA,
  ADD COLUMN IF NOT EXISTS passphrase_enc BYTEA;
COMMENT ON COLUMN exchange_configs.api_key_enc IS 'D-515：AES-256-GCM 密文（明文 api_key 清空后唯一读源；NULL=未自封）';
COMMENT ON COLUMN exchange_configs.api_secret_enc IS 'D-515：AES-256-GCM 密文';
COMMENT ON COLUMN exchange_configs.passphrase_enc IS 'D-515：AES-256-GCM 密文';

CREATE TABLE IF NOT EXISTS swaparb_allow_combos (
  id                     BIGSERIAL PRIMARY KEY,
  kind                   TEXT NOT NULL CHECK (kind IN ('allow','deny')),
  canonical              TEXT NOT NULL,
  venue_long             TEXT NOT NULL,
  venue_short            TEXT NOT NULL,
  max_long_qty           NUMERIC(20,8),
  max_short_qty          NUMERIC(20,8),
  max_gross_exposure_usd NUMERIC(20,8),
  enabled                BOOL NOT NULL DEFAULT true,
  note                   TEXT NOT NULL DEFAULT '',
  created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (kind, canonical, venue_long, venue_short)
);
COMMENT ON TABLE swaparb_allow_combos IS 'D-515：SWAPARB 组合门禁表化热读（configKick+60s 快照；env 字符串面停读）';
COMMENT ON COLUMN swaparb_allow_combos.kind IS 'allow/deny 双轨同表';
COMMENT ON COLUMN swaparb_allow_combos.canonical IS '规范符号键';
COMMENT ON COLUMN swaparb_allow_combos.venue_long IS '买腿 venue_key（account:mt4/mt5）';
COMMENT ON COLUMN swaparb_allow_combos.venue_short IS '卖腿 venue_key';
COMMENT ON COLUMN swaparb_allow_combos.max_long_qty IS '买腿量上限（NULL/≤0=不限）';
COMMENT ON COLUMN swaparb_allow_combos.max_short_qty IS '卖腿量上限';
COMMENT ON COLUMN swaparb_allow_combos.max_gross_exposure_usd IS '毛敞口上限 USD';
COMMENT ON COLUMN swaparb_allow_combos.enabled IS '行开关（false=行不生效不删行）';
