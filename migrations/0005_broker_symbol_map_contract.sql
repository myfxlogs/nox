-- 0005_broker_symbol_map_contract.sql - Symbol 映射合约规格（D-042）
-- 每条映射记录该 broker 符号的真实合约规格（MT5 SymbolInfo.ContractSize），
-- 供 canonical 画像校验：同标的跨 broker 合约必一致，不符 = 假映射（套利对比不可靠）。
-- NULL = 未知（MT4 无 ContractSize / 历史映射），校验跳过。

ALTER TABLE broker_symbol_map ADD COLUMN contract_size DOUBLE PRECISION;
