-- 0073_cnfi_holdings.sql - CN-FI P3 持仓与四层进度（D-226 §6；SSOT=docs/cnfi/CN-FI设计文档.md）
-- 金额/份额=D-047 允许的 PG 运行时数据（禁入 git 的只有 git 面：migration 无种子、文档无数值）。
-- 层间目标比例存 cnfi_signal_config（key='layer_targets'，可入 git——纯比例非个人数据）。

CREATE TABLE cnfi_holdings (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        text NOT NULL REFERENCES cnfi_instruments (code),
    layer       text NOT NULL CHECK (layer IN ('liquidity', 'coupon', 'locked', 'opportunity')),
    qty         numeric NOT NULL,              -- 份额（基金）/ 张数（国债）
    cost_value  numeric NOT NULL,              -- 成本金额（运行时个人数据，D-047：禁入 git）
    opened_date date NOT NULL,
    note        text,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cnfi_holdings_layer_idx ON cnfi_holdings (layer);
