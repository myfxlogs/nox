-- 0061_position_reviews.sql - D-193-C：结构化偏差事实
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §6 S3
-- 审计报告 §6：不与 knowledge_entries（定性知识）合并。

CREATE TABLE IF NOT EXISTS position_reviews (
    position_id           bigint PRIMARY KEY REFERENCES positions(id),
    hypothesis_id         text,
    expected_net_usd      double precision,
    actual_net_usd        double precision,        -- 读 position_settlements.net_realized_usd
    expected_cost_usd     double precision,
    actual_cost_usd       double precision,
    cost_deviation_usd    double precision,        -- actual - expected
    expected_risk_usd     double precision,
    actual_risk_usd       double precision,
    risk_deviation_usd    double precision,
    followup_action       text,                    -- continue/reduce/quarantine/retire
    review_note           text,
    reviewed_at           timestamptz NOT NULL DEFAULT now()
);
