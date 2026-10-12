-- D-191-R11①：DCI 报价行补申购量界三字段（min/max_amount+purchase_decimal）；
-- depositAmount floor 至 purchase_decimal、界外 fail-closed 不补不截。
ALTER TABLE fin_cex_dci_quote
    ADD COLUMN IF NOT EXISTS min_amount double precision,
    ADD COLUMN IF NOT EXISTS max_amount double precision,
    ADD COLUMN IF NOT EXISTS purchase_decimal integer;
