-- 1033_s2_trial_notional.sql —— D-548：S2 桥试单名义额 PG 参数化。
-- 裁定：s2_trial_notional_usd=200（本金 ~1.8%/结构——盖 venue 最小名义+
-- 费率信噪比，试错损失噪音级；设计稿「额度封顶」悬置项落此 D#）。
INSERT INTO app_config (key, value, note) VALUES
('s2_trial_notional_usd', '200', 'D-548 S2 桥试单名义额封顶（$；改值须新 D#）')
ON CONFLICT (key) DO NOTHING;
