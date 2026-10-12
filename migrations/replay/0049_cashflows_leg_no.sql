-- 0049_cashflows_leg_no.sql - D-159 W3a R5：per-leg 幂等键前置（position+leg+kind+day）
-- leg_no 对应 position_legs.leg_no（SMALLINT，从 0 开始）；历史行 leg_no=NULL（兼容，不破坏既有查询）。
-- 唯一索引防重复记账：(position_id, leg_no, kind, UTC 日期) WHERE leg_no IS NOT NULL。
-- UTC 日期用 ((ts AT TIME ZONE 'UTC')::date) 明确时区，与 HasCashflowForLegDay 的 time.Date(...,time.UTC) 对齐。
-- 表达式索引需额外括号包裹整个表达式（PostgreSQL 语法要求）。
-- 历史行（leg_no=NULL）不受唯一索引约束（WHERE leg_no IS NOT NULL），兼容既有数据。

ALTER TABLE position_cashflows ADD COLUMN leg_no SMALLINT;

CREATE UNIQUE INDEX position_cashflows_per_leg_uniq
    ON position_cashflows (position_id, leg_no, kind, ((ts AT TIME ZONE 'UTC')::date))
    WHERE leg_no IS NOT NULL;
