-- settle 单消费者管线（PraisonAI 终稿①②合并——D-626 域外票）：
-- 机读件→typed 列（存储终裁补钉：reflection 零 JSON，exit_kind 既有列直供指纹）；
-- 人读件→reflection text 固定确定性模板（模板即 schema，可读可 grep）。
ALTER TABLE positions    ADD COLUMN IF NOT EXISTS reflection               TEXT;
ALTER TABLE positions    ADD COLUMN IF NOT EXISTS entry_expected_cost_bps  DOUBLE PRECISION;
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS reflection           TEXT;
