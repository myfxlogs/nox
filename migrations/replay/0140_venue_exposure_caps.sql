-- 0140 D-475a S1+S5：venue 敞口闸表（真金 approve 闸载体，业主裁 PG 表禁 env）+
-- 纸面行质量杆观测两列（accrue_ticks=实结期数 / min_accrued_usd=存续期最低水位，观测版零拒批）。

CREATE TABLE IF NOT EXISTS fin_venue_exposure_caps (
    venue_key   TEXT PRIMARY KEY,                          -- 账户级键：MT='3001157:mt5'，CEX='okx:perp'
    cap_usd     NUMERIC NOT NULL CHECK (cap_usd >= 0),     -- 0=显式全禁（冻结语义）
    enabled     BOOLEAN NOT NULL DEFAULT true,             -- false=停用该闸（行仍在档）
    note        TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE paper_spot_carry
    ADD COLUMN IF NOT EXISTS accrue_ticks INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS min_accrued_usd NUMERIC;
