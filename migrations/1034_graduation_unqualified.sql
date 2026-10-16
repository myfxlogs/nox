-- 毕业准入闸漏洞修（业主令 10-16）：pending 定期重校验态机补 'unqualified'/'stale'
-- ——衰减行摘牌转 unqualified（引擎复活可翻回 pending），stale 存量值合法化
-- （原 CHECK 只许 pending/approved/rejected，stale 写入撞约束隐性失败在案）。
ALTER TABLE carry_graduations DROP CONSTRAINT carry_graduations_status_check;
ALTER TABLE carry_graduations ADD CONSTRAINT carry_graduations_status_check
  CHECK (status IN ('pending','approved','rejected','unqualified','stale'));
