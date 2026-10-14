// run.go —— D-187：周期匹配 runner——账本×模板→提案 alert（D-168 episode 闸防抖，
// 提案级零自动执行）/缺口→fin_condition_gap→飞书请求。报价源=fin_cex_dci_quote/
// fin_cex_earn_apr 现有采集面（不重复造采集）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// dciProposalVenues dci 族提案 venue 枚举——报价面采集在跑的 venue 各出
// 独立提案（fin_cex_dci_quote venue 维度；新 venue 采集接入加列即可）。
var dciProposalVenues = []string{"binance", "okx"}

// RunOnce 单轮匹配（日频调用）：提案=InsertAlert info（episode 闸天然防抖）；
// 缺口=OpenConditionGap 幂等+RequestConditionGap（仅 open→requested 推一次）。
// D-247：缺口请求生成前先过可行性预检（capitalBase/perStrategyCap=资金基数×
// 单策略上限口径；≤0 由 effCap 收口不拦）——不过→模板休眠约束行+原因，零请求。
// 返回提案数/新请求缺口数。
func RunOnce(ctx context.Context, st *pgstore.Store, capitalBase, perStrategyCap float64) (int, int, error) {
	ts, err := st.ListOppTemplates(ctx)
	if err != nil {
		return 0, 0, err
	}
	led, err := st.ListCapabilityLedger(ctx)
	if err != nil {
		return 0, 0, err
	}
	view := ledgerView(led)
	var in []Template
	for _, t := range ts {
		if !t.Enabled {
			continue
		}
		tp := Template{ID: t.ID, ExecTrack: t.ExecTrack}
		for _, c := range t.Conds {
			tp.Conds = append(tp.Conds, Cond{Kind: c.Kind, Subject: c.Subject,
				Qualifier: c.Qualifier, MinVal: c.MinVal})
		}
		tp.APR, err = quoteFor(ctx, st, t.ID)
		if err != nil {
			return 0, 0, err
		}
		in = append(in, tp)
	}
	allGaps, err := st.ListConditionGaps(ctx, "")
	if err != nil {
		return 0, 0, err
	}
	gapByKey := map[string]string{} // template|kind|subject → status（预取消 N+1）
	for _, g := range allGaps {
		gapByKey[g.TemplateID+"|"+g.Kind+"|"+g.Subject] = g.Status
	}
	propN, gapN := 0, 0
	var liveTmpls []string // 当轮达标 tid@venue 对集（过期判定并入扫描拍——零新定时器）
	// 休眠轨集（约束表 subject 'dormant:<tmpl>@<venue>' value_num=1）——
	// 命中跳过 CreateProposal+存量 proposed 翻 deferred；读败不阻扫描（集空降级）。
	dormant := map[string]bool{}
	if cons, cerr := st.ListCapabilityConstraints(ctx); cerr == nil {
		dormant = pgstore.DormantPairs(cons)
	}
	feas := NewFeasibility(led, capitalBase, perStrategyCap) // D-247 可行性预检面
	for _, r := range Match(in, view) {
		if dormant[r.TemplateID+"@binance"] {
			if n, derr := st.DeferProposalsByPair(ctx, r.TemplateID, "binance"); derr == nil && n > 0 {
				log.Printf("[oppmatch] 休眠轨 %s@binance 翻 deferred %d 行", r.TemplateID, n)
			}
			if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
				"休眠轨（D-191R7）%s@binance 达标但跳过提案", r.TemplateID)); err != nil {
				log.Printf("[oppmatch] dormant alert %s: %v", r.TemplateID, err)
			}
			continue // 零新提案（info 级=台账留痕零推送）
		}
		// auto 轨无执行器（IsExecTemplate 外族）→ 永不翻态的僵尸源：
		// 不产提案+存量 proposed 翻 deferred（D-204 同径复用）+info 留痕。
		// 处置语义钉死：auto 提案必有处置径（执行器或 deferred），龄不白涨。
		if r.ExecTrack == "auto" && !gate.IsExecTemplate(r.TemplateID) {
			if n, derr := st.DeferProposalsByPair(ctx, r.TemplateID, "binance"); derr == nil && n > 0 {
				log.Printf("[oppmatch] auto 轨无执行器 %s 翻 deferred %d 行", r.TemplateID, n)
			}
			if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
				"auto 轨无执行器 %s@binance 达标但不产提案（executor-missing）", r.TemplateID)); err != nil {
				log.Printf("[oppmatch] executor-missing alert %s: %v", r.TemplateID, err)
			}
			continue
		}
		if len(r.Missing) == 0 {
			// dci 族 venue 参化：同模板按 venue 各出独立提案（pending 幂等键
			// template_id+venue）；其余族照旧 binance 单 venue。
			venues := []string{"binance"}
			if _, ok := gate.ExecTemplates[r.TemplateID]; ok {
				venues = dciProposalVenues
			}
			for _, venue := range venues {
				if dormant[r.TemplateID+"@"+venue] {
					continue
				}
				// D-514 DCI 溢价闸：venue 维度判——本 venue 无达标档只压本 venue
				// 提案（权利金坍缩期不卖 tail 险；闸只挡 DCI 不碰 earn）。
				if r.TemplateID == "dci_buy_low" {
					if ok, why := dciPremiumGate(ctx, st, venue); !ok {
						if aerr := st.InsertAlertDedup(ctx, "info", fmt.Sprintf(
							"D-514 溢价闸：dci_buy_low@%s %s——本拍「无达标档」不产提案", venue, why), 6*time.Hour); aerr != nil {
							log.Printf("[oppmatch] 溢价闸 alert: %v", aerr)
						}
						continue
					}
				}
				propN++
				// 保活键=tid@venue 对；earn 族提案由 idleGapScan 分 venue 产
				// （主环只产 binance）——earnExecVenues 各键都保活同旧语义。
				liveVenues := venues
				if gate.EarnTemplates[r.TemplateID] {
					liveVenues = liveVenues[:0]
					for v := range earnExecVenues {
						liveVenues = append(liveVenues, v)
					}
				}
				for _, lv := range liveVenues {
					liveTmpls = append(liveTmpls, r.TemplateID+"@"+lv)
				}
				apr := "报价缺"
				if r.APR != nil {
					apr = fmt.Sprintf("apy=%.4g（venue原生口径）", *r.APR)
				}
				// 提案实体化——行即稳定 id（同模板 pending 复用），
				// params=快照审计锚（执行时实时闸复验，非钉价）。
				pid, created, err := st.CreateProposal(ctx, &pgstore.Proposal{
					TemplateID: r.TemplateID, ExecTrack: r.ExecTrack, Venue: venue,
					Params:      proposalParams(ctx, st, r, venue),
					ExpectedAPR: r.APR})
				if err != nil {
					log.Printf("[oppmatch] proposal row %s@%s: %v", r.TemplateID, venue, err)
					continue
				}
				card := evalProposal(ctx, st, r, pid) // D-258 M4.1：一提案一卡（每扫一拍复算）
				streak, uerr := st.UpsertOppEval(ctx, card)
				if uerr != nil {
					log.Printf("[oppmatch] eval card proposal#%d: %v", pid, uerr)
				}
				// 处置②：unevaluable 连拍≥3 翻 deferred——评估证据持续不足不许无限挂
				// proposed（阈值=3 扫拍：单拍数据面波动不致误杀，连拍才定性「持续不足」）。
				if card.Verdict == "unevaluable" && streak >= 3 {
					if n, derr := st.DeferProposalsByPair(ctx, r.TemplateID, venue); derr == nil && n > 0 {
						log.Printf("[oppmatch] unevaluable 连拍 %d 翻 deferred（proposal#%d）", streak, pid)
					}
					if aerr := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
						"提案#%d %s@%s 评估证据连拍不足（unevaluable×%d）→deferred", pid, r.TemplateID, venue, streak)); aerr != nil {
						log.Printf("[oppmatch] uneval-defer alert #%d: %v", pid, aerr)
					}
				}
				tag := "提案#"
				if !created {
					tag = "提案#（在册）"
				}
				if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
					"机会%s%d %s@%s（轨=%s，%s，评估卡=%s）", tag, pid, r.TemplateID, venue, r.ExecTrack, apr, card.Verdict)); err != nil {
					log.Printf("[oppmatch] proposal alert %s: %v", r.TemplateID, err)
				}
			}
			continue
		}
		// D-247 可行性预检：缺口请求前先判能不能投——不过→模板休眠+原因留痕零请求。
		if ok, reason := feas.Check(r); !ok {
			if uerr := st.UpsertCapabilityConstraint(ctx,
				DormantConstraint(r.TemplateID, "binance", reason)); uerr != nil {
				log.Printf("[oppmatch] dormant constraint %s: %v", r.TemplateID, uerr)
				continue
			}
			if n, derr := st.DeferProposalsByPair(ctx, r.TemplateID, "binance"); derr == nil && n > 0 {
				log.Printf("[oppmatch] 预检休眠 %s 翻 deferred %d 行", r.TemplateID, n)
			}
			if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
				"可行性预检不过（D-247）%s@binance 休眠：%s", r.TemplateID, reason)); err != nil {
				log.Printf("[oppmatch] precheck alert %s: %v", r.TemplateID, err)
			}
			continue
		}
		for _, c := range r.Missing {
			key := r.TemplateID + "|" + c.Kind + "|" + c.Subject
			st_, seen := gapByKey[key]
			if seen && st_ != "open" { // requested/fulfilled/waived 行零重推
				continue
			}
			id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
				TemplateID: r.TemplateID, Kind: c.Kind, Subject: c.Subject,
				Qualifier: c.Qualifier, RequestText: gapText(r.TemplateID, c)})
			if err != nil {
				return propN, gapN, err
			}
			if err := st.RequestConditionGap(ctx, id); err != nil {
				return propN, gapN, err
			}
			gapByKey[key] = "requested"
			gapN++
		}
	}
	// 对账哨兵并入扫描拍（零新定时器）。
	checkVenueFloat(ctx, st, in)
	reconcileGaps(ctx, st, in, view)
	// 过期并入扫描拍——当轮不再达标的 pending 提案→expired。
	if n, err := st.ExpireProposalsExcept(ctx, liveTmpls); err != nil {
		log.Printf("[oppmatch] expire proposals: %v", err)
	} else if n > 0 {
		log.Printf("[oppmatch] expired %d stale proposals", n)
	}
	// D-260 M4.2：生命周期三件并入扫描拍（覆盖/SLA/晋级评估——零新定时器）。
	runLifecycle(ctx, st, in)
	// 跨 venue 机会差调拨提案（业主令 10-14——同资产 earn APR 差超阈→请求箱）。
	xferScan(ctx, st)
	// 闲置 CEX 池缺口扫描（okx unified 类——无 earn 执行径如实记缺口行）。
	idleGapScan(ctx, st)
	// CFD swap 正息面→假设行（采集在跑消费断档接上；只产假设不碰钱）。
	swapScan(ctx, st)
	// 裸腿假设→对冲腿可行面（asset_equiv 类桥实读 universe；无腿如实注记）。
	hedgeScan(ctx, st)
	// live 策略退役闸（委任式——死角实证触发；零产/亏损硬线+零持仓前置）。
	delegateRetire(ctx, st)
	log.Printf("[oppmatch] scan done: %d templates, %d proposals, %d new gap requests",
		len(in), propN, gapN)
	return propN, gapN, nil
}

