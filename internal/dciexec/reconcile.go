// reconcile.go —— D-191 R3：托管池日对账。
// 口径：池份额行=personal_positions asset='BTC' kind='other'（DCI 托管池表象，
// 同 D-191 回执入账径）；实有池=币安实时 BTC 余额×custody_share（venue 实拍为唯
// 一真相，不读静态 qty）；漂移=|账-实|>阈值→warn 告警+行 qty/note 自动修正。
// 归因口径：挖矿/利息等外部流入只增池余额→对账增量记 note「池外增量」，不计策略
// 损益（策略归因只看 DCI 申购/结算单面，池行 qty 变动属托管池水位非盈亏）。
package dciexec

import (
	"context"
	"fmt"
	"log"
	"math"
)

// 漂移阈值：相对 1% 或绝对 1e-4 BTC（防余额浮点噪声误警）。
const (
	reconcileDriftRel = 0.01
	reconcileDriftAbs = 1e-4
)

// ReconcilePoolOnce 日对账单轮：返回账/实/漂移是否超阈（告警+自动修正已内联）。
// 实=余额(free+locked)×share + 未到期 DCI 申购在外 qty×share（R4 回填）。
func (e *Executor) ReconcilePoolOnce(ctx context.Context) (ledger, live float64, drifted bool, err error) {
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	share, _ := tenorCapOf(cons)
	if share == nil {
		return 0, 0, false, fmt.Errorf("custody_share 约束缺（托管池对账不可行）")
	}
	live, err = e.freeBalance(ctx, "BTC")
	if err != nil {
		return 0, 0, false, fmt.Errorf("BTC 实时余额读败: %w", err)
	}
	live *= *share
	// outstanding 回填：filled 未到期申购锁定币离现货账，加回池实有（R4）。
	if out, err := e.Store.OutstandingDCIQty(ctx, "binance", "BTC"); err != nil {
		return 0, 0, false, fmt.Errorf("outstanding DCI 读败: %w", err)
	} else {
		live += out * *share
	}
	poss, err := e.Store.ListOpenPersonalPositions(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	var row *struct {
		id  int64
		qty float64
	}
	var poolQty float64
	var poolID int64
	for i := range poss {
		if poss[i].Asset == "BTC" && poss[i].Kind == "other" {
			if poss[i].Qty != nil {
				poolQty += *poss[i].Qty
			}
			// 修正目标行取首行（池行单行契约；多余行合并在 ledger 口径内）。
			if row == nil {
				poolID = poss[i].ID
				row = &struct {
					id  int64
					qty float64
				}{poss[i].ID, 0}
			}
		}
	}
	ledger = poolQty
	if row == nil {
		return ledger, live, false, fmt.Errorf("托管池份额行缺（无 BTC other OPEN 持仓行）")
	}
	diff := math.Abs(live - ledger)
	base := math.Max(math.Abs(ledger), 1e-9)
	drifted = diff > reconcileDriftAbs || diff/base > reconcileDriftRel
	now := e.Now().Format("2006-01-02 15:04")
	if !drifted {
		log.Printf("[dciexec] pool reconcile ok: ledger %.8f live %.8f", ledger, live)
		return ledger, live, false, nil
	}
	delta := live - ledger
	note := fmt.Sprintf(
		"托管池对账自动修正 %s：账 %.8f→实 %.8f（Δ%+.8f；外部增量如挖矿/利息计池外流入，不入策略归因）",
		now, ledger, live, delta)
	if err := e.Store.UpdatePersonalPositionQtyNote(ctx, poolID, live, note); err != nil {
		return ledger, live, true, err
	}
	if err := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"托管池对账漂移：BTC 账 %.8f vs 币安实有×share %.8f（Δ%+.8f），份额行已自动修正（池外增量不计策略损益）",
		ledger, live, delta)); err != nil {
		return ledger, live, true, err
	}
	return ledger, live, true, nil
}
