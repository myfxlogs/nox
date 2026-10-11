// run.go —— D-187：周期匹配 runner——账本×模板→提案 alert（D-168 episode 闸防抖，
// 提案级零自动执行）/缺口→fin_condition_gap→飞书请求。报价源=fin_cex_dci_quote/
// fin_cex_earn_apr 现有采集面（不重复造采集）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/dciexec"
	"arb/internal/store/pgstore"
)

// gapText 缺口→业主请求文案（投资人域动作四型：开户/凭据/权限/资金划转/约束喂入）。
func gapText(tid string, c Cond) string {
	switch c.Kind {
	case "credential":
		return fmt.Sprintf("请补 %s 账户凭据（%s 能力）", c.Subject, c.Qualifier)
	case "capital":
		return fmt.Sprintf("请划转 %s ≥%.0f（USD 口径）", c.Subject, val(c.MinVal))
	case "holding":
		return fmt.Sprintf("请确认 %s 持仓 ≥%.4g", c.Subject, val(c.MinVal))
	case "permission":
		return fmt.Sprintf("请开通 %s 权限（%s）", c.Subject, c.Qualifier)
	case "constraint":
		return fmt.Sprintf("请喂约束 %s（%s）", c.Subject, c.Qualifier)
	}
	return fmt.Sprintf("请补条件 %s:%s", c.Kind, c.Subject)
}

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// ledgerView pgstore.CapabilityLedger→求值投影。
func ledgerView(l *pgstore.CapabilityLedger) LedgerView {
	v := LedgerView{
		Credentials: map[string]bool{}, CapitalBy: map[string]float64{},
		Holdings: map[string]float64{}, Constraints: map[string]ConstraintVal{},
		Permissions: map[string]bool{}, PermStates: map[string]string{},
	}
	for _, c := range l.Credentials {
		v.Credentials[c.Venue] = true
		if c.VenueType != "" {
			v.Credentials[c.Venue+":"+c.VenueType] = true
			v.Credentials["type:"+c.VenueType] = true // type 级（mt5/futures/option）
		}
	}
	for _, b := range l.Buckets {
		switch strings.ToUpper(b.Currency) {
		case "USD", "USDT", "USDC":
			v.CapitalUSD += b.EquityRaw
		default:
			v.CapitalBy[strings.ToUpper(b.Currency)] += b.EquityRaw
		}
	}
	for _, h := range l.Holdings {
		v.Holdings[strings.ToUpper(h.Asset)] += h.Qty
	}
	for _, c := range l.Constraints {
		v.Constraints[c.Subject] = ConstraintVal{Qualifier: c.Qualifier, Value: c.ValueNum}
	}
	for _, p := range l.Permissions {
		if p.State == "present" {
			v.Permissions[p.Permission] = true
		}
		v.PermStates[p.Permission] = p.State // D-193② 三态原样（unknown 不误收不误开）
	}
	return v
}

