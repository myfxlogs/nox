// run.go —— D-187：周期匹配 runner——账本×模板→提案 alert（D-168 episode 闸防抖，
// 提案级零自动执行）/缺口→fin_condition_gap→飞书请求。报价源=fin_cex_dci_quote/
// fin_cex_earn_apr 现有采集面（不重复造采集）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"

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
		Holdings: map[string]float64{}, Constraints: map[string]bool{},
		Permissions: map[string]bool{},
	}
	for _, c := range l.Credentials {
		v.Credentials[c.Venue] = true
		if c.VenueType != "" {
			v.Credentials[c.Venue+":"+c.VenueType] = true
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
		v.Constraints[c.Subject] = true
	}
	return v
}

// quoteFor 模板→报价源（dci_*=DCI 表 BTC+ETH 最佳、earn/cash=理财表；其余报价缺）。
func quoteFor(ctx context.Context, st *pgstore.Store, tid string) (*float64, error) {
	var f float64
	var ok bool
	var err error
	switch {
	case strings.HasPrefix(tid, "dci_"):
		f, ok, err = st.MaxDCIAPY(ctx, "")
	case tid == "earn_apr_lending" || tid == "cash_mgmt":
		f, ok, err = st.MaxEarnAPR(ctx)
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
	propN, gapN := 0, 0
	for _, r := range Match(in, view) {
		if len(r.Missing) == 0 {
			propN++
			apr := "报价缺"
			if r.APR != nil {
				apr = fmt.Sprintf("净APR≈%.1f%%", *r.APR*100)
			}
			if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
				"机会提案#%d %s（轨=%s，%s）", propN, r.TemplateID, r.ExecTrack, apr)); err != nil {
				log.Printf("[oppmatch] proposal alert %s: %v", r.TemplateID, err)
			}
			continue
		}
		for _, c := range r.Missing {
			id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
				TemplateID: r.TemplateID, Kind: c.Kind, Subject: c.Subject,
				Qualifier: c.Qualifier, RequestText: gapText(r.TemplateID, c)})
			if err != nil {
				return propN, gapN, err
			}
			gaps, err := st.ListConditionGaps(ctx, "open")
			if err != nil {
				return propN, gapN, err
			}
			for _, g := range gaps {
				if g.ID == id {
					if err := st.RequestConditionGap(ctx, id); err != nil {
						return propN, gapN, err
					}
					gapN++
				}
			}
		}
	}
	log.Printf("[oppmatch] scan done: %d templates, %d proposals, %d new gap requests",
		len(in), propN, gapN)
	return propN, gapN, nil
}
