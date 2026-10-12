-- 0008_personal_ledger.sql: 个人持仓实账化（财务线工作项 3，SSOT 财务线-个人持仓实账化方案.md）
-- 1) personal_positions 加 4 列（均 NULL；staking 只填 qty，short_hedge 填全）
ALTER TABLE personal_positions
    ADD COLUMN qty         DOUBLE PRECISION,   -- 持仓数量（中性化：真实值由本地脚本注入 /opt/fin/arb-personal-seed.sql，D-047 数据不进 git）
    ADD COLUMN entry_price DOUBLE PRECISION,   -- 开仓价（仅 short_hedge；中性化同上）
    ADD COLUMN leverage    INTEGER,            -- 杠杆倍数（仅 short_hedge）
    ADD COLUMN opened_at   TIMESTAMPTZ;        -- 开仓时间

-- 2) 每日收益表：一日一条（全组合计），energy/voting 属质押腿、interest 属空单腿，一次录入覆盖三值。
--    结构化每日数值只进本表；记账页 observation 只记定性/一次性笔记（职责边界 P3）。
CREATE TABLE position_daily_income (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    day          DATE NOT NULL UNIQUE,        -- 一日一条（全组合计）
    energy_trx   NUMERIC NOT NULL DEFAULT 0,  -- 能量收益（手动）
    voting_trx   NUMERIC NOT NULL DEFAULT 0,  -- 投票收益（手动）
    interest_usd NUMERIC NOT NULL DEFAULT 0,  -- 资金费/利息（手动）
    price        NUMERIC,                     -- 记录时 TRX 现价（后端自动快照）
    pnl_usd      NUMERIC,                     -- 记录时空单浮动盈亏（后端自动算）
    note         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 3) 初始数据回填：按 (asset,kind) 定位（不依赖 id，防 id 漂移）；note 简化（0.36/0.3695 已入结构化列 warn_high/crit_high）。
--    数值中性化（qty/entry 置 0、leverage/opened_at 置 NULL）：真实持仓值由本地脚本注入（/opt/fin/arb-personal-seed.sql，D-047 金额/数量不进 git）。
UPDATE personal_positions SET qty = 0
 WHERE asset = 'TRX' AND kind = 'staking';

UPDATE personal_positions
   SET qty = 0,
       entry_price = 0,
       leverage = NULL,
       opened_at = NULL,
       note = '对冲空单（杠杆，保值腿）'
 WHERE asset = 'TRX' AND kind = 'short_hedge';
