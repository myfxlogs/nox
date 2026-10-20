-- 毕业判据三值持久化（D-637 序③返修——建议行带族级证据可查）
ALTER TABLE carry_graduations ADD COLUMN IF NOT EXISTS family_n int;
ALTER TABLE carry_graduations ADD COLUMN IF NOT EXISTS family_win_rate float8;
ALTER TABLE carry_graduations ADD COLUMN IF NOT EXISTS family_lower_bound float8;
