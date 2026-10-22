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
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"strings"
)

// 漂移阈值：相对 1% 或绝对 1e-4 BTC（防余额浮点噪声误警）。
const (
	reconcileDriftRel = 0.01
	reconcileDriftAbs = 1e-4
)

// ReconcilePoolOnce 日对账单轮：返回账/实/漂移是否超阈（告警+自动修正已内联）。
// 实=（现货 free+locked+赚币子账）×share + 未到期 DCI 申购在外 qty×share（R4/R6）。
func (e *Executor) ReconcilePoolOnce(ctx context.Context) (ledger, live float64, drifted bool, err error) {
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	share := gate.CustodyShareOf(cons, "binance") // D-644：对账池=binance 共享池域
	if share == nil {
		return 0, 0, false, fmt.Errorf("custody_share 约束缺（托管池对账不可行）")
	}
	live, err = e.poolBalance(ctx, "BTC") // 现货+赚币子账（R6 池口径）
	if err != nil {
		return 0, 0, false, fmt.Errorf("BTC 池余额读败（盲源禁缩账）: %w", err)
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
		// 托管池 venue 归因（STATE 行45）：池行=binance 域专格——okx DCI 回执
		// 同写 BTC other（venue 盲病灶，okx_dci.go:188）须剔出 ledger 口径，
		// 否则 live(binance)×share vs 账(binance+okx 锁仓) 往复对打 ping-pong
		// （10-21 14:12/15:01 两 warn 同值实证 #41 0.0207）。归因=note venue
		// 词解析（personal_positions 无 venue 列——schema 列=后续票余量）。
		if poss[i].Asset == "BTC" && poss[i].Kind == "other" && dciPoolVenue(poss[i]) == "binance" {
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
	// 盲源闸：实有≈0 但账>0=余额面盲读疑（earn/spot 权限或子账未覆盖），
	// 禁缩账——缺口请求业主核查而非自动清零（R6）。
	if live < reconcileDriftAbs && ledger > reconcileDriftAbs {
		return ledger, live, false, e.gap(ctx, gate.CallLeg, "runtime", "earn_position",
			"系统侧病灶：托管池实有≈0 但账 %.8f——余额面盲读疑（赚币权限/子账覆盖），禁缩账", ledger)
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
		"托管池对账漂移：BTC 账 %.8f vs 币安实有×share %.8f（Δ%+.8f），binance 池行已自动修正（池外增量不计策略损益；okx 域行另格不算本池）",
		ledger, live, delta)); err != nil {
		return ledger, live, true, err
	}
	return ledger, live, true, nil
}

// dciPoolVenue BTC other 池行 venue 归因（1071 列转正）：机器归因读 venue
// 列——列空才回落 note 词解（存量未归因行安全垫；新行 venue 列写入径供给
// 永不落空）。无 venue 词且列空行=binance（向后兼容兜底——venue 盲时代行
// 默认归 binance 域如实，与迁移 1071 回填三态同构）。
func dciPoolVenue(p pgstore.PersonalPosition) string {
	if p.Venue != nil && *p.Venue != "" {
		return *p.Venue
	}
	if i := strings.Index(p.Note, "申购回执："); i >= 0 {
		tok := p.Note[i+len("申购回执："):]
		if j := strings.IndexAny(tok, " \t"); j > 0 {
			return tok[:j]
		}
		return tok
	}
	return "binance"
}
