-- 1065：经济经验库消化裁定表（消化链建制票②三分诊定性落库）。
-- 待归纳行裁定三分出路枚举：canon=转正条目 / dismiss=理由必填驳回（同因重报=病灶信号） /
-- merge=并入既有条目。event_key=机械行 settlement_event_key 或人工键（[LLM草稿] 行用
-- llm:<slug>）。理由必填防 dismiss 垃圾桶（CHECK 钉死非空）。
CREATE TABLE IF NOT EXISTS econ_kb_verdicts (
    event_key  text PRIMARY KEY,
    verdict    text NOT NULL CHECK (verdict IN ('canon','dismiss','merge')),
    reason     text NOT NULL CHECK (btrim(reason) <> ''),
    entry_id   text,                          -- canon=新条目 id；merge=并入目标条目 id
    fixpoint   text,                          -- 回改点（canon 必填：eval|kill_trigger|combo_gate|sizing|semantic:<target>）
    decided_by text NOT NULL DEFAULT '',
    decided_at timestamptz NOT NULL DEFAULT now()
);
