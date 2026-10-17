// delegate.go —— D-513：毕业委任规则·确定性自动放行（并入 RunOnce 扫描拍，零新定时器）。
// 全条件 AND：pending≥3天 ∧ paper龄≥14天 ∧ accrued>0 ∧ flag_warn空 ∧ 额度≤$500 ∧
// 品种限既有 live 白名单 canonical 域 ∧ 日批≤1 → approved + decided_by='delegate:D-513'
// +逐笔 warn 告警。app_config grad_delegate_enabled='0'/'false'/'off' 一键撤（缺省=启用——
// 决策本意就是缺席期代批；撤键=全停）。LLM 零介入；条件任一不满足=继续 pending 不编不放。
// 结构域收窄 fail-closed：仅 'spot_carry'（同所现货多×perp 空，组合写真金白名单即放行）；
// cfd/long_spot_short_perp/perp_perp 留人工——委任不越人工批同构语义。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// GradDelegateReload 委任批后白名单热载回调（cmd/core 装配 atEngine；
// nil=未接线→委任整件跳过不批，假授权不留）。
var GradDelegateReload func(ctx context.Context) error

const (
	delegateMinAge    = 3 * 24 * time.Hour  // pending 龄
	delegatePaperAge  = 14 * 24 * time.Hour // paper 龄
	delegateCapUSD    = 500.0               // 单策略额度硬帽
	delegatePerDayMax = 1                   // 日批上限
)

// delegateTickMaxAge D-604 N2：真金 sizing 判价窗（cmd/core 装配注
// cfg.LegTickWindow 同源——禁新字面量；0=禁用保旧行为）。
var delegateTickMaxAge time.Duration

// SetDelegateTickMaxAge cmd/core 装配注入（同 exec.SetLegTickWindow 先例）。
func SetDelegateTickMaxAge(d time.Duration) { delegateTickMaxAge = d }

// delegateGraduations 委任放行主件（runLifecycle 尾挂）。
func delegateGraduations(ctx context.Context, st *pgstore.Store) {
	if GradDelegateReload == nil {
		return // 热载器未接线=假授权源——整件静默跳过（log 一次性有 Setup 痕迹可查）
	}
	if v, ok, err := st.AppConfigString(ctx, "grad_delegate_enabled"); err == nil && ok &&
		(v == "0" || v == "false" || v == "off" || v == "disabled") {
		return // 一键撤——disable 即全停（d#513）
	}
	grs, err := st.ListCarryGraduations(ctx)
	if err != nil {
		log.Printf("[delegate] list graduations: %v", err)
		return
	}
	today, err := st.CountDelegateGraduationsToday(ctx)
	if err != nil {
		log.Printf("[delegate] count today: %v", err)
		return
	}
	live, err := st.LoadXAssetAllowCombos(ctx, "live")
	if err != nil {
		log.Printf("[delegate] live combos: %v", err)
		return
	}
	liveCanon := map[string]bool{}
	for _, c := range live {
		liveCanon[c.CanonicalBuy] = true
	}
	now := time.Now()
	for _, g := range grs {
		if today >= delegatePerDayMax {
			return
		}
		if !delegateEligible(g) || now.Sub(g.CreatedAt) < delegateMinAge {
			continue
		}
		paper, perr := st.GetPaperSpotCarryByID(ctx, g.PaperID)
		if perr != nil {
			log.Printf("[delegate] paper#%d read: %v", g.PaperID, perr)
			continue
		}
		if !delegatePaperEligible(paper, now) || !liveCanon[g.Symbol] {
			continue
		}
		if delegateApprove(ctx, st, &g, paper) {
			today++
		}
	}
}

// delegateEligible 卡行域筛：pending+flag_warn 空+结构域（spot_carry 唯一委任结构）。
func delegateEligible(g pgstore.CarryGraduation) bool {
	return g.Status == "pending" &&
		(g.FlagWarn == nil || *g.FlagWarn == "") &&
		g.Structure == "spot_carry"
}

