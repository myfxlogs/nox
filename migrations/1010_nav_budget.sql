-- M4.4 NAV 日切 + 资金配置器（D-264）：
-- fin_nav_daily=日切 NAV 一行/天幂等（day_start=BJ 历日 UTC 零时，同 deptreport 口径）；
-- fin_strategy_budget=策略级预算表，配置器域独写，闸层（gate.BudgetChecker）只读。

CREATE TABLE IF NOT EXISTS fin_nav_daily (
    day_start   timestamptz PRIMARY KEY,         -- BJ 历日零时（UTC 值，不赌 DB 时区）
    nav_usd     double precision NOT NULL,       -- Σ venue 权益（USD-only，同 IR 口径）
    idle_usd    double precision NOT NULL,       -- Σ 可动余额
    venues      jsonb NOT NULL DEFAULT '[]'::jsonb,  -- 各 venue 分解 [{venue,venue_type,equity,balance,currency}]
    computed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS fin_strategy_budget (
    strategy     text PRIMARY KEY,
    budget_usd   double precision NOT NULL,
    derived_from jsonb NOT NULL DEFAULT '{}'::jsonb, -- 推导输入留痕（nav/deployable/share/max_notional）
    updated_at   timestamptz NOT NULL DEFAULT now()
);
