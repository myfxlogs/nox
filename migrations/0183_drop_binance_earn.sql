-- 0183_drop_binance_earn.sql - D-133 域1：binance 理财面整体摘除（业主令：理财账户实有
-- 杂币，「只统计 USDT」不可达→earn 面不纳入系统）。counter-migration 收口 0182 的 INSERT——
-- 0182 本体不改（已应用迁移不可变），新环境仍会先播种、本迁移随即将其删除；幂等可重放。
-- 生产 binance:earn 行已于 10-08 先行删除（D-130 部署注），本迁移双保险。
DELETE FROM exchange_configs WHERE venue = 'binance' AND venue_type = 'earn';
