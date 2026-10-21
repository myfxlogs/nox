// eval.go —— D-258 M4.1：提案评估卡装配面。econ.Evaluate 纯算，
// 本件以 venue/账本查询喂 Term；机理不适用项给 mechanism: 证据，
// 无查询源=Evidence 空→卡面如实列 missing→unevaluable（零外推）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"nox/internal/research/econ"
	"nox/internal/store/pgstore"
)

// evalWindow 证据窗口（账本/报价中位口径 30d，对齐报价面 retention）。
const evalWindow = 30 * 24 * time.Hour

// 判据输入轨道标注（D-643/§11.3 步①——零行为变 warn 侦测先行）：
// live=venue 现态轨（即时报价/费率/账户参数——live-轨语义合法消费）；
// replay=venue 账本/历史序列（订单/腿/中位窗——回放帧候选）；
// point-query=paper 轨/探针估计（串轨病灶源——直喂 live-轨语义即 warn 立账）；
// prior=模板先验/机理声明（常量假设非数据轨）。
// 每词必带轨否则 missing——D-643「禁无标注混轨」的词源级落地。
const (
	trackLive       = "live"
	trackReplay     = "replay"
	trackPointQuery = "point-query"
	trackPrior      = "prior"
)

// evalProposal 提案→评估卡（五项逐项证源；读败项如实记 missing 不外推）。
// 返回卡面供 UpsertOppEval 落行。
func evalProposal(ctx context.Context, st *pgstore.Store, r Result, pid int64) *pgstore.OppEval {
	in := econ.EvalInput{TemplateID: r.TemplateID}
	// tr 词→数据源轨（含 hh 伪词=年化分母轨——串轨闸面）。
	tr := map[string]string{}
	trk := func(name, track, ev string) string {
		tr[name] = track
		return ev + "（track=" + track + "）"
	}
	if r.APR != nil {
		ev := "quoteFor:" + r.TemplateID
		if r.GrossEvidence != "" {
			ev = r.GrossEvidence
		}
		in.Gross = econ.Term{Value: *r.APR, Evidence: trk("gross", trackLive, ev)}
	}
	dci := strings.HasPrefix(r.TemplateID, "dci_") || r.TemplateID == "option_spread"
	earn := r.TemplateID == "earn_apr_lending" || r.TemplateID == "cash_mgmt" || r.TemplateID == "earn_rotation"
	switch {
	case dci:
		// DCI 申购：venue 报价 APY 申购即锁——费/滑点/市窗机理不适用；
		// 失败成本=败单账本实证（拒绝零资金损耗）。
		in.Fee = econ.Term{Evidence: trk("fee", trackPrior, "mechanism:DCI申购费不适用")}
		in.Slippage = econ.Term{Evidence: trk("slippage", trackPrior, "mechanism:申购锁价滑点不适用")}
		in.Window = econ.Term{Evidence: trk("window", trackPrior, "mechanism:APY申购即锁市窗不适用")}
		if n, err := st.DCIRejectCount(ctx, r.TemplateID, evalWindow); err == nil {
			in.Failure = econ.Term{Evidence: trk("failure", trackReplay, fmt.Sprintf("fin_dci_order %d败/30d零资金损耗", n))}
		}
	case earn:
		in.Fee = econ.Term{Evidence: trk("fee", trackPrior, "mechanism:理财申赎费不适用")}
		in.Slippage = econ.Term{Evidence: trk("slippage", trackPrior, "mechanism:理财报价滑点不适用")}
		if m, ok, err := st.MedianEarnAPR(ctx, evalWindow); err == nil && ok && r.APR != nil {
			w := *r.APR - m
			if w < 0 {
				w = 0
			}
			in.Window = econ.Term{Value: w, Evidence: trk("window", trackReplay, "fin_cex_earn_apr 30d中位")}
		}
		// D-2xx 失败证源=fin_venue_reject earn_redeem 撞坑累计（口径同 DCI：
		// 申赎拒绝发生在资金移动前=零资金损耗）。
		if n, err := st.RejectHits(ctx, "earn_redeem", evalWindow); err == nil {
			in.Failure = econ.Term{Evidence: trk("failure", trackReplay, fmt.Sprintf("fin_venue_reject earn_redeem %d败/30d零资金损耗", n))}
		}
	case r.TemplateID == "funding_carry":
		// funding=永续随市：滑点=cex_spread；市窗=facts funding 30d 中位衰减。
		// D-2xx 费=perp taker 中位×双腿 按 realized 持有窗年化（PERP_FUNDING
		// CLOSED 时长账本；零行=缺证据不外推）；失败=position_legs FAILED 账本
		// ——0 败=零损耗证据，>0=成本未计价 fail-closed 仍缺（不估单价）。
		// D-277 G4 修复：fin_cex_spread.symbol 写侧存基础币形（BTC/SOL/TRX/ETH），
		// 键须用基名——"BTCUSDT" 永不命中（8526 行/日鲜写侧键义不动）。
		if hs, ok, err := st.LatestDepthHalfSpread(ctx, r.Venue, ""); err == nil && ok {
			in.Slippage = econ.Term{Value: hs, Evidence: trk("slippage", trackLive, fmt.Sprintf("fin_cex_depth %s 中位半价差", r.Venue))}
		} else if b, ok, err := st.LatestSpreadBps(ctx, "BTC"); err == nil && ok {
			in.Slippage = econ.Term{Value: b / 100, Evidence: trk("slippage", trackLive, "fin_cex_spread:BTC")}
		}
		if m, ok, err := st.MedianFactValue(ctx, "funding", evalWindow); err == nil && ok && r.APR != nil {
			w := *r.APR - m
			if w < 0 {
				w = 0
			}
			in.Window = econ.Term{Value: w, Evidence: trk("window", trackReplay, "facts:funding 30d中位")}
		}
		// hh 持有窗三序取径（D-642 发现一病灶修）：realized 账本 → 模板持有先验
		// （fin_opp_template.hold_hours_prior，1057 迁移判据落表）→ missing。
		// paper 探针退出中位（perp_perp exited ~3h）=探针纪律非策略持有语义，
		// 禁作 fee 年化分母——用之即 3h 复利化假费假拒（1383%→−550 实证）。
		hh, hhSrc := 0.0, ""
		if v, ok, err := st.MedianHoldHours(ctx, []string{"PERP_FUNDING"}, evalWindow); err == nil && ok && v > 0 {
			hh, hhSrc = v, fmt.Sprintf("PERP_FUNDING 中位持有%.1fh", v)
			tr["hh"] = trackReplay
		} else if v, ok2, err2 := st.HoldHoursPrior(ctx, r.TemplateID); err2 == nil && ok2 {
			hh, hhSrc = v, fmt.Sprintf("模板持有先验%.0fh", v)
			tr["hh"] = trackPrior
		}
		if t, ok, err := st.MedianTakerFeeBps(ctx); err == nil && ok && hh > 0 {
			// fee_apr = 双腿 taker%×（8760h/持有h）年化
			in.Fee = econ.Term{Value: t * 2 / 100 * (8760 / hh),
				Evidence: trk("fee", trackLive, fmt.Sprintf("exchange_configs taker中位%.2fbps×2腿 / %s", t, hhSrc))}
		}
		if in.Fee.Evidence == "" && hh > 0 { // venue 账本径缺→paper 轨绕行（估费标 paper轨 口径不混 venue 词；hh 同三序径）
			if ratio, _, ok, err := st.MedianPaperFeeRatio(ctx, "perp_perp", evalWindow); err == nil && ok {
				in.Fee = econ.Term{Value: ratio * 100 * (8760 / hh),
					Evidence: trk("fee", trackPointQuery, fmt.Sprintf("paper轨估费 perp_perp 中位%.4f%%/qty ×8760/%.1fh（%s）", ratio*100, hh, hhSrc))}
			}
		}
		if n, err := st.LegFailCount(ctx, []string{"PERP_FUNDING"}, evalWindow); err == nil && n == 0 {
			in.Failure = econ.Term{Evidence: trk("failure", trackReplay, "position_legs 0腿败/30d")}
		}
	case r.TemplateID == "term_basis":
		// D-637 序③：gross=BasisAnn 原生年化已在；fee=taker×2 按到期持有窗年化
		// （universe FUTURES expiry_ts 中位——无到期行=缺不外推）；slippage=无每
		// venue 盘口深源如实 missing；window=机理（basis_ann 已含收敛至到期）。
		// fee/slippage 同到期窗年化径（×8760/hh——dts 块内算；无到期行两词同 missing 不掺径）
		if rows, err2 := st.ListCexUniverse(ctx); err2 == nil {
			var dts []float64
			for _, u := range rows {
				if u.ContractType == "FUTURES" && u.ExpiryTS != nil {
					if d := time.Until(*u.ExpiryTS).Hours(); d > 24 {
						dts = append(dts, d)
					}
				}
			}
			if len(dts) > 0 {
				sort.Float64s(dts)
				hh := dts[len(dts)/2]
				tr["hh"] = trackLive // universe 到期窗=venue 现态域
				if t, ok, err := st.MedianTakerFeeBps(ctx); err == nil && ok {
					in.Fee = econ.Term{Value: t * 2 / 100 * (8760 / hh),
						Evidence: trk("fee", trackLive, fmt.Sprintf("exchange_configs taker中位%.2fbps×2腿 / universe 到期中位%.1fh", t, hh))}
				}
				if hs, ok, err := st.LatestDepthHalfSpread(ctx, r.Venue, ""); err == nil && ok {
					in.Slippage = econ.Term{Value: hs * (8760 / hh),
						Evidence: trk("slippage", trackLive, fmt.Sprintf("fin_cex_depth %s 中位半价差×到期窗年化(%.1fh)", r.Venue, hh))}
				}
			}
		}
		if n, err := st.LegFailCount(ctx, []string{"TERM_BASIS"}, evalWindow); err == nil && n == 0 {
			in.Failure = econ.Term{Evidence: trk("failure", trackReplay, "position_legs 0腿败/30d")}
		}
		in.Window = econ.Term{Evidence: trk("window", trackPrior, "mechanism:basis_ann 年化已含收敛至到期")}
	case r.TemplateID == "cross_venue_spot":
		// D-637 序③ 单位裁定钦定：本族全部词=per-turn 单周转口径（非 APR——
		// APR 槽禁静默塞 per-turn 值故 evidence 串一律带 per-turn 单位标）。
		if in.Gross.Evidence != "" {
			in.Gross.Evidence += " per-turn"
		}
		if t, ok, err := st.MedianTakerFeeBps(ctx); err == nil && ok {
			in.Fee = econ.Term{Value: t * 2 / 100, Evidence: trk("fee", trackLive, fmt.Sprintf("exchange_configs taker中位%.2fbps×2腿 per-turn", t))}
		}
		if hs, ok, err := st.LatestDepthHalfSpread(ctx, r.Venue, ""); err == nil && ok {
			in.Slippage = econ.Term{Value: hs, Evidence: trk("slippage", trackLive, fmt.Sprintf("fin_cex_depth %s 中位半价差 per-turn", r.Venue))}
		}
		in.Failure = econ.Term{Evidence: trk("failure", trackPrior, "mechanism:per-turn 单周转无腿账本域")}
		in.Window = econ.Term{Evidence: trk("window", trackPrior, "mechanism:per-turn 单周转非年化口径")}
	case r.TemplateID == "cfd_swap_carry":
		// D-637 序③：gross 现 nil=canonical 无配对 fail-closed 如实；MT 腿成本
		// 无账本面=missing（mt5-quotes-history 域无费率端点在案——不凑数）。
		if n, err := st.LegFailCount(ctx, []string{"XASSET_CARRY", "XASSET"}, evalWindow); err == nil && n == 0 {
			in.Failure = econ.Term{Evidence: trk("failure", trackReplay, "position_legs 0腿败/30d")}
		}
	}
	// D-643 串轨 warn 闸：point-query 源直喂 live-轨语义（年化/持有期类）
	// 即记 warn 立账——不拒（拒级归 D-633 开口，本件侦测先行零行为变）。
	// hh 伪词同面检（年化分母轨）。dedup 键含模板+词——同卡逐拍不刷屏。
	for name, track := range tr {
		if track != trackPointQuery {
			continue
		}
		if aerr := st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
			"串轨侦测（D-643）：%s 评估卡 %s 词源 point-query 直喂 live-轨语义（提案#%d）——侦测立账不拒", r.TemplateID, name, pid), 24*time.Hour); aerr != nil {
			log.Printf("[oppmatch] 串轨 warn %s/%s: %v", r.TemplateID, name, aerr)
		}
	}
	card := econ.Evaluate(in)
	ev := map[string]any{"terms": card.Evidence, "tracks": tr}
	if len(card.Missing) > 0 {
		ev["missing"] = card.Missing
	}
	fp := func(i int) *float64 {
		if _, ok := card.Evidence[terms[i]]; !ok {
			return nil // 缺证据项=NULL 不落 0 伪值
		}
		v := card.Terms[i]
		return &v
	}
	var net *float64
	if card.Verdict != "unevaluable" {
		net = &card.Net
	}
	return &pgstore.OppEval{
		ProposalID: pid, TemplateID: r.TemplateID,
		GrossAPR: fp(0), FeeAPR: fp(1), SlippageAPR: fp(2),
		FailureAPR: fp(3), WindowAPR: fp(4), NetAPR: net,
		Verdict: card.Verdict, EvidenceRefs: ev,
	}
}

var terms = [5]string{"gross", "fee", "slippage", "failure", "window"}
