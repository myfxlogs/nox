-- 0152_macro_tables.sql —— D-528 宏观采集层（设计稿 §2 S2）：三表幂等锚。
-- macro_event：mql5 财历事件（UNIQUE 四元组=upsert 幂等锚）；
-- cot_position：CFTC COT 周报持仓（v1 只搬原始字段，pct_index/momentum/conviction 暂空）；
-- fred_observation：FRED 观测序列（PK=幂等锚，同日重拉覆盖）。

CREATE TABLE IF NOT EXISTS macro_event (
  id             BIGSERIAL PRIMARY KEY,
  source         TEXT        NOT NULL,
  event_time_utc TIMESTAMPTZ NOT NULL,
  currency       TEXT        NOT NULL,
  name           TEXT        NOT NULL,
  importance     TEXT        NOT NULL DEFAULT '',
  forecast       TEXT        NOT NULL DEFAULT '',
  previous       TEXT        NOT NULL DEFAULT '',
  actual         TEXT        NOT NULL DEFAULT '',
  revision       TEXT        NOT NULL DEFAULT '',
  raw            JSONB,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source, event_time_utc, currency, name)
);
CREATE INDEX IF NOT EXISTS idx_macro_event_time ON macro_event (event_time_utc);
COMMENT ON TABLE macro_event IS 'D-528：宏观财历事件（source=mql5；时间一律 UTC，渲染层转 BJ）';

CREATE TABLE IF NOT EXISTS cot_position (
  id           BIGSERIAL PRIMARY KEY,
  report_date  DATE        NOT NULL,
  instrument   TEXT        NOT NULL,
  category     TEXT        NOT NULL,
  direction    TEXT        NOT NULL DEFAULT '',
  net_position BIGINT      NOT NULL DEFAULT 0,
  net_change   BIGINT      NOT NULL DEFAULT 0,
  pct_index    NUMERIC,
  momentum     TEXT        NOT NULL DEFAULT '',
  conviction   NUMERIC,
  raw          JSONB,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (report_date, instrument, category)
);
CREATE INDEX IF NOT EXISTS idx_cot_position_instr ON cot_position (instrument, report_date DESC);
COMMENT ON TABLE cot_position IS 'D-528：CFTC COT 净持仓（category=lev_fund/asset_mgr/dealer/managed_money/prod_merc/swap/noncomm/comm/nonrept——后三类仅 CSV 兜底路，CFTC legacy 归类）';

CREATE TABLE IF NOT EXISTS fred_observation (
  series_id  TEXT        NOT NULL,
  obs_date   DATE        NOT NULL,
  value      NUMERIC     NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (series_id, obs_date)
);
COMMENT ON TABLE fred_observation IS 'D-528：FRED 观测（白名单序列；同日重拉 ON CONFLICT 覆盖）';
