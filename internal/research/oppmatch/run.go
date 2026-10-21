// run.go —— D-187：周期匹配 runner——账本×模板→提案 alert（D-168 episode 闸防抖，
// 提案级零自动执行）/缺口→fin_condition_gap→飞书请求。报价源=fin_cex_dci_quote/
// fin_cex_earn_apr 现有采集面（不重复造采集）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"nox/internal/platform/alerttext"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// AutoexecHook D-598：autoexec=true 模板提案落库即执行回派（cmd/core 装配注入
// dciexec.Executor.GoProposal；nil=跳过——含配置库未挂面的安全回落）。
var AutoexecHook func(ctx context.Context, proposalID int64) (string, error)

// dciProposalVenues dci 族提案 venue 枚举——执行轨覆盖集各出独立提案
// （fin_cex_dci_quote venue 维度；gate.DCIExecVenues 单源衍生排序定序）。
var dciProposalVenues = sortedVenues(gate.DCIExecVenues)

// sortedVenues venue 集→排序切片（提案枚举/保活键确定性——map 序不定）。
func sortedVenues(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

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
	// D-604 P2③：无流池桶权益消费点实拉（扫描拍首单次拉→map 引用——
	// 退回㉗伤③修法：禁逐桶 Refresh；拉败该桶跳过+warn 留痕，禁显示陈值）。
	if liveEquityMapFn != nil {
		lm := fetchLiveEquityMap(ctx)
		for i, b := range led.Buckets {
			if b.VenueType == "mt4" || b.VenueType == "mt5" {
				continue
			}
			if eq, ok := lm.poolEquity(b.Venue, b.VenueType, b.Currency, b.EquityRaw, "[oppmatch]"); ok {
				led.Buckets[i].EquityRaw = eq
			} else {
				led.Buckets[i].EquityRaw = 0 // 拉败=0 如实缺非陈值
			}
		}
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
		vs := venueSetFor(r.TemplateID)
		if r.TemplateID == "cash_mgmt" {
			vs = nil // cash_mgmt 产制归 idleGapScan 独域（自有 miss 阈值过期机制）——
			// match 环 dormant 判定/写面对其零消费，defer 反伤其存量提案。
		}
		if dormantAll(ctx, st, dormant, r.TemplateID, vs) {
			continue // 全 venue 休眠=零新提案零请求（命中 venue 已翻 deferred）
		}
		// auto 轨无执行器（IsExecTemplate 外族）→ 永不翻态的僵尸源：
		// 不产提案+存量 proposed 翻 deferred（D-204 同径复用）+info 留痕。
		// 处置语义钉死：auto 提案必有处置径（执行器或 deferred），龄不白涨。
		if r.ExecTrack == "auto" && !gate.IsExecTemplate(r.TemplateID) {
			if n, derr := st.DeferProposalsByPair(ctx, r.TemplateID, "binance"); derr == nil && n > 0 {
				log.Printf("[oppmatch] auto 轨无执行器 %s 翻 deferred %d 行", r.TemplateID, n)
			}
			if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
				"auto 轨无执行器 %s@binance 达标但不产提案（executor-missing）%s", r.TemplateID,
				alerttext.SeatDispatchLine("实施", fmt.Sprintf("%s 执行器补件评估（读 STATE 本行——达标轨无执行器=补件或下架裁定）", r.TemplateID)))); err != nil {
				log.Printf("[oppmatch] executor-missing alert %s: %v", r.TemplateID, err)
			}
			continue
		}
		if len(r.Missing) == 0 {
			// dci 族 venue 参化/earn_apr_lending 按有 USDT 报价行的 rail venue
			// 各出独立提案（报价所取 venue 如实写提案）；其余族照旧 binance 单 venue。
			venues := []string{"binance"}
			var venueAPR map[string]float64
			if _, ok := gate.ExecTemplates[r.TemplateID]; ok {
				venues = dciProposalVenues
			} else if gate.EarnTemplates[r.TemplateID] {
				if r.TemplateID == "cash_mgmt" {
					continue // cash_mgmt 产制归 idleGapScan 独域（APR 闸同源——扇出不过闸=绕闸径）
				}
				// earn 族按有 USDT 报价行的 rail venue 各出（报价所取 venue 如实写提案）。
				venues, venueAPR = earnQuoteVenues(ctx, st)
			}
			for _, venue := range venues {
				if dormant[r.TemplateID+"@"+venue] {
					continue // 半休眠：只压本 venue 面（存量翻 deferred 已在枚举前做）
				}
				// D-514+D-526 DCI 选档闸：venue 维度判——本 venue 无达标档只压本
				// venue 提案（权利金坍缩期不卖 tail 险；闸只挡 DCI 不碰 earn）。
				var gateNote string // D-526 返修①：过闸 σ/界/尾损注记落提案 params 可检索
				if r.TemplateID == "dci_buy_low" {
					ok, why := dciPremiumGate(ctx, st, venue)
					if !ok {
						if aerr := st.InsertAlertDedup(ctx, "info", fmt.Sprintf(
							"DCI 选档闸：dci_buy_low@%s %s——本拍「无达标档」不产提案", venue, why), 6*time.Hour); aerr != nil {
							log.Printf("[oppmatch] 选档闸 alert: %v", aerr)
						}
						continue
					}
					gateNote = why
				}
				propN++
				// 保活键=tid@venue 对；earn 族提案 rail 覆盖集各键都保活同
				// 旧语义（cash_mgmt 分 venue 由 idleGapScan 产/过期自管）。
				liveVenues := venues
				if gate.EarnTemplates[r.TemplateID] {
					liveVenues = sortedVenues(gate.EarnExecVenues)
				}
				for _, lv := range liveVenues {
					liveTmpls = append(liveTmpls, r.TemplateID+"@"+lv)
				}
				// earn_apr_lending 提案 venue=报价所取 venue——apr/证源随 venue
				// 换（跨 venue 顶档价不贴别所提案，执行轨按 venue 路由不悬空）。
				rv := r
				if va, ok := venueAPR[venue]; ok {
					rv.APR = &va
					rv.GrossEvidence = fmt.Sprintf("fin_cex_earn_apr %s USDT 最新行", venue)
				}
				apr := "报价缺"
				if rv.APR != nil {
					apr = fmt.Sprintf("apy=%.4g（venue原生口径）", *rv.APR)
				}
				// 提案实体化——行即稳定 id（同模板 pending 复用），
				// params=快照审计锚（执行时实时闸复验，非钉价）。
				params := proposalParams(ctx, st, rv, venue)
				if gateNote != "" {
					params["gate_note"] = gateNote
				}
				pid, created, err := st.CreateProposal(ctx, &pgstore.Proposal{
					TemplateID: r.TemplateID, ExecTrack: r.ExecTrack, Venue: venue,
					Params:      params,
					ExpectedAPR: rv.APR})
				if err != nil {
					log.Printf("[oppmatch] proposal row %s@%s: %v", r.TemplateID, venue, err)
					continue
				}
				// D-598：autoexec 模板提案落库即执行（豁免族=委员会级常设授权——
				// 提案只是记账/透出载体；执行结果照常 executed/告警透出）。
				if created && AutoexecHook != nil {
					if ae, aerr := st.TemplateAutoexec(ctx, r.TemplateID); aerr == nil && ae {
						if msg, gerr := AutoexecHook(ctx, pid); gerr != nil {
							log.Printf("[oppmatch] autoexec proposal#%d: %v", pid, gerr)
						} else {
							log.Printf("[oppmatch] autoexec proposal#%d: %s", pid, msg)
						}
					}
				}
				rv.Venue = venue
				card := evalProposal(ctx, st, rv, pid) // D-258 M4.1：一提案一卡（每扫一拍复算）
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
			dormantWritePrecheck(ctx, st, r.TemplateID, vs, reason)
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
	// cash_mgmt 保活键无条件追（返修 a：产制点唯一、保活面不变）。
	for _, lv := range sortedVenues(gate.EarnExecVenues) {
		liveTmpls = append(liveTmpls, "cash_mgmt@"+lv)
	}
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
	// D-612 息差轮动所内面：earn 池低效候选→earn_rotation 提案（autoexec 族，
	// 净值差终判在执行拍实拉——D-613 补正①）。
	rotateScan(ctx, st)
	// CFD swap 正息面→假设行（采集在跑消费断档接上；只产假设不碰钱）。
	swapScan(ctx, st)
	// 裸腿假设→对冲腿可行面（asset_equiv 类桥实读 universe；无腿如实注记）。
	hedgeScan(ctx, st)
	// S2 桥：approved 假设→可跑参数入册（族→沉点映射；无面告警待立票）。
	s2Bridge(ctx, st)
	preconditionWatch(ctx, st)  // D-625 P2：armed/trial 前提塌自盯+regime 标签读面
	trialPromotionScan(ctx, st) // D-625 P0 续件④：trial 晋级判据→tier 翻 armed
	// live 策略退役闸（委任式——死角实证触发；零产/亏损硬线+零持仓前置）。
	delegateRetire(ctx, st)
	log.Printf("[oppmatch] scan done: %d templates, %d proposals, %d new gap requests",
		len(in), propN, gapN)
	return propN, gapN, nil
}