// delegatePaperEligible paper 证据筛：龄≥14d ∧ accrued>0 ∧ 0<qty_usd≤$500（额度硬帽不可绕——
// 超帽不缩批不编尺寸，继续 pending 等人工）。
func delegatePaperEligible(p *pgstore.PaperSpotCarry, now time.Time) bool {
	return now.Sub(p.EntryTs) >= delegatePaperAge &&
		p.AccruedUSD > 0 && p.QtyUSD > 0 && p.QtyUSD <= delegateCapUSD
}

// delegateApprove 写白名单组合（qty=QtyUSD/现价——现价缺→fail-closed 不批）+委任落账+warn 留痕。
func delegateApprove(ctx context.Context, st *pgstore.Store, g *pgstore.CarryGraduation, p *pgstore.PaperSpotCarry) bool {
	px, ok := st.LatestTickMidWithin(ctx, g.VenueA, g.Symbol, delegateTickMaxAge)
	if !ok || px <= 0 {
		log.Printf("[delegate] grad#%d %s@%s 现价缺/陈旧→不批（fail-closed·D-604 N2 判价窗）", g.ID, g.Symbol, g.VenueA)
		return false
	}
	qty := p.QtyUSD / px
	combo := &pgstore.XAssetComboInsert{
		CanonicalBuy: g.Symbol, CanonicalSell: g.Symbol,
		VenueBuy: keydomain.Join(g.VenueA, "spot"), VenueSell: keydomain.Join(g.VenueA, "perp"),
		MaxLongQty: qty, MaxShortQty: qty,
		MaxGrossUSD: p.QtyUSD, NotionalUSD: &p.QtyUSD,
		// D-584 出处约束：委任批=毕业批准委托径——source=graduation+血缘=毕业行 id
		// （裁定：delegate 非试单非手工——批准链在案如实标）。
		Source: "graduation", ProvenanceID: &g.ID,
		Note: fmt.Sprintf("D-513委任批 paper#%d grad#%d", p.ID, g.ID),
	}
	comboID, err := st.InsertXAssetAllowCombo(ctx, combo)
	var comboIDP *int64
	if err != nil {
		if err == pgstore.ErrXAssetComboExists {
			comboIDP = nil // 已在白名单——批仍有效（授权即放行语义不重复写）
		} else {
			log.Printf("[delegate] grad#%d combo insert: %v", g.ID, err)
			return false
		}
	} else {
		comboIDP = &comboID
		if rerr := GradDelegateReload(ctx); rerr != nil {
			log.Printf("[delegate] grad#%d reload: %v（已写白名单，委任批照旧落账+warn 注记）", g.ID, rerr)
		}
	}
	note := fmt.Sprintf("委任批（D-513）：paper#%d 龄%.0fd accr+$%.2f cap$%.0f qty=%.6g",
		p.ID, time.Since(p.EntryTs).Hours()/24, p.AccruedUSD, p.QtyUSD, qty)
	if comboIDP == nil {
		note += "（组合已在白名单）"
	}
	if err := st.DelegateApproveCarryGraduation(ctx, g.ID, comboIDP, note); err != nil {
		log.Printf("[delegate] grad#%d decision: %v", g.ID, err)
		return false
	}
	if aerr := st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"[毕业委任] %s %s@%s 自动放行（D-513 全条件达标）paper#%d accr=$%.2f qty=$%.0f→combo %s",
		g.Symbol, g.Structure, g.VenueA, p.ID, p.AccruedUSD, p.QtyUSD, comboRef(comboIDP))); aerr != nil {
		log.Printf("[delegate] grad#%d warn alert: %v", g.ID, aerr)
	}
	return true
}

func comboRef(id *int64) string {
	if id == nil {
		return "已存在"
	}
	return fmt.Sprintf("#%d", *id)
}
