-- 0130_cashflows_leg_day_eventkey.sql - D-422 R1：per-leg 幂等索引放宽到 event_key 粒度。
-- 原 position_cashflows_per_leg_uniq 为 (position_id, leg_no, kind, UTC日) 唯一——
-- 同日同腿 OPEN 入场费（event_key 带 :open）与 CLOSE 平仓佣金（无 tag）被互吞（R1 实证 23505）。
-- event_key 幂等由 position_cashflows_event_key_uniq（0054）独立承载；
-- 本索引保留 leg_no IS NOT NULL 行的腿日约束但追加 COALESCE(event_key,'') 列——
-- 无 event_key 的 legacy 写入仍按腿日判重，带 event_key 行各自独立不互吞。

DROP INDEX IF EXISTS position_cashflows_per_leg_uniq;

CREATE UNIQUE INDEX position_cashflows_per_leg_uniq
    ON position_cashflows (position_id, leg_no, kind, ((ts AT TIME ZONE 'UTC')::date), COALESCE(event_key, ''))
    WHERE leg_no IS NOT NULL;