// proposalParams 提案快照参数（审计锚非钉价）：dci 模板补最优档 product/strike/tenor
// 快照；其余仅 apr——执行时实时闸复验为准（禁区：禁按提案时 strike 硬执行）。
func proposalParams(ctx context.Context, st *pgstore.Store, r Result, venue string) map[string]any {
	p := map[string]any{}
	if r.APR != nil {
		p["apr"] = *r.APR
	}
	var opt, inv string
	switch r.TemplateID {
	case "dci_sell_high_covered_call":
		opt, inv = "CALL", "BTC"
	case "dci_buy_low":
		opt, inv = "PUT", "USDT"
	}
	if opt != "" {
		// 快照同界——bound=现货参照×约束表 OTM 界（无参照/界行缺
		// →无界快照降级；快照=审计锚非钉价，执行时实时闸复验不变）。
		bound := dciStrikeBound(ctx, st, opt)
		if qs, err := st.ListDCIQuotes(ctx, venue, "BTC", opt, inv, 90, bound); err == nil && len(qs) > 0 {
			p["product_ref"] = qs[0].ProductRef
			if qs[0].Strike != nil {
				p["strike"] = *qs[0].Strike
			}
			if qs[0].TenorDays != nil {
				p["tenor_days"] = *qs[0].TenorDays
			}
		}
	}
	return p
}
