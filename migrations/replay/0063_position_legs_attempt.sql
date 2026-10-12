-- 0063_position_legs_attempt.sql - D-195-B §4.2：position_legs attempt 字段扩展
-- 同一 leg 可多次尝试（原唯一索引 (position_id, leg_no, phase) 改为含 attempt_no）

ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS attempt_no    SMALLINT NOT NULL DEFAULT 1;
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS parent_attempt_id BIGINT;
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS client_order_id TEXT NOT NULL DEFAULT '';
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS requested_qty  DOUBLE PRECISION;
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS filled_qty     DOUBLE PRECISION;
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS avg_fill_price DOUBLE PRECISION;
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS fee_raw        TEXT NOT NULL DEFAULT '';
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS fee_usd        DOUBLE PRECISION;
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS submitted_at   TIMESTAMPTZ;
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS uncertain_at   TIMESTAMPTZ;

-- 删除旧唯一索引 (position_id, leg_no, phase)，替换为含 attempt_no
DROP INDEX IF EXISTS position_legs_pos_leg_phase_idx;
CREATE UNIQUE INDEX IF NOT EXISTS position_legs_pos_leg_phase_attempt_idx
    ON position_legs (position_id, leg_no, phase, attempt_no);

-- client_order_id 幂等索引（venue 支持时非空）
CREATE UNIQUE INDEX IF NOT EXISTS position_legs_client_order_id_idx
    ON position_legs (venue, account, client_order_id) WHERE client_order_id != '';
