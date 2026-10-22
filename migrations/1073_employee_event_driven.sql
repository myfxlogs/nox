-- 1073 员工台账事件驱动分型（roster 建模错位修——任务型 spawn 〔每查询一 spawn/
-- 每 tx 一 spawn/boot 一次性/日级任务〕跑完即退=running=f 永假离线；非永驻
-- spawn 不入 running 判生死域，显「事件驱动」态不判离线）。
ALTER TABLE fin_employee_registry
  ADD COLUMN IF NOT EXISTS event_driven boolean NOT NULL DEFAULT false;
