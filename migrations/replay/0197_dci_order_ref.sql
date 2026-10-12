-- D-191-R10②：DCI 报价行补 order_ref（申购发参 id/orderId/depositAmount/autoCompoundPlan 面；
-- parseBinanceDCI 原解析 orderId 丢弃——落库后 executor 候选按此发单）。
ALTER TABLE fin_cex_dci_quote
    ADD COLUMN IF NOT EXISTS order_ref text NOT NULL DEFAULT '';
