-- 1039_combo_source.sql —— D-584 真金门槛补洞①：armed 白名单出处约束。
-- 病灶实证（SG 10-16）：xasset_allow_combos 零血缘列——直插即白名单绕过
-- 发现→纸面→毕业全管道；#1-4 demo 种子行 venue 已拆仍 enabled。
-- ①source CHECK 枚举（graduation|s2_trial|manual_seed）+provenance_id 引用列；
-- ②enable 闸=NULL 出处行禁 enabled（CHECK 钉死——写径复检同判）；
-- ③存量回填如实：TRX #17-19=graduation 出处（D-403 批准链在案）；其余
--   无毕业实据行 manual_seed 如实（禁造假）。

ALTER TABLE xasset_allow_combos
    ADD COLUMN IF NOT EXISTS source text,
    ADD COLUMN IF NOT EXISTS provenance_id bigint;

ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_source_check;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_source_check
    CHECK (source IS NULL OR source IN ('graduation','s2_trial','manual_seed'));

-- 存量回填如实（复审对位面）：SG 实拍 carry_graduations #17/#18/#19 approved
-- 三行 ↔ 白名单 combos #46/#47/#48 venue 对位——显式 id 映射钉死。
UPDATE xasset_allow_combos SET source='graduation', provenance_id=v.gid
    FROM (VALUES (46,17),(47,18),(48,19)) AS v(id,gid)
    WHERE xasset_allow_combos.id=v.id AND source IS NULL;
-- 兜底：symbol+venue 双对位（id 漂移库面——graduation venue_a/b ↔ combos
-- venue_buy/sell 名段）；无毕业实据行不碰（manual_seed 如实）。
-- 无毕业实据存量行=manual_seed 如实（不造假 graduation 出处）
UPDATE xasset_allow_combos SET source='manual_seed' WHERE source IS NULL;

-- enable 闸：NULL 出处行禁 enabled（DB 钉死——回填之后挂闸存量行先合规）。
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_source_required;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_source_required
    CHECK (NOT enabled OR source IS NOT NULL);
