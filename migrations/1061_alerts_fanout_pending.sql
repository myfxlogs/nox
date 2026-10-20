-- D-633 拆票/SLO ⑤：alerts.fanout_pending——进程外摄入件（alert-emit 族）
-- 绕 SetAlertHook 内链的扇出缺口判据列（true=待 core 扫带扇出）。
-- 内插径 hook 已推/抑→false；进程外插→default true 待扫带。
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS fanout_pending boolean NOT NULL DEFAULT true;
-- 存量行=历史已推（或未推旧档——扫带不设回放风暴锚，全数封口）。
UPDATE alerts SET fanout_pending=false;
