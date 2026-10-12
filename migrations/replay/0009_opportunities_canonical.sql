-- 0009_opportunities_canonical.sql: opportunities 落 canonical 列（D-051 WP2 修④，P3 单点）。
-- ID 格式 TYPE:canonical:venueBuy:venueSell:ms → split_part(id, ':', 2) = canonical（venueKey 含冒号不影响）。
ALTER TABLE opportunities ADD COLUMN canonical TEXT;

UPDATE opportunities SET canonical = split_part(id, ':', 2) WHERE canonical IS NULL;
