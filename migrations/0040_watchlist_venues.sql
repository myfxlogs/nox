-- D-090 返修包① R1：自选清单升级双维（canonical + venues[]）。
-- venues NULL/空 = 全部源（语义「全部」显式）；非空 = 仅该 venueKey 集合。
-- 纯加列，不破坏既有 0039 数据（既有行 venues=NULL=全部源，语义不变）。
ALTER TABLE watchlist_symbols ADD COLUMN IF NOT EXISTS venues text[];
