-- 0004_broker_commission.sql - CFD 账号佣金 per-venue 真实定价（D-036）
-- 按账号类型（Standard/Raw 等）记录真实佣金率，NULL = 未配置，回退 config 默认。
-- 单位：bps/腿（与 opportunities.commission_cost 同口径）。填 $/手 时按名义额换算后手填。

ALTER TABLE broker_accounts ADD COLUMN commission_bps DOUBLE PRECISION;
