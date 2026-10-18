// s2bridge.go —— S2 通用族参数面桥（设计稿 §S2+业主令 10-14 触发版）：
// fin_opp_hypothesis status='approved' 行 → 可跑参数入册。族→沉点映射：
//
//	cfd_swap_hedged|v|sym|side|cven|csym → xasset_allow_combos 白名单行
//	  （试单名义额=app_config s2_trial_notional_usd PG 读——D-548 裁定 $200，
//	  side 进腿位映射：short=空CFD收swap/多CEX腿，long 反之，未知拒桥）。
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
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/data/fact"
	"nox/internal/platform/alerttext"
	"nox/internal/store/pgstore"
)

// s2TrialNotionalAt 试单名义额读口（D-548）：app_config `s2_trial_notional_usd`
// PG 真值（迁移 1033 种 200）。钱径闸 fail-closed——键缺/破值→拒桥返 err，
// 不硬编码兜底（额度=真金敞口上限，禁代码字面量）。
func s2TrialNotionalAt(ctx context.Context, st *pgstore.Store) (float64, error) {
	v, ok, err := st.AppConfigFloat(ctx, "s2_trial_notional_usd")
	if err != nil {
		return 0, err
	}
	if !ok || v <= 0 {
		return 0, fmt.Errorf("s2_trial_notional_usd 键缺/破值（ok=%v v=%v）——拒桥", ok, v)
	}
	return v, nil
}

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

// s2TradifiAxesReady TRADIFI 采集轴达标判（D-624 前置闸消费面）：休市历轴
// =MT 腿 trade_session facts 30d 内有行（SessionsFacts 日刷件——native/canonical
// 双发故 mt_symbol 裸名直查）；资金费率轴=binance funding facts 26h 新鲜行
// （premiumIndex/markPrice 采集轴产）。返回 miss=缺失轴名（""=双轴齐）。
func s2TradifiAxesReady(ctx context.Context, st *pgstore.Store, mtVenue, mtSym, cexSym string) (string, error) {
	sessions, err := st.FactsSince(ctx, fact.KindTradeSession, mtVenue, mtSym, time.Now().Add(-30*24*time.Hour))
	if err != nil {
		return "", fmt.Errorf("trade_session facts 读败: %w", err)
	}
	if len(sessions) == 0 {
		return fmt.Sprintf("休市历轴空（%s %s trade_session 30d 零行——采集轴未建/品种未映射）", mtVenue, mtSym), nil
	}
	funding, err := st.FactsSince(ctx, fact.KindFunding, "binance", cexSym, time.Now().Add(-26*time.Hour))
	if err != nil {
		return "", fmt.Errorf("funding facts 读败: %w", err)
	}
	if len(funding) == 0 {
		return fmt.Sprintf("资金费率轴空（binance %s funding 26h 零行——TRADIFI 品种枚举未覆盖/采集轴未产）", cexSym), nil
	}
	return "", nil
}

// s2LegCombo cfd_swap_hedged 方向→腿位映射（D-548 复审修）：category_key
// side=CFD 腿方向。short=空 CFD 收 swap/多 CEX 腿；long=反之。其余方向
// 拒桥（不臆造腿位——方向错=真金反向仓）。
func s2LegCombo(h pgstore.OppHypothesis, mtVenue, mtSym, side, cexVenue, cexSym string, notional float64) (*pgstore.XAssetComboInsert, error) {
	c := &pgstore.XAssetComboInsert{
		MaxLongQty: 5, MaxShortQty: 5, // 腿量上限（base 单位；gross 上限先绑）
		MaxGrossUSD: notional, NotionalUSD: &notional,
		// D-584 出处约束：S2 桥委员会试单——source=s2_trial+假设行 id 血缘。
		Source: "s2_trial", ProvenanceID: &h.ID,
		Note: fmt.Sprintf("%s 假设#%d（%s腿 %s side）", pgstore.XAssetTrialNotePrefix, h.ID, mtSym, side),
	}
	switch side {
	case "short":
		c.CanonicalBuy, c.VenueBuy = cexSym, cexVenue+":perp" // CEX 腿=funding 收付侧
		c.CanonicalSell, c.VenueSell = mtSym, mtVenue+":mt5"  // MT 腿=swap 收息侧
	case "long":
		c.CanonicalBuy, c.VenueBuy = mtSym, mtVenue+":mt5"
		c.CanonicalSell, c.VenueSell = cexSym, cexVenue+":perp"
	default:
		return nil, fmt.Errorf("side=%s 方向未知拒桥", side)
	}
	return c, nil
}

