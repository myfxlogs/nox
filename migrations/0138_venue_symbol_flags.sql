-- 0138_venue_symbol_flags.sql - D-463 S1/S2：KB→pipeline 投影表 + 毕业建议 flag_warn 列。
-- fin_venue_symbol_flags：venue×symbol 结构化旗标（KB 叙事不直读交易路径；
-- 消费侧口径：venue=facts/纸面行 venue 裸名（cex 如 'gate'，MT 如 '3001157'，
-- 组合行=全串 'mtA×mtB' 或腿段）；symbol=canonical（spot 轨）或行 symbol。
-- level：blocked=纸面入口硬拦；warn=毕业建议带旗不拦。resolved_at 软删保审计史。
CREATE TABLE IF NOT EXISTS fin_venue_symbol_flags (
	id          bigserial PRIMARY KEY,
	venue       text NOT NULL,
	symbol      text NOT NULL,
	level       text NOT NULL CHECK (level IN ('blocked','warn')),
	reason      text NOT NULL,
	kb_ref      text,
	created_at  timestamptz NOT NULL DEFAULT now(),
	resolved_at timestamptz
);
CREATE INDEX IF NOT EXISTS fin_venue_symbol_flags_active_idx
	ON fin_venue_symbol_flags (venue, symbol) WHERE resolved_at IS NULL;

-- carry_graduations.flag_warn：毕业建议行红旗透出（warn 命中时=拼接 reason 短串）。
ALTER TABLE carry_graduations ADD COLUMN IF NOT EXISTS flag_warn text;
