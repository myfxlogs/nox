-- 仓位→template 归组前置（一策略一格抽屉闸）：positions 前向写径带 strategy_key，
-- 判据落表 fin_position_strategy_map（engine_type→strategy_key，DB 驱动禁硬编码）；
-- position_legs 空表零回填债；map 无行→strategy_key NULL→unassigned 如实落未归组格。
ALTER TABLE positions ADD COLUMN IF NOT EXISTS strategy_key TEXT;
CREATE TABLE IF NOT EXISTS fin_position_strategy_map (
  engine_type  TEXT PRIMARY KEY,   -- positions.type（旧引擎名空间）
  strategy_key TEXT NOT NULL       -- 归组键=fin_opp_template.template_id 同域值
);
