-- 1071：personal_positions.venue 列转正（STATE 行49——custody-attr note 词
-- 解析过渡件转正；venue 列禁 CHECK 枚举——venue 集=账本投影域新所注册不
-- 该撞 schema）。
ALTER TABLE personal_positions ADD COLUMN IF NOT EXISTS venue text;

-- 回填三态：①「申购回执：<venue>」词解行照词填；②asset=BTC kind=other 无
-- 词行=binance（dciPoolVenue 向后兼容兜底语义同构——venue 盲时代行如实归
-- binance 域）；③其它 kind/asset 行=NULL 未归因如实。
UPDATE personal_positions
SET venue = substring(note from '申购回执：([^[:space:]]+)')
WHERE venue IS NULL AND note ~ '申购回执：[^[:space:]]+';

UPDATE personal_positions
SET venue = 'binance'
WHERE venue IS NULL AND asset = 'BTC' AND kind = 'other';
