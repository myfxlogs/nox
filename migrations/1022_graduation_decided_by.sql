-- 1022_graduation_decided_by.sql —— D-513：毕业委任自动放行审计列。
-- decided_by：'committee'/人工 RPC 空=''（存量行语义=委员会裁决）；'delegate:D-513'=规则自动批。
ALTER TABLE carry_graduations ADD COLUMN IF NOT EXISTS decided_by text NOT NULL DEFAULT '';
COMMENT ON COLUMN carry_graduations.decided_by IS 'D-513：裁决主体——''=人工委员会 RPC；delegate:D-513=委任规则自动放行';
