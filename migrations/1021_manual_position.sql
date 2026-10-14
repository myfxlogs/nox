-- 1021_manual_position.sql —— D-512：CMC 手工仓位只读纳管。
-- 业主手工域（cmc 等无 API venue）仓位行：录入→资金全景可见（src='manual'），
-- 不进敞口/浓度/风控/执行径。不编值——price_usd 缺省=未知价如实空态。
CREATE TABLE IF NOT EXISTS fin_manual_position (
    id bigserial PRIMARY KEY,
    venue_label text NOT NULL DEFAULT 'manual',
    symbol text NOT NULL,
    qty numeric NOT NULL CHECK (qty > 0),
    price_usd numeric CHECK (price_usd IS NULL OR price_usd > 0),
    note text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE fin_manual_position IS 'D-512：业主手工仓位只读台账（src=manual）——只读展示域，不进敞口计算不风控不追踪';
