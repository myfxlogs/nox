// position_evals.go —— D-377 S1/S2：XASSET 退出评估观测轨存储（窄行表，每评估一行）。
// 与 monitor/cooled_exits 表同族：id/ts/pos_id 窄行 + pos_ts 复合索引。
package pgstore

import (
	"context"

	"arb/internal/errclass"
)

// PositionEval position_evals 一行（评估当刻 basis/anchor/fill 同刻并置）。
type PositionEval struct {
	PositionID int64
	BasisBps   *float64 // currentBasisBps 当刻值（NULL=quote 缺失）
	AnchorBps  *float64 // expected_entry_spread_bps 当刻存储值
	FillBps    *float64 // EntrySpreadBpsFromLegs 当刻重算（NULL=腿未 FILLED）
	Decision   string   // hold/harvest/stop/exec_drift/carry_death/fail_closed:*（应用层枚举）
	Detail     string   // 阈值/reason 短串（≤200 字截断）
}

// InsertPositionEval 落一行观测（evaluated_at=now()；detail 200 字 rune 截断）。
// 写库失败由调用方 fail-open 承接（观测不得阻塞判定，D-377 S2 钉注）。
func (s *Store) InsertPositionEval(ctx context.Context, e PositionEval) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO position_evals (position_id, basis_bps, anchor_bps, fill_bps, decision, detail)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.PositionID, e.BasisBps, e.AnchorBps, e.FillBps, e.Decision, errclass.TruncateNote(e.Detail, 200))
	return err
}
