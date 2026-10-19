-- P0 续件：positions.combo_id 归因列（armed 现役仓 best-effort 回填：note 含
-- 'combo:<id>' 前缀者机读回填，无据行留 NULL=诚实缺不臆造）。
ALTER TABLE positions ADD COLUMN IF NOT EXISTS combo_id BIGINT;
CREATE INDEX IF NOT EXISTS positions_combo_id_idx ON positions (combo_id) WHERE combo_id IS NOT NULL;
UPDATE positions SET combo_id = (regexp_match(note, 'combo:(\d+)'))[1]::bigint
WHERE combo_id IS NULL AND note ~ 'combo:\d+';
