-- 0167 D-015 ②：position_legs.status 增 'EXTERNAL'——venue 推送平仓事件
-- （gate position_closes/liquidates/ADL、okx positions pos=0、binance ACCOUNT_UPDATE pa=0）
-- 落「外部已平」CLOSE 腿终态，价格/费用从帧取。与 FILLED 区分=系统未发单的平腿。
ALTER TABLE position_legs DROP CONSTRAINT position_legs_status_check;
ALTER TABLE position_legs ADD CONSTRAINT position_legs_status_check
    CHECK (status IN ('PENDING','SUBMITTED','FILLED','FAILED','UNKNOWN','EXTERNAL'));
