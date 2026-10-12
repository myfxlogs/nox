-- D-345：carry 宇宙 suspect 门注记列——脏源行（任一输入腿 |年化|>100%）保观测落表，
-- net=0 剔除排名，note 记疑因（如 'suspect: swap 469.5%'）。干净行 note=''。
ALTER TABLE fin_carry_universe ADD COLUMN IF NOT EXISTS note TEXT NOT NULL DEFAULT '';
