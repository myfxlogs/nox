-- 0120_position_evals.sql - D-377 S1：XASSET 退出评估观测轨（窄行表）。
-- 每评估一行：basis/anchor/fill 同刻并置（fill−basis=漂移序列）+ decision 枚举
-- （hold/harvest/stop/exec_drift/carry_death/fail_closed:no_basis|no_anchor|no_legs；
--  text 无 CHECK 应用层枚举，exit_kind 同风格）。
-- 体量估算：≤50 仓×≤1 行/60s=≤72k 行/天——不设 TTL（需时后补 prune 任务）。
-- fail_closed 行=「评估跑了但无法判」正证据（饥荒/缺数据观测双根因）。
CREATE TABLE position_evals (
    id           bigserial PRIMARY KEY,
    position_id  bigint      NOT NULL REFERENCES positions(id),
    evaluated_at timestamptz NOT NULL DEFAULT now(),
    basis_bps    double precision,
    anchor_bps   double precision,
    fill_bps     double precision,
    decision     text        NOT NULL,
    detail       text        NOT NULL DEFAULT ''
);
CREATE INDEX position_evals_pos_ts ON position_evals (position_id, evaluated_at);
