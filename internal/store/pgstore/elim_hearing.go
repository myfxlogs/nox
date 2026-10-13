package pgstore

import "context"

// ElimHearing fin_strategy_hearing 行（D-249① 淘汰多空听证——裁定记录附件）。
type ElimHearing struct {
	Strategy string
	Family   []string
	Stats    []byte // jsonb 统计面快照
	Reason   string
	BullText string
	BearText string
	Status   string // debated | skipped_no_ai | failed
}

// InsertElimHearing 听证行落库（追加——事件触发，一裁定一行）。
func (s *Store) InsertElimHearing(ctx context.Context, h ElimHearing) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_strategy_hearing (strategy, family, stats, reason, bull_text, bear_text, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		h.Strategy, h.Family, h.Stats, h.Reason, h.BullText, h.BearText, h.Status)
	return err
}
