// s2bridge.go —— S2 通用族参数面桥（设计稿 §S2+业主令 10-14 触发版）：
// fin_opp_hypothesis status='approved' 行 → 可跑参数入册。族→沉点映射：
//
//	cfd_swap_hedged|v|sym|side|cven|csym → xasset_allow_combos 白名单行
//	  （小额试单 notional=$500/上限 gross=$500——额度封顶设计稿 §8 待决策项
//	  暂取保守值随行注记，decision 落 D# 后改 PG 键）。
//	okx FUTURES *_XPERP（USD_UM 日历基差）→ perp_delivery 纸面轨已覆盖
//	  （wip/okx-um-basis 并入面）→ 标 trialing 注记去向即可，零新参数。
//	family_fit='generic_yield' → 参数面 schema 未建 → 告警待立施工票
//	  （24h 去重，approved 态保持不丢）。
//	其余族 → 新机制施工票告警（同去重）。
//
// 桥每拍搭车 RunOnce（hedgeScan 后），零新定时器。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

const s2TrialNotionalUSD = 500.0 // 小额试单额度（设计稿「额度封顶」待定 D# 前保守值）

// s2Bridge approved 假设→可跑桥主径（RunOnce 拍内调用）。
func s2Bridge(ctx context.Context, st *pgstore.Store) {
	rows, err := st.ListApprovedHypotheses(ctx)
	if err != nil {
		log.Printf("[s2-bridge] list approved: %v", err)
		return
	}
	for _, h := range rows {
		if err := bridgeOne(ctx, st, h); err != nil {
			log.Printf("[s2-bridge] #%d %s: %v", h.ID, h.CategoryKey, err)
		}
	}
}

func bridgeOne(ctx context.Context, st *pgstore.Store, h pgstore.OppHypothesis) error {
	parts := strings.Split(h.CategoryKey, "|")
	switch {
	case parts[0] == "cfd_swap_hedged" && len(parts) == 6:
		// {mtVenue}|{mtSym}|{side}|{cexVenue}|{cexSym}
		mtVenue, mtSym, side, cexVenue, cexSym := parts[1], parts[2], parts[3], parts[4], parts[5]
		combo := &pgstore.XAssetComboInsert{
			CanonicalBuy: cexSym, CanonicalSell: mtSym,
			VenueBuy:   cexVenue + ":perp", // CEX 腿=funding 收付侧（P0 口径 perp）
			VenueSell:  mtVenue + ":mt5",   // MT 腿=swap 收息侧
			MaxLongQty: 5, MaxShortQty: 5,  // 试单腿量封顶（base 单位；gross 上限双闸）
			MaxGrossUSD: s2TrialNotionalUSD,
			NotionalUSD: &[]float64{s2TrialNotionalUSD}[0],
			Note:        fmt.Sprintf("S2桥 假设#%d（%s腿 %s side）", h.ID, mtSym, side),
		}
		if _, err := st.InsertXAssetAllowCombo(ctx, combo); err != nil && err != pgstore.ErrXAssetComboExists {
			return fmt.Errorf("allow combo insert: %w", err)
		}
		if err := st.MarkHypothesisTrialing(ctx, h.ID, "xasset_allow_combos 白名单行"); err != nil {
			return err
		}
		return st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s）→ xasset_allow_combos 白名单行已入册（notional=$%.0f 试单封顶）",
			h.ID, h.CategoryKey, s2TrialNotionalUSD))
	case h.InstType == "FUTURES" && strings.HasSuffix(h.Shape, "_XPERP"):
		// okx USD_UM 日历基差——perp_delivery 纸面轨覆盖（同 canonical 配对+Δbasis 记账已产）。
		if err := st.MarkHypothesisTrialing(ctx, h.ID, "perp_delivery 纸面轨已覆盖（okx USD_UM）"); err != nil {
			return err
		}
		return st.InsertAlertDedup(ctx, "info", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s）已由 perp_delivery 纸面轨覆盖→trialing",
			h.ID, h.CategoryKey), 24*time.Hour)
	case h.FamilyFit == "generic_yield":
		return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s）族=generic_yield 参数面 schema 未建——立施工票",
			h.ID, h.CategoryKey), 24*time.Hour)
	default:
		return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s 族=%s）无参数面沉点——新机制施工票待立",
			h.ID, h.CategoryKey, h.FamilyFit), 24*time.Hour)
	}
}
