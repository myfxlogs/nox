-- 0011_exchange_simulated.sql: exchange_configs 加 simulated 标志（D-054）。
-- OKX 模拟盘 = 实盘域名 + 请求头 x-simulated-trading:1（demo key 必开，独立域名 demo-trading.okx.com 不存在）。
-- 存量行默认 FALSE（实盘语义）；历史无此概念，新开关默认关闭。
ALTER TABLE exchange_configs ADD COLUMN simulated BOOLEAN NOT NULL DEFAULT FALSE;
