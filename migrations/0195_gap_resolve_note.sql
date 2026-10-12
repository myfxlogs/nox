-- D-193②：缺口自动收口备注列（需求变更收口留痕）。
ALTER TABLE fin_condition_gap
    ADD COLUMN IF NOT EXISTS resolve_note text;
