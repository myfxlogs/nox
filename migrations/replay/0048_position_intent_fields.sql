-- 0048_position_intent_fields.sql - D-159 W1a：intent 持久化 + UNKNOWN 过渡
-- request_id：唯一执行意图 ID（autotrade-{ts}-{oppID} 或 RPC 传入），重复提交返回原 intent
-- dedup_key：业务组合防重（combo.Key 格式），active 唯一约束
-- environment：'demo'/'live'，执行时固定
-- state_version：状态机版本（过渡期 CLOSING+uncertainty_reason 表达 UNKNOWN）
-- uncertainty_reason：UNKNOWN 过渡语义（OrderSend 超时/响应丢失/查询失败）

ALTER TABLE positions ADD COLUMN IF NOT EXISTS request_id text;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS dedup_key text;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS environment text;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS state_version int NOT NULL DEFAULT 1;
ALTER TABLE positions ADD COLUMN IF NOT EXISTS uncertainty_reason text;

-- request_id 唯一索引（过渡期允许 NULL，W1a 后新行必填）
CREATE UNIQUE INDEX IF NOT EXISTS positions_request_id_idx ON positions (request_id) WHERE request_id IS NOT NULL;

-- active dedup_key 唯一约束（同一组合只能有一个 active intent）
CREATE UNIQUE INDEX IF NOT EXISTS positions_active_dedup_key_idx ON positions (dedup_key) WHERE status IN ('OPENING','OPEN','LEGGED','CLOSING');

-- D-159 W1a R3-1：CLOSE leg 原子领取防重——同一 position+leg_no+phase 只能写一次。
-- 对账自动收尾 + 手动平仓并发时，CLOSE leg 重复 INSERT 被唯一索引拒绝（原子领取）。
CREATE UNIQUE INDEX IF NOT EXISTS position_legs_pos_leg_phase_idx ON position_legs (position_id, leg_no, phase);
