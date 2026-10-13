-- D-258 M4.1：fin_opp_eval 机会评估卡（v3 §7）。
-- 一提案一卡（proposal_id PK 幂等 upsert）；五项拆解全 APR% 年化口径，
-- 缺证据项值 NULL；verdict∈viable|unevaluable|reject；
-- actual_apr 执行后回写（喂 §4.3 指标⑤预实偏差）。
CREATE TABLE IF NOT EXISTS fin_opp_eval (
  proposal_id   bigint PRIMARY KEY REFERENCES fin_proposal(id) ON DELETE CASCADE,
  template_id   text NOT NULL,
  gross_apr     double precision,
  fee_apr       double precision,
  slippage_apr  double precision,
  failure_apr   double precision,
  window_apr    double precision,
  net_apr       double precision,
  actual_apr    double precision,
  verdict       text NOT NULL CHECK (verdict IN ('viable','unevaluable','reject')),
  evidence_refs jsonb NOT NULL DEFAULT '{}'::jsonb,
  computed_at   timestamptz NOT NULL DEFAULT now()
);