// quoteFor 模板→报价源（dci_*=DCI 表 BTC+ETH 最佳、earn/cash=理财表；其余报价缺）。
func quoteFor(ctx context.Context, st *pgstore.Store, tid string) (*float64, error) {
	var f float64
	var ok bool
	var err error
	switch {
	case strings.HasPrefix(tid, "dci_") || tid == "option_spread":
		f, ok, err = st.MaxDCIAPY(ctx, "") // option_spread 暂代报价（DCI 同族衍生品面）
	case tid == "earn_apr_lending" || tid == "cash_mgmt":
		f, ok, err = st.MaxEarnAPR(ctx)
	case tid == "funding_carry":
		f, ok, err = st.MaxFactValue(ctx, "funding") // facts 永续 funding（pct_annualized）
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &f, nil
}

// RunOnce 单轮匹配（日频调用）：提案=InsertAlert info（episode 闸天然防抖）；
// 缺口=OpenConditionGap 幂等+RequestConditionGap（仅 open→requested 推一次）。
// 返回提案数/新请求缺口数。
func RunOnce(ctx context.Context, st *pgstore.Store) (int, int, error) {
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
	var liveTmpls []string // 当轮达标模板集（过期判定并入扫描拍——零新定时器）
	for _, r := range Match(in, view) {
		if len(r.Missing) == 0 {
			propN++
			liveTmpls = append(liveTmpls, r.TemplateID)
			apr := "报价缺"
			if r.APR != nil {
				apr = fmt.Sprintf("apy=%.4g（venue原生口径）", *r.APR)
			}
			// D-189①②：提案实体化——行即稳定 id（同模板 pending 复用），
			// params=快照审计锚（执行时实时闸复验，非钉价）。
			pid, created, err := st.CreateProposal(ctx, &pgstore.Proposal{
				TemplateID: r.TemplateID, ExecTrack: r.ExecTrack, Venue: "binance",
				Params:      proposalParams(ctx, st, r),
				ExpectedAPR: r.APR})
			if err != nil {
				log.Printf("[oppmatch] proposal row %s: %v", r.TemplateID, err)
				continue
			}
			tag := "提案#"
			if !created {
				tag = "提案#（在册）"
			}
			if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
				"机会%s%d %s（轨=%s，%s）", tag, pid, r.TemplateID, r.ExecTrack, apr)); err != nil {
				log.Printf("[oppmatch] proposal alert %s: %v", r.TemplateID, err)
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
	// D-193①②：对账哨兵并入扫描拍（零新定时器）。
	checkVenueFloat(ctx, st, in)
	reconcileGaps(ctx, st, in, view)
	// D-189⑤：过期并入扫描拍——当轮不再达标的 pending 提案→expired。
	if n, err := st.ExpireProposalsExcept(ctx, liveTmpls); err != nil {
		log.Printf("[oppmatch] expire proposals: %v", err)
	} else if n > 0 {
		log.Printf("[oppmatch] expired %d stale proposals", n)
	}
	log.Printf("[oppmatch] scan done: %d templates, %d proposals, %d new gap requests",
		len(in), propN, gapN)
	return propN, gapN, nil
}

// checkVenueFloat D-193①：venue 悬空自检——仅对已接执行轨模板（executor 映射集内），
// 其 credential 条件声明 venue 须落在执行轨 venue 集内；不符→warn alert 一次
// （episode 防抖）。未接执行轨模板声明新 venue=合法投资人请求不报。
// 内部不一致报系统不修数据（修种子=策略治理权）。
func checkVenueFloat(ctx context.Context, st *pgstore.Store, in []Template) {
	execVenue := dciexec.ExecTemplateVenues()
	for _, t := range in {
		ev, ok := execVenue[t.ID]
		if !ok {
			continue // 未接执行轨=不报
		}
		for _, c := range t.Conds {
			if c.Kind != "credential" {
				continue
			}
			ven := c.Subject
			if i := strings.Index(ven, ":"); i > 0 {
				ven = ven[:i]
			}
			if ven != ev {
				if err := st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
					"模板venue悬空：%s 声明凭据 %s，执行轨 venue=%s（需求行漂移，报系统不修数据）",
					t.ID, c.Subject, ev)); err != nil {
					log.Printf("[oppmatch] venue-float alert %s: %v", t.ID, err)
				}
			}
		}
	}
}

// reconcileGaps D-193②：缺口自动收口+复开——
// (a) 条件存在且满足→fulfilled；(b) 条件行已从模板集移除→fulfilled+note「需求已变更」；
// (c) fulfilled 行条件再失→回 requested（同 key 复用不插新行，episode 闸防抖照旧）；
// unknown 态（探针未就绪/传输层败）不收不复开。
func reconcileGaps(ctx context.Context, st *pgstore.Store, in []Template, view LedgerView) {
	condByKey := map[string]Cond{} // template|kind|subject → 当前模板条件集
	for _, t := range in {
		for _, c := range t.Conds {
			condByKey[t.ID+"|"+c.Kind+"|"+c.Subject] = c
		}
	}
	gaps, err := st.ListConditionGaps(ctx, "")
	if err != nil {
		log.Printf("[oppmatch] gap reconcile list: %v", err)
		return
	}
	for _, g := range gaps {
		if g.Status == "waived" {
			continue // 业主放弃线不动
		}
		key := g.TemplateID + "|" + g.Kind + "|" + g.Subject
		c, ok := condByKey[key]
		if !ok {
			// 条件行被删/模板停用→需求已变更收口（仅 open/requested）。
			if err := st.FulfillConditionGap(ctx, g.ID, "需求已变更"); err != nil {
				log.Printf("[oppmatch] gap fulfill(stale) %d: %v", g.ID, err)
			}
			continue
		}
		switch EvalCondTri(view, c) {
		case "ok": // 条件满足自动收口
			if err := st.FulfillConditionGap(ctx, g.ID, "条件满足自动收口"); err != nil {
				log.Printf("[oppmatch] gap fulfill %d: %v", g.ID, err)
			}
		case "miss": // fulfilled 行条件再失→复开（反向跳变防静默）
			if err := st.ReopenConditionGap(ctx, g.ID); err != nil {
				log.Printf("[oppmatch] gap reopen %d: %v", g.ID, err)
			}
		}
	}
}

// proposalParams 提案快照参数（审计锚非钉价）：dci 模板补最优档 product/strike/tenor
// 快照；其余仅 apr——执行时实时闸复验为准（禁区：禁按提案时 strike 硬执行）。
func proposalParams(ctx context.Context, st *pgstore.Store, r Result) map[string]any {
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
		// D-191-R8：快照同界——bound=现货参照×%OTM 闸（无参照→无界降级，
		// 快照=审计锚非钉价，执行时实时闸复验不变）。
		var bound float64
		if spot, err := st.LatestSpreadSpotRef(ctx, "BTC"); err == nil && spot > 0 {
			bound = dciexec.StrikeBound(opt, spot)
		}
		if qs, err := st.ListDCIQuotes(ctx, "binance", "BTC", opt, inv, 90, bound); err == nil && len(qs) > 0 {
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
