-- 0115_fin_venue_io.sql - D-365 S1：CEX 进出账流水（充值/提现，binance+okx real 配置）。
-- 幂等=(venue,config_id,direction,external_id) 唯一键（external_id=binance.id / okx depId·wdId，
-- 后者字段名经核证点④真探 9-20 实证）；水位游标由 LatestVenueIOTs 从表内派生（零新状态表）。
CREATE TABLE fin_venue_io (
  id          BIGSERIAL PRIMARY KEY,
  venue       TEXT NOT NULL,             -- 'binance'/'okx'
  config_id   BIGINT NOT NULL,           -- exchange_configs.id（账户归属）
  direction   TEXT NOT NULL,             -- 'deposit'/'withdraw'
  ccy         TEXT NOT NULL,
  amount      NUMERIC NOT NULL,
  fee         NUMERIC,
  status      TEXT,                      -- 交易所原生状态码原样存（核证点③枚举见采集器注释）
  txid        TEXT,
  external_id TEXT NOT NULL,             -- binance.id / okx depId·wdId
  ts          TIMESTAMPTZ NOT NULL,      -- 事件时间（insertTime/applyTime/ts）
  fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (venue, config_id, direction, external_id)
);
CREATE INDEX idx_fin_venue_io_ts ON fin_venue_io(ts DESC);
