-- D-279：TRX 交易官方恶意标记（TronScan cheatStatus）
ALTER TABLE fin_trx_txs ADD COLUMN cheat_status boolean NOT NULL DEFAULT false;
