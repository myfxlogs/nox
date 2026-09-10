-- D-271 R1 返修：exchange_configs 唯一约束加 simulated 列，允许 demo+main 共存。
-- 根因：迁移 0010 建表时 UNIQUE(venue, venue_type)，D-271 迁移 0083 INSERT 主网行被 ON CONFLICT 跳过。

-- 1. 删旧唯一约束
ALTER TABLE exchange_configs DROP CONSTRAINT IF EXISTS exchange_configs_venue_venue_type_key;

-- 2. 加新唯一约束（含 simulated）
ALTER TABLE exchange_configs ADD CONSTRAINT exchange_configs_venue_venue_type_simulated_key UNIQUE (venue, venue_type, simulated);

-- 3. INSERT 主网只读行（凭据列留空，业主手动填）
INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
VALUES
    ('okx', 'spot', 'OKX 主网只读', '', '', '', '', false, 'https://www.okx.com'),
    ('okx', 'perp', 'OKX 主网只读', '', '', '', '', false, 'https://www.okx.com'),
    ('binance', 'spot', 'Binance 主网只读', '', '', '', 'https://192.229.85.35:8443/api', false, 'https://www.binance.com'),
    ('binance', 'perp', 'Binance 主网只读', '', '', '', 'https://192.229.85.35:8443/fapi', false, 'https://www.binance.com')
ON CONFLICT (venue, venue_type, simulated) DO NOTHING;
