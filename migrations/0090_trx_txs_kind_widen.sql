-- D-279：fin_trx_txs.kind 扩容 varchar(16)→varchar(32)
-- 原因：permission_update(18) 超 varchar(16) 限制
ALTER TABLE fin_trx_txs ALTER COLUMN kind TYPE varchar(32);
