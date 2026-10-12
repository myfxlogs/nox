-- 0154_calib_tables.sql —— D-533 校准计分板（设计稿 §3 S1/S2）：
-- calib_scoreboard 月度桶（venue×symbol×dim MAE/bias/p90）+ calib_params 版本化提案
-- （提案永不直写运行参数：active 翻位=业主显式动作，activated_by 留痕）。
-- 种值四行=现取值链 v1 锚（cexSlipBpsByCanonical 分级表种值不迁——按 param_key='cex_slip_bps'
-- +symbol 列留待提案逐项校准）。

CREATE TABLE IF NOT EXISTS calib_scoreboard (
  month_ym    TEXT NOT NULL,             -- 'YYYY-MM' 北京历月（D-510 口径）
  dim         TEXT NOT NULL CHECK (dim IN ('commission','slippage','funding')),
  venue       TEXT NOT NULL,
  symbol      TEXT NOT NULL,             -- canonical 基名（KB canonical-name-vs-source-native-name）
  sample_n    INT NOT NULL,
  mae         DOUBLE PRECISION,
  bias        DOUBLE PRECISION,          -- actual - predicted（bps，正=低估成本）
  p90_abs     DOUBLE PRECISION,
  status      TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok','insufficient')),
  computed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (month_ym, dim, venue, symbol)
);
COMMENT ON TABLE calib_scoreboard IS 'D-533：月度校准计分板（dim=commission/slippage/funding；sample_n<5=insufficient 不出提案）';

CREATE TABLE IF NOT EXISTS calib_params (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  param_key      TEXT NOT NULL,
  venue          TEXT NOT NULL DEFAULT '',
  symbol         TEXT NOT NULL DEFAULT '',
  value_bps      DOUBLE PRECISION NOT NULL,
  source_month   TEXT NOT NULL,            -- 提案依据月份；种值行='seed'
  sample_n       INT, mae DOUBLE PRECISION, bias DOUBLE PRECISION,
  proposed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  activated_at   TIMESTAMPTZ, activated_by TEXT,
  prev_value_bps DOUBLE PRECISION,
  active         BOOLEAN NOT NULL DEFAULT false,
  UNIQUE (param_key, venue, symbol, source_month)   -- 提案幂等键
);
COMMENT ON TABLE calib_params IS 'D-533：版本化参数提案链（active 唯一；回滚=翻位；自动激活路径不存在）';

-- active 唯一：同键至多一行 active（回滚=翻位）
CREATE UNIQUE INDEX IF NOT EXISTS calib_params_active_uniq
  ON calib_params (param_key, venue, symbol) WHERE active;
-- 种值（现值，source_month='seed'）——保证取值链 v1 有锚：
INSERT INTO calib_params (param_key, value_bps, source_month, activated_at, activated_by, active) VALUES
  ('cex_taker_fee_bps',    5.0, 'seed', now(), 'd533-seed', true),
  ('cfd_fee_bps',          0.3, 'seed', now(), 'd533-seed', true),
  ('cex_slip_default_bps', 3.0, 'seed', now(), 'd533-seed', true),
  ('intra_taker_fee_bps',  5.0, 'seed', now(), 'd533-seed', true)
ON CONFLICT DO NOTHING;
