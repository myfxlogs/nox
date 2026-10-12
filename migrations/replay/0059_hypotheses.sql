-- 0059_hypotheses.sql - D-193-C：假设卡运行事实
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §3.4
-- 步骤 2 的假设卡是研究 SSOT；本表把批准版本变成可关联的运行事实。

CREATE TABLE IF NOT EXISTS hypotheses (
    hypothesis_id     text PRIMARY KEY,           -- 如 HYP-CB-ROTATION-001
    hypothesis_version integer NOT NULL DEFAULT 1,
    status            text NOT NULL DEFAULT 'draft', -- draft/testing/approved/rejected/retired
    net_return_hurdle double precision NOT NULL DEFAULT 8.0, -- 年化净收益门槛 %
    max_drawdown      double precision NOT NULL DEFAULT 10.0, -- 最大回撤 %
    principal_rule    text NOT NULL,              -- 本金保护规则描述
    kill_rule         text NOT NULL,              -- 证伪/杀线规则
    evidence_ref      text,                       -- 证据文档路径
    approved_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (status = ANY (ARRAY['draft','testing','approved','rejected','retired']))
);

CREATE INDEX IF NOT EXISTS hypotheses_status_idx ON hypotheses (status);
