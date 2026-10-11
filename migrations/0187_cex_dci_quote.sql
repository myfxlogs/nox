-- 0187_cex_dci_quote.sql D-183：三所 DCI/双币产品报价表（venue×underlying×strike×tenor×apy）。
-- 只读采集面（facts 纯值口径塞不下 strike/tenor 维度→独立表，票面裁定）。
CREATE TABLE IF NOT EXISTS fin_cex_dci_quote (
  id            bigserial PRIMARY KEY,
  venue         text NOT NULL,
  underlying    text NOT NULL,      -- BTC/ETH（行权/标的币种）
  invest_ccy    text NOT NULL,      -- 申购币种
  strike        double precision,   -- 行权价；venue 不返则 NULL 如实缺
  opt_type      text,               -- CALL/PUT（gate 小写归一大写）
  delivery_ts   timestamptz,        -- 交割时点；venue 不返则 NULL
  tenor_days    int,                -- 采集时刻换算剩余天数
  apy           double precision NOT NULL, -- 年化（venue 原生口径小数/分数按所原样）
  product_ref   text,               -- 行 id/instrument_name 溯源
  ts            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_dci_quote_vu_ts ON fin_cex_dci_quote (venue, underlying, ts DESC);
