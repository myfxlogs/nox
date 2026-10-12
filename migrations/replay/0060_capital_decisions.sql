-- 0060_capital_decisions.sql - D-193-C：资本决策记录（含状态迁移）
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §3.4
-- 审计报告 §3.3：状态迁移并入 from_state/to_state，不另建 capital_state_log。
-- 追加式（append-only），不修改已写入的决策。

CREATE TABLE IF NOT EXISTS capital_decisions (
    decision_id           bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
    account_id            bigint REFERENCES capital_accounts(account_id),
    capital_snapshot_id   bigint,                  -- 决策时的快照
    opportunity_id        text,                    -- 关联 opportunities
    hypothesis_id         text REFERENCES hypotheses(hypothesis_id),
    hypothesis_version    integer,
    decision              text NOT NULL,           -- allocate/wait/reduce/quarantine/retire
    from_state            text,                    -- CORE/READY/DEPLOYED/HARVESTING/REDUCING/QUARANTINED/CASH
    to_state              text,                    -- 同上
    trigger_fact_id       text,                    -- 触发事实 ID
    visible_event_watermark timestamptz,           -- 决策时可见事件水位
    parent_decision_id    bigint REFERENCES capital_decisions(decision_id),
    reversible            boolean NOT NULL DEFAULT false,
    capital_before_usd    double precision,
    capital_change_usd    double precision,
    capital_after_usd     double precision,
    expected_net_usd      double precision,
    expected_net_annual_pct double precision,
    annualization_basis   text,                    -- 247/trading_days/calendar
    risk_budget_usd       double precision,
    reason                text NOT NULL,
    evidence_ref          text,
    config_digest         text,                    -- 配置版本摘要
    occurred_at           timestamptz NOT NULL DEFAULT now(),
    event_key             text NOT NULL UNIQUE,    -- 幂等键
    CHECK (decision = ANY (ARRAY['allocate','wait','reduce','quarantine','retire'])),
    CHECK (from_state IS NULL OR from_state = ANY (ARRAY['CORE','READY','DEPLOYED','HARVESTING','REDUCING','QUARANTINED','CASH'])),
    CHECK (to_state IS NULL OR to_state = ANY (ARRAY['CORE','READY','DEPLOYED','HARVESTING','REDUCING','QUARANTINED','CASH']))
);

CREATE INDEX IF NOT EXISTS capital_decisions_account_ts_idx
    ON capital_decisions (account_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS capital_decisions_hypothesis_idx
    ON capital_decisions (hypothesis_id) WHERE hypothesis_id IS NOT NULL;
