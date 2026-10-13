-- D-255 venue 败因坑表：CEX/DCI 拒因机器可读沉淀——下单败行自动落表，
-- 申购前查坑（action=fix 按参数映射自修复 / suppress 抑单窗 / critical 升告警）。
-- 能力账本补件：约束业主喂（fin_capability_constraint），坑 venue 教（本表）。

CREATE TABLE IF NOT EXISTS fin_venue_reject (
    venue           text NOT NULL,
    op              text NOT NULL,              -- 操作面（dci_subscribe 等）
    error_signature text NOT NULL,              -- 规范化签名（code:-1102 / 归一败文）
    action          text NOT NULL DEFAULT 'suppress'
                    CHECK (action IN ('fix','suppress','critical')),
    action_params   jsonb NOT NULL DEFAULT '{}'::jsonb, -- fix: {"deposit_decimals":0} 等映射
    hits            integer NOT NULL DEFAULT 1,          -- 累计撞坑次数
    first_seen      timestamptz NOT NULL DEFAULT now(),
    last_seen       timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz,                -- suppress 窗；NULL=永久
    PRIMARY KEY (venue, op, error_signature)
);
