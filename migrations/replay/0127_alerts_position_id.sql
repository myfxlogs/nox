-- D-411 S1：alerts.position_id——持仓级告警关联列（rule_id 保留真规则告警语义；
-- SET NULL=仓删告警历史仍留，复盘价值保留；部分索引只索持仓行）。
ALTER TABLE alerts ADD COLUMN position_id BIGINT REFERENCES positions(id) ON DELETE SET NULL;
CREATE INDEX alerts_position_id_idx ON alerts(position_id) WHERE position_id IS NOT NULL;
