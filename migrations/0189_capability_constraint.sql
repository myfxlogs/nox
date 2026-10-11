-- 0189_capability_constraint.sql D-186②：能力账本·约束承载面（D-184 总纲——
-- 不进账本对系统不存在；托管份额/赎回 SLA/结算口径/风险禁限由对话入库）。
-- 挂点=nullable position_id + scope 枚举四值（审计修②：约束不都挂持仓）。
CREATE TABLE IF NOT EXISTS fin_capability_constraint (
  id          bigserial PRIMARY KEY,
  scope       text NOT NULL CHECK (scope IN ('position','bucket','venue','global')),
  position_id bigint REFERENCES positions(id) ON DELETE SET NULL,  -- scope=position 才填
  subject     text NOT NULL,            -- custody_share / custody_redeemable / redeem_sla / settlement / reserve_pct / risk_limit
  qualifier   text NOT NULL DEFAULT '', -- max / min / always / same_day / current_price
  value_num   double precision,         -- 数值界（份额 0.34 / 百分比 30）；纯定性约束 NULL
  note        text NOT NULL DEFAULT '',
  source      text NOT NULL DEFAULT '', -- 出处（业主令/D#）
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS fin_capability_constraint_uq
  ON fin_capability_constraint (scope, COALESCE(position_id, 0), subject);

-- 种子：D-171/D-184 托管约束入库（票面验收——托管 1/3 约束落账本后被 D-187 匹中）。
INSERT INTO fin_capability_constraint (scope, subject, qualifier, value_num, note, source) VALUES
  ('bucket','custody_share','max',0.34,'BTC 托管可动份额上限（约 1/3，余 2/3 业主自留不动）','D-171 业主令'),
  ('bucket','custody_redeemable','always',NULL,'托管币随时可赎回（无锁仓 DCI 定期承诺外不可占满）','D-171 业主令'),
  ('bucket','custody_settlement','current_price',NULL,'托管线现价结算口径','D-171 业主令')
ON CONFLICT DO NOTHING;
