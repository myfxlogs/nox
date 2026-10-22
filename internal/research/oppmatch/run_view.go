package oppmatch

// run_view.go —— 账本投影/报价源/缺口文案件（原 run.go 拆分，零行为变）。

import (
	"context"
	"fmt"
	"strings"

	"nox/internal/store/pgstore"
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
	return quoteForVenue(ctx, st, tid, "binance") // 模板级 APR=默认提案 venue（非 Exec/Earn 模板 venues=["binance"] 同域——venue 过滤不悬空）
}

// quoteForVenue venue 参化报价（term_basis venue 过滤须知提案 venue——
// 其余模板 venue 无关同签名转调）。
func quoteForVenue(ctx context.Context, st *pgstore.Store, tid, venue string) (*float64, error) {
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
	case tid == "cfd_swap_carry":
		f, ok, err = cfdSwapCarryQuote(ctx, st) // swap 收息−funding 对冲成本同源现值
	case tid == "cross_venue_spot":
		f, ok, err = crossVenueSpotQuote(ctx, st) // 同符号跨所 index_price 价差（单周转）
	case tid == "term_basis":
		f, ok, err = termBasisQuote(ctx, st, venue) // universe FUTURES venue 过滤最优行
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &f, nil
}
