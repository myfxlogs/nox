-- 1019_opp_hypothesis.sql —— 新品类自发现管线 S1：fin_opp_hypothesis 假设行表。
-- category_key=venue+instType+形态键 UNIQUE 幂等锚；rejected 在册即抑重推（弃件记忆）。
-- S1 只到 proposed+memo 止——approved→执行衔接属 S2/S3，本表不建自动径。
CREATE TABLE IF NOT EXISTS fin_opp_hypothesis (
    id           bigserial PRIMARY KEY,
    venue        text NOT NULL,
    category_key text NOT NULL UNIQUE,
    inst_type    text,                          -- venue 原生仪器类（contractType/instType/形态名）
    shape        text,                          -- 形态键（quote/结算币/结构签名）
    params_proto jsonb,                         -- LLM 预审参数原型草案（S2 通用族原料）
    family_fit   text,                          -- generic_yield|novel|none；NULL=未预审
    memo         text,                          -- LLM 预审备忘录（顾问产出，非批准件）
    sample       text,                          -- 示例符号（≤3）
    detail       text,                          -- 侦测细节（行数/端点）
    status       text NOT NULL DEFAULT 'proposed'
                 CHECK (status IN ('proposed','approved','rejected','trialing')),
    decided_by   text,
    decided_at   timestamptz,
    reject_reason text,
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE fin_opp_hypothesis IS '新品类假设行（S1：目录 diff 侦测→proposed+memo→info 告警；裁决/试单属 S2/S3）';
