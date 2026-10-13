-- M4.2 策略生命周期（D-260）：fin_strategy 状态机 + fin_strategy_criteria 判据表。
-- 判据=数据行非源码字面量：改行即改行为（票面验收③）。
-- 状态机=唯一存活裁决：state∈(demoted,retired)→disabled 投影（fail-closed）。

CREATE TABLE IF NOT EXISTS fin_strategy (
    strategy    text PRIMARY KEY,
    state       text NOT NULL DEFAULT 'candidate'
                CHECK (state IN ('candidate','paper','graduated','live','demoted','retired')),
    hit_rules   jsonb NOT NULL DEFAULT '[]'::jsonb,
    decided_at  timestamptz,
    decided_by  text NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- 存量回填=覆盖验收（fin_strategy 全覆盖 strategy_stats）：
-- disabled→retired（既有淘汰等价）；在跑者→live（即当前真实现况——
-- state!=live 即 disabled 投影，盲置 candidate 会部署即停生产）。
INSERT INTO fin_strategy (strategy, state, decided_by)
SELECT strategy,
       CASE WHEN disabled THEN 'retired' ELSE 'live' END,
       'migration-1009'
FROM strategy_stats
ON CONFLICT (strategy) DO NOTHING;

-- 判据表：一行=一条规则。pred 命名 anchors；params jsonb 机读区间+条件式。
CREATE TABLE IF NOT EXISTS fin_strategy_criteria (
    id        bigserial PRIMARY KEY,
    strategy  text NOT NULL DEFAULT '*',
    kind      text NOT NULL CHECK (kind IN ('graduation','demotion','elimination')),
    pred      text NOT NULL,
    params    jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (strategy, kind, pred)
);

-- 种子=elimination.go 原字面量迁表，语义逐项对等：
-- 小样本 [5,30) 中位&均值双负→kill；大样本 ≥30 胜率<30% 或均值<0→kill。
INSERT INTO fin_strategy_criteria (strategy, kind, pred, params) VALUES
('*', 'elimination', 'small_sample_kill',
 '{"trades_gte":5,"trades_lt":30,"mode":"all","conds":["median_pnl<0","avg_pnl<0"]}'),
('*', 'elimination', 'large_sample_kill',
 '{"trades_gte":30,"mode":"any","conds":["win_rate<0.3","avg_pnl<0"]}'),
-- graduation：纸面存活判据（paper→graduated 出「可晋级」告警，不自动真金）。
('*', 'graduation', 'paper_survive',
 '{"trades_gte":10,"mode":"all","conds":["avg_pnl>0","win_rate>=0.5"]}')
ON CONFLICT (strategy, kind, pred) DO NOTHING;

-- 提案 SLA：proposed 超龄→expired+critical 告警。期限机读改值零改码（默认 24h 钦定）。
INSERT INTO app_config (key, value, note) VALUES
('proposal_sla_hours', '24', 'M4.2 提案 SLA：proposed 龄超此值→expired+critical')
ON CONFLICT (key) DO NOTHING;
