-- D-272 逆回购操作记录+策略验证闭环。
-- 与 cnfi_repo_ops（D-270 提醒生命周期）分离：
--   cnfi_repo_ops = 系统提醒→确认（pending/operated/skipped）
--   cnfi_repo_trades = 实际操作记录+理论对比+结算+偏差分析
-- 如果有 pending op 被确认，repo_op_id 关联；业主也可手动录入（无 pending op）。

CREATE TABLE IF NOT EXISTS cnfi_repo_trades (
    id                      BIGSERIAL PRIMARY KEY,

    -- 操作录入（业主填写）
    instrument_code         TEXT NOT NULL,             -- "204001" (GC001)
    trade_date              DATE NOT NULL,             -- 操作日期
    holding_days            INTEGER NOT NULL,          -- 利息天数（GC001周四=3，T+1结算享周末3天利息）
    maturity_date           DATE NOT NULL,             -- 资金可用日（后端默认 trade_date+1 回显，业主可覆盖——全库无交易日历，周五/假日 T+1≠次日历日）
    principal_cny           DOUBLE PRECISION NOT NULL, -- 本金（实际操作金额）
    actual_rate_pct         DOUBLE PRECISION NOT NULL, -- 实际成交利率%（年化，百分数 1.45=1.45%）

    -- 理论快照（录入时系统自动抓取，业主不可编辑）
    -- 非交易时段 fin_repo_rates 为空时，以下理论字段全部 NULL，仍允许录入
    theoretical_rate_pct    DOUBLE PRECISION,          -- 理论利率（录入时 GC001 最新利率，百分数）
    theoretical_return_by_idle DOUBLE PRECISION,       -- 系统建议收益 = theoretical_rate/100 * idle_cny * holding_days/365
    theoretical_return_cny  DOUBLE PRECISION,          -- 同金额理论收益 = theoretical_rate/100 * principal * holding_days/365
    idle_cny_at_record      DOUBLE PRECISION,          -- 录入时 idle_cny 快照（系统建议金额）
    signal_snapshot         TEXT,                      -- 录入时信号快照 JSON（结构见设计 §3）

    -- 实际结算（到期后业主确认）
    actual_return_cny       DOUBLE PRECISION,          -- 实际收益（到期确认）
    settled_at              TIMESTAMPTZ,               -- 结算确认时间

    -- 偏差分析（系统计算）
    -- 录入时填入（不依赖结算）：
    deviation_rate_bps      DOUBLE PRECISION,          -- 利率偏差 = (actual_rate - theoretical_rate) * 100
    deviation_principal_cny DOUBLE PRECISION,          -- 金额偏差 = principal - idle_cny（正=超额操作，负=不足操作）
    -- 结算时填入（依赖 actual_return）：
    deviation_return_cny    DOUBLE PRECISION,          -- 收益偏差 = actual_return - theoretical_return_cny
    deviation_return_pct    DOUBLE PRECISION,          -- 收益偏差率 = deviation_return / theoretical_return_cny * 100（theoretical_return=0 时置 0）

    -- 生命周期（两态，无 amended）
    status                  TEXT NOT NULL DEFAULT 'open', -- open(在途) / settled(已结算)

    -- 关联+来源
    repo_op_id              BIGINT,                    -- 关联 cnfi_repo_ops.id（如有系统提醒，NULL=纯手动）
    entry_source            TEXT NOT NULL DEFAULT 'manual', -- 恒 'manual'（本 D# 无飞书自动创建 trades 流程；未来有自动创建流程的 D# 再加 'feishu'）

    -- 到期提醒幂等（D-272 §5）
    last_maturity_reminded_at TIMESTAMPTZ,

    -- 审计
    rationale               TEXT,                      -- 操作笔记（为什么做）
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 重复录入防护：同一天同一品种只能录入一次
CREATE UNIQUE INDEX idx_repo_trades_unique ON cnfi_repo_trades (trade_date, instrument_code);

-- 按操作日期倒序（前端列表）
CREATE INDEX idx_repo_trades_trade_date ON cnfi_repo_trades (trade_date DESC);
-- 按状态筛选（在途/已结算）
CREATE INDEX idx_repo_trades_status ON cnfi_repo_trades (status);
-- 到期提醒查询（maturity_date <= today AND status=open）
CREATE INDEX idx_repo_trades_maturity ON cnfi_repo_trades (maturity_date) WHERE status = 'open';
-- 关联 cnfi_repo_ops（可选）
CREATE INDEX idx_repo_trades_repo_op_id ON cnfi_repo_trades (repo_op_id) WHERE repo_op_id IS NOT NULL;
