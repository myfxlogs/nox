-- 仓位→template 归组前置（一策略一格抽屉闸）：positions 前向写径带 strategy_key，
-- 判据落表 fin_position_strategy_map（source_kind+source_val→strategy_key，DB 驱动
-- 禁硬编码）：source_kind='structure'=provenance 精确键（paper_spot_carry.structure
-- 即策略定义面——复审粒度病灶修，engine_type 粗于策略会错挂）；'engine_type'=旧
-- 引擎名回落兜底。map 无行→strategy_key NULL→unassigned 如实落未归组格。
ALTER TABLE positions ADD COLUMN IF NOT EXISTS strategy_key TEXT;
CREATE TABLE IF NOT EXISTS fin_position_strategy_map (
  source_kind  TEXT NOT NULL,      -- 'structure' | 'engine_type'
  source_val   TEXT NOT NULL,      -- paper_spot_carry.structure 或 positions.type
  strategy_key TEXT NOT NULL,      -- 归组键=fin_opp_template.template_id 同域值
  PRIMARY KEY (source_kind, source_val)
);
