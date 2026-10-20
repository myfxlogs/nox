-- expire 判据粘性修（491 定性病灶）：fin_proposal.miss_count——连 N 拍
-- live 面外才 expired（单拍瞬态 miss 不再即杀；DB 列持久=重启保守域）。
ALTER TABLE fin_proposal ADD COLUMN IF NOT EXISTS miss_count int NOT NULL DEFAULT 0;
