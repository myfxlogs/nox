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

-- 存量回填如实（复审二次返修选型裁定=因果链优先）：combos note
-- 'D-403毕业 paper#N' → graduation.paper_id=N 且 status='approved' 回链
-- provenance_id=g.id——因果实证非 id 硬对位（fresh 库面亦真，SG 实拍预期
-- combos #17/18/19 落 (graduation,46/47/48)）。无 paper# 注行不碰（manual_seed 如实）。
UPDATE xasset_allow_combos a SET source='graduation', provenance_id=g.id
    FROM carry_graduations g
    WHERE g.status='approved'
      AND g.paper_id = substring(a.note from 'paper#(\d+)')::bigint
      AND a.source IS NULL;
-- S2桥 试单行回填（复审③注记）：note 'S2桥 假设#N' → fin_opp_hypothesis.id
-- 回链 provenance_id（委员会试单批准链在案如实）。
UPDATE xasset_allow_combos a SET source='s2_trial',
    provenance_id=substring(a.note from 'S2桥 假设#(\d+)')::bigint
    WHERE a.note ~ 'S2桥 假设#\d+' AND a.source IS NULL
      AND EXISTS (SELECT 1 FROM fin_opp_hypothesis h
        WHERE h.id = substring(a.note from 'S2桥 假设#(\d+)')::bigint);
-- 无毕业/试单实据存量行=manual_seed 如实（不造假出处）
UPDATE xasset_allow_combos SET source='manual_seed' WHERE source IS NULL;

-- enable 闸：NULL 出处行禁 enabled（DB 钉死——回填之后挂闸存量行先合规）。
ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_source_required;
ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_source_required
    CHECK (NOT enabled OR source IS NOT NULL);
