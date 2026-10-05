-- 0179_retire_universe_alerts.sql D-072：「真候选浮现」发现告警退役——
-- 发射点已删；本迁移清历史 info 行（含浮动数字 %.2f%%
-- 致 D-592 同键去重永不命中，实拍 7 天 1143 条、峰值 310 条/天=纯噪音）。
-- 幂等可重放。
DELETE FROM alerts WHERE level='info' AND message LIKE '[carry 宇宙] 真候选浮现%';
