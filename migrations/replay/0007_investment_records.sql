-- 0007_investment_records.sql: 记账功能（财务线工作项 2）
-- 结构化投资记录（决定/教训/观察），关联资产，独立轻量表（设计文档 财务线-记账功能方案.md §3）。
-- 与 ledger（出入金流水）语义不同：ledger 是金额结构化数据，此处是文本知识；不并入 ledger 表（§1.2）。

CREATE TABLE investment_records (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('decision','lesson','observation')),
    asset      TEXT NOT NULL DEFAULT '',     -- 关联资产（类别级：TRX / USDT / HK现金 / CNY …）
    content    TEXT NOT NULL,                -- 记录正文
    ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX investment_records_kind_idx  ON investment_records (kind);
CREATE INDEX investment_records_asset_idx ON investment_records (asset);
