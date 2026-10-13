-- D-249①：策略淘汰多空听证记录——裁定击杀前跑 LLM 多空对辩（多方辩持有/
-- 空方辩击杀，各 ≤400 字），对辩文本附裁定记录供决策席复核。
-- 听证仅观察面：零写径回淘汰判定（判定权=判据表确定性函数）；status 标
-- debated/skipped_no_ai/failed——AI 缺配=听证缺如正常淘汰（fail-open）。
CREATE TABLE IF NOT EXISTS fin_strategy_hearing (
    id          bigserial PRIMARY KEY,
    strategy    text        NOT NULL,
    family      text[]      NOT NULL DEFAULT '{}',
    stats       jsonb       NOT NULL DEFAULT '{}'::jsonb, -- 确定性统计面快照（total/win_rate/avg_pnl/median_pnl）
    reason      text        NOT NULL,                     -- 判据命中理由（裁定记录原文）
    bull_text   text        NOT NULL DEFAULT '',          -- 多方辩持有词
    bear_text   text        NOT NULL DEFAULT '',          -- 空方辩击杀词
    status      text        NOT NULL CHECK (status IN ('debated','skipped_no_ai','failed')),
    created_at  timestamptz NOT NULL DEFAULT now()
);
