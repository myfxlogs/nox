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

-- 存量回填如实：TRX #17-19（carry_graduations #17/#18/#19 毕业批准链在案）
UPDATE xasset_allow_combos SET source='graduation', provenance_id=17
    WHERE canonical_buy='TRX' AND id IN (17,18,19) AND source IS NULL;
-- 兜底：id 漂移场景按行序对位（approved graduation 行 id 集）
UPDATE xasset_allow_combos a SET source='graduation',
    provenance_id=(SELECT min(g.id) FROM carry_graduations g
        WHERE g.status='approved' AND g.symbol=a.canonical_buy)
    WHERE a.source IS NULL
      AND EXISTS (SELECT 1 FROM carry_graduations g
        WHERE g.status='approved' AND g.symbol=a.canonical_buy);
-- 无毕业实据存量行=manual_seed 如实（不造假 graduation 出处）
UPDATE xasset_allow_combos SET source='manual_seed' WHERE source IS NULL;

-- enable 闸：NULL 出处行禁 enabled（DB 钉死——回填之后挂闸存量行先合规）。
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_source_required;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_source_required
    CHECK (NOT enabled OR source IS NOT NULL);
