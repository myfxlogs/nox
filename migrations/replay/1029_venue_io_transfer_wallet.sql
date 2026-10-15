-- 1029_venue_io_transfer_wallet.sql - D-539①：内部划转台账——fin_venue_io 扩
-- direction='transfer' 语义所需的两列（from_wallet/to_wallet 快照 venue_type 同键域：
-- spot/funding/earn/perp/unified/margin/...；NULL=外部充提行不适用）。
ALTER TABLE fin_venue_io ADD COLUMN IF NOT EXISTS from_wallet TEXT;
ALTER TABLE fin_venue_io ADD COLUMN IF NOT EXISTS to_wallet   TEXT;
COMMENT ON COLUMN fin_venue_io.from_wallet IS '划转源钱包（direction=transfer 专用；venue_type 键域）';
COMMENT ON COLUMN fin_venue_io.to_wallet   IS '划转目的钱包（direction=transfer 专用；venue_type 键域）';
