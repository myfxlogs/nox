-- D-274 事件驱动机会研判引擎：event_cases + paper_trades 两表（M1 §2/§3）
CREATE TABLE event_cases (
    id SERIAL PRIMARY KEY,
    alert_id BIGINT UNIQUE,          -- 去重锚（M0 §13）
    code TEXT NOT NULL,
    market TEXT NOT NULL,             -- HK / US
    event_type TEXT NOT NULL,         -- privatisation / merger / tender_offer
    announce_date DATE NOT NULL,
    offer_price NUMERIC,              -- 对价（原始货币）
    currency TEXT,                    -- HKD/USD
    pre_price NUMERIC,                -- 公告前收市价（原始货币）
    lot_size INT,                     -- 每手股数（Longbridge 权威 API）
    lot_size_source TEXT,             -- longbridge / history / unknown
    premium_pct NUMERIC,              -- 百分比数字 61.3（非小数）
    offer_type TEXT,                   -- fixed_price / dutch_auction / partial / full
    proration TEXT,                    -- full / partial / none
    conditions TEXT,                   -- JSON 数组：["court_approval","shareholder_vote","regulatory","competing_offer"]
    acquirer_strength TEXT,            -- strong / medium / weak
    risk_signal TEXT,                  -- red / yellow / green
    score INT,                         -- 满分 8（v5.0 L5 删 competing_offer；0-8 分）
    suggested_qty INT,                  -- 研判时预计算的建议手数
    outcome TEXT NOT NULL,            -- success / failed / withdrawn / pending
    settle_date DATE,
    settle_price NUMERIC,
    fail_drawdown_pct NUMERIC,         -- 小数 0-1（非百分比数字 29.5）
    holding_days INT,
    source_url TEXT,
    model_validation TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_event_cases_type ON event_cases(event_type);
CREATE INDEX idx_event_cases_outcome ON event_cases(outcome);
CREATE INDEX idx_event_cases_source_url ON event_cases(source_url);  -- SA2：source_url 预检索引

CREATE TABLE paper_trades (
    id SERIAL PRIMARY KEY,
    case_id INT REFERENCES event_cases(id) UNIQUE,  -- 1 case 1 trade
    alert_id BIGINT,                  -- 查询用（非 UNIQUE）
    source_type TEXT NOT NULL,         -- hkex / edgar
    code TEXT NOT NULL,
    market TEXT NOT NULL,
    direction TEXT NOT NULL,          -- buy / hold / sell
    suggested_qty INT,                -- 手（从 event_cases.suggested_qty 读取）
    suggested_price NUMERIC,         -- 建议买入价=pre_price（原始货币）
    actual_price NUMERIC,             -- 实际成交价（业主填，NULL=未执行）
    status TEXT NOT NULL,             -- suggested / settled / expired
    confidence TEXT NOT NULL,         -- high / medium / low
    sample_size INT,
    rationale TEXT,
    model_net_return_pct NUMERIC,    -- 小数 0-1（=研判时 netReturnPct）
    model_fail_drawdown_pct NUMERIC, -- 小数 0-1（=研判时 failDrawdownPct）
    model_success_rate NUMERIC,      -- 小数 0-1（=研判时 successRate）
    holding_period_days INT,
    actual_net_return_pct NUMERIC,   -- 小数 0-1（结算后填，M3 算）
    actual_outcome TEXT,              -- success / failed
    created_at TIMESTAMPTZ DEFAULT NOW(),
    settled_at TIMESTAMPTZ
);
CREATE INDEX idx_paper_trades_status ON paper_trades(status);
CREATE INDEX idx_paper_trades_code ON paper_trades(code);
CREATE INDEX idx_paper_trades_created ON paper_trades(created_at DESC);
