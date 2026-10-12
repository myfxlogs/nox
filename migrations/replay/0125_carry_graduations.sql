-- D-403 S1：验证毕业链——纸面行毕业建议表（扫描→验证→实盘三段接通的授权建议持久层）。
-- paper_id UNIQUE+级联删（纸面行删→建议随行，无孤儿）；证据列=建议时点快照（KB carry-rate-is-not-expectancy：
-- 速率非期望值，授权决策看证据不搬速率）；combo_id=approve 写入的 xasset_allow_combos.id（NULL=未写/非 cfd 体检分支）。
CREATE TABLE IF NOT EXISTS carry_graduations (
  id               BIGSERIAL PRIMARY KEY,
  paper_id         BIGINT NOT NULL UNIQUE REFERENCES paper_spot_carry(id) ON DELETE CASCADE,
  venue            TEXT NOT NULL,        -- paper 行 venue（cfd=mt×cex 组合串）
  symbol           TEXT NOT NULL,        -- universe canonical（展示用）
  structure        TEXT NOT NULL,        -- long_cfd_short_perp / short_cfd_long_perp / perp_perp / spot_carry
  mt_venue         TEXT,                 -- cfd 行 MT 腿 venue（raw id）；非 cfd NULL
  age_days         NUMERIC NOT NULL,     -- 建议时点证据快照
  accrued_usd      NUMERIC NOT NULL,
  last_funding_ann NUMERIC,
  status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at       TIMESTAMPTZ,
  decided_note     TEXT NOT NULL DEFAULT '',
  combo_id         BIGINT                -- approve 写入的 xasset_allow_combos.id；NULL=无写入
);
