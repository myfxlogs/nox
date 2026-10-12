-- 0201_leg_forensics.sql —— D-208：腿级事故取证洞+CLOSING 半平裸奔阈值种子。
-- ①position_legs.fail_reason：venue 拒因原文（≤512B 截断由写入侧保证），
--   #507 类场景库内可直读拒因不再只有 status=FAILED。
-- ②CLOSING 半平裸奔升级阈值入 app_config（seed-once 幂等，禁硬编码）：
--   ARB_CLOSING_NAKED_WARN_SEC=存在未平腿超此时长→warn；
--   ARB_CLOSING_NAKED_CRIT_SEC=再超→critical（#505 3h 裸奔必有 warn→critical 轨迹）。
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS fail_reason text;

INSERT INTO app_config (key, value, note) VALUES
  ('ARB_CLOSING_NAKED_WARN_SEC','1800','D-208：CLOSING 半平裸奔 warn 阈值（秒）'),
  ('ARB_CLOSING_NAKED_CRIT_SEC','7200','D-208：CLOSING 半平裸奔 critical 阈值（秒）')
ON CONFLICT (key) DO NOTHING;
