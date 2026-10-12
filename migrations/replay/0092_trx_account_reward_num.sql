-- 0092_trx_account_reward_num.sql: TRX 待领取奖励真值采集（D-285）
-- tronscan /api/account.rewardNum 直接返回链上估算的待领取奖励（=TronLink 显示值）。
-- 替代 D-233 Vi 累积器估算（偏差 59%）。
ALTER TABLE fin_trx_account ADD COLUMN reward_num_sun BIGINT NOT NULL DEFAULT 0;
