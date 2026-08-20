-- D-068: 添加 PERP_FUNDING 类型到 opportunities type 约束
-- D-064 perp×perp funding 检测器产出 PERP_FUNDING 类型，但 DB 约束未更新导致 upsert 失败
ALTER TABLE opportunities DROP CONSTRAINT IF EXISTS opportunities_type_check;
ALTER TABLE opportunities ADD CONSTRAINT opportunities_type_check CHECK (type IN ('CROSS_VENUE','CARRY','FUNDING_HEDGE','PERP_FUNDING'));
