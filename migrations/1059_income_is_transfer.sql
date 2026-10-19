-- D-630 ③：income_events 划转伪影标记列（划转≠收益——is_transfer=true 行
-- 收入面剔除；伪影行禁删史=标记不删，冲正走反向入账既有径）。
ALTER TABLE income_events ADD COLUMN IF NOT EXISTS is_transfer boolean NOT NULL DEFAULT false;