func bridgeOne(ctx context.Context, st *pgstore.Store, h pgstore.OppHypothesis) error {
	parts := strings.Split(h.CategoryKey, "|")
	switch {
	case parts[0] == "cfd_swap_hedged" && len(parts) == 6:
		// {mtVenue}|{mtSym}|{side}|{cexVenue}|{cexSym}
		mtVenue, mtSym, side, cexVenue, cexSym := parts[1], parts[2], parts[3], parts[4], parts[5]
		notional, nerr := s2TrialNotionalAt(ctx, st)
		if nerr != nil {
			return nerr
		}
		combo, cerr := s2LegCombo(h, mtVenue, mtSym, side, cexVenue, cexSym, notional)
		if cerr != nil {
			return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"S2桥：approved 假设#%d（%s）%v%s", h.ID, h.CategoryKey, cerr,
				alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d S2桥入册败处置（读 STATE 本行）", h.ID))), 24*time.Hour)
		}
		if _, err := st.InsertXAssetAllowCombo(ctx, combo); err != nil && err != pgstore.ErrXAssetComboExists {
			return fmt.Errorf("allow combo insert: %w", err)
		}
		if err := st.MarkHypothesisTrialing(ctx, h.ID, "xasset_allow_combos 白名单行"); err != nil {
			return err
		}
		return st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s）→ xasset_allow_combos 白名单行已入册（notional=$%.0f 试单封顶）%s",
			h.ID, h.CategoryKey, notional,
			alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d 白名单入册后续（读 STATE 本行——试单授权另闸 D-624）", h.ID))))
	case h.InstType == "FUTURES" && strings.HasSuffix(h.Shape, "_XPERP"):
		// okx USD_UM 日历基差——perp_delivery 纸面轨覆盖（同 canonical 配对+Δbasis 记账已产）。
		if err := st.MarkHypothesisTrialing(ctx, h.ID, "perp_delivery 纸面轨已覆盖（okx USD_UM）"); err != nil {
			return err
		}
		return st.InsertAlertDedup(ctx, "info", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s）已由 perp_delivery 纸面轨覆盖→trialing",
			h.ID, h.CategoryKey), 24*time.Hour)
	case parts[1] == "TRADIFI_PERPETUAL" && len(parts) == 3 && parts[0] == "binance":
		// D-623族归（评估件 docs/能力账/binance-tradifi-perp-eval-2026-10-18.md）：
		// TradFi 标的永续=「crypto perp × MT5 CFD」xasset 对冲变体——既有族
		// 参数沉点复用 xasset_allow_combos。腿参数=params_proto{mt_venue,
		// mt_symbol, side, cex_symbol} 显式供给（LLM memo 草案=顾问件——腿
		// 未配 fail-closed dedup 告警，禁猜腿位）。
		var legs struct {
			MtVenue   string `json:"mt_venue"`
			MtSymbol  string `json:"mt_symbol"`
			Side      string `json:"side"`
			CexSymbol string `json:"cex_symbol"`
		}
		_ = json.Unmarshal(h.ParamsProto, &legs)
		if legs.MtVenue == "" || legs.MtSymbol == "" || legs.Side == "" || legs.CexSymbol == "" {
			return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"S2桥：approved 假设#%d（%s）TRADIFI_PERP 腿参数缺——params_proto{mt_venue,mt_symbol,side,cex_symbol} 未配齐，拒猜腿位%s",
				h.ID, h.CategoryKey,
				alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d TRADIFI 腿参供给处置（读 STATE 本行——params_proto 字段清单在告警文）", h.ID))), 24*time.Hour)
		}
		// D-624 前置闸①②消费面（采集轴建置票）：休市历轴+资金费率轴缺一拒桥——
		// 无历品种=休市窗不可对冲硬敞口；无费率实值=收益性无法评估。
		if miss, aerr := s2TradifiAxesReady(ctx, st, legs.MtVenue, legs.MtSymbol, legs.CexSymbol); aerr != nil {
			return fmt.Errorf("tradifi 采集轴读败: %w", aerr)
		} else if miss != "" {
			return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"S2桥：approved 假设#%d（%s）TRADIFI_PERP 采集轴未达——%s%s", h.ID, h.CategoryKey, miss,
				alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d TRADIFI 采集轴验收处置（读 STATE 本行）", h.ID))), 24*time.Hour)
		}
		notional, nerr := s2TrialNotionalAt(ctx, st)
		if nerr != nil {
			return nerr
		}
		combo, cerr := s2LegCombo(h, legs.MtVenue, legs.MtSymbol, legs.Side, h.Venue, legs.CexSymbol, notional)
		if cerr != nil {
			return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"S2桥：approved 假设#%d（%s）%v%s", h.ID, h.CategoryKey, cerr,
				alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d S2桥入册败处置（读 STATE 本行）", h.ID))), 24*time.Hour)
		}
		if _, err := st.InsertXAssetAllowCombo(ctx, combo); err != nil && err != pgstore.ErrXAssetComboExists {
			return fmt.Errorf("allow combo insert: %w", err)
		}
		if err := st.MarkHypothesisTrialing(ctx, h.ID, "xasset_allow_combos 白名单行（TRADIFI_PERP→xasset 变体）"); err != nil {
			return err
		}
		return st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s）→xasset 白名单（TRADIFI_PERP 变体——决策席试单闸另闸）%s", h.ID, h.CategoryKey,
			alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d 白名单入册后续（读 STATE 本行——试单授权另闸 D-624）", h.ID))))
	case h.FamilyFit == "generic_yield":
		return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s）族=generic_yield 参数面 schema 未建——立施工票%s",
			h.ID, h.CategoryKey,
			alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d 新族机制票处置（读 STATE 本行+fin_opp_hypothesis#%d）", h.ID, h.ID))), 24*time.Hour)
	default:
		return st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
			"S2桥：approved 假设#%d（%s 族=%s）无参数面沉点——新机制施工票待立%s",
			h.ID, h.CategoryKey, h.FamilyFit,
			alerttext.SeatDispatchLine("决策", fmt.Sprintf("假设#%d 新族机制票处置（读 STATE 本行+fin_opp_hypothesis#%d）", h.ID, h.ID))), 24*time.Hour)
	}
}
