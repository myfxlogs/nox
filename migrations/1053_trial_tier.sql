-- D-625 P0 微额试单轨：tier 阶级层+cost_model 实测账+晋级判据种子。
ALTER TABLE xasset_allow_combos
  ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'armed'
    CHECK (tier IN ('armed','trial'));
ALTER TABLE fin_decision_outcomes
  ADD COLUMN IF NOT EXISTS source_tier TEXT NOT NULL DEFAULT 'armed';
-- 成本模型实测账（venue×结构×时段桶——回测准入面读实收非模型）。
CREATE TABLE IF NOT EXISTS fin_cost_model (
  id          bigserial PRIMARY KEY,
  venue       text NOT NULL,
  venue_type  text NOT NULL DEFAULT '',
  structure   text NOT NULL,
  bucket_hour int  NOT NULL DEFAULT -1,  -- -1=全天桶（时段维先粗后细）
  fills       int  NOT NULL DEFAULT 0,
  fee_usd     numeric(20,8) NOT NULL DEFAULT 0,
  drift_bps   numeric(20,8) NOT NULL DEFAULT 0,       -- fill vs 簿锚 drift 合计
  latency_ms  bigint        NOT NULL DEFAULT 0,       -- 划转/下单时滞合计
  funding_exp numeric(20,8) NOT NULL DEFAULT 0,       -- 预期 funding 合计
  funding_act numeric(20,8) NOT NULL DEFAULT 0,       -- 实收 funding 合计
  updated_at  timestamptz   NOT NULL DEFAULT now(),
  CONSTRAINT fin_cost_model_key UNIQUE (venue, venue_type, structure, bucket_hour)
);
-- 晋级判据种子（app_config 驱动禁硬编码——阈值=判据非字面量）。
INSERT INTO app_config (key, value) VALUES
  ('trial_min_fills','3'),
  ('trial_cost_dev_max_bps','50'),
  ('trial_tier_notional_usd','200')
ON CONFLICT (key) DO NOTHING;
-- 存量 s2_trial 组合回填 trial 阶级（桥写径已带 tier，历史行缺省 armed 须归位——
-- combo#20 即首发标本，不回填则 trial 组合被当 armed 面消费）。
UPDATE xasset_allow_combos SET tier='trial' WHERE source='s2_trial' AND tier='armed';
