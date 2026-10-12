-- 0149_settlement_incident_attributed.sql —— D-512 T9：combo 冷却闸（B10 事故账同面复用）。
-- incident_attributed=true = 事故归因结算（position 终态 LEGGED/FAILED 或 skipped_reason 非空）；
-- 判据从部署时点起算（历史行不回填——历史 LEGGED 属 T8 legacy_demo 域）。
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS incident_attributed bool NOT NULL DEFAULT false;

-- 存量回填按同判据一次（部署时点快照，非逐日追溯）：
UPDATE position_settlements s SET incident_attributed = true
FROM positions p
WHERE p.id = s.position_id
  AND (p.status IN ('LEGGED','FAILED') OR COALESCE(s.skipped_reason,'') <> '');
