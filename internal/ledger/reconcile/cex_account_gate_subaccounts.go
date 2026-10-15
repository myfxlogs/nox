// cex_account_gate_subaccounts.go —— gate earn（uni-lend 出借中）子账户查询实现
// （自 cex_account_subaccounts.go 按 venue 搬出——check-lines 拆分，零语义变化）。
// 行解析共享面 parseGateUniLendRows 在 cex_availability_gate.go（availability 同径）。
package reconcile

import (
	"context"
	"fmt"
	"strconv"

	v1 "nox/internal/gen/dashboardv1"
)

// gateUniLendRow /earn/uni/lends 行面（venue 实拍 10-15：真实字段 amount/lent_amount/
// frozen_amount——current_amount 系误字段恒空）。lent_amount=出借中本金。
type gateUniLendRow struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	Lent     string `json:"lent_amount"`
	Frozen   string `json:"frozen_amount"`
}

// parseGateUniLends lends 响应→分币持仓（D-285 零换算；equity=USDT 出借额）。
func parseGateUniLends(body []byte) (usdt float64, holdings []*v1.Holding, err error) {
	rows, err := parseGateUniLendRows(body)
	if err != nil {
		return 0, nil, err
	}
	for _, r := range rows {
		v, perr := strconv.ParseFloat(r.Lent, 64)
		if perr != nil || v <= 0 || r.Currency == "" {
			continue
		}
		uv := 0.0
		if r.Currency == "USDT" {
			uv = v
			usdt += v
		}
		holdings = append(holdings, &v1.Holding{Ccy: r.Currency, Qty: v, UsdtValue: uv})
	}
	return usdt, holdings, nil
}

// accountGateEarn gate 理财（uni-lend 出借中）：GET /earn/uni/lends 签名读——
// D-523③ 接 rest_poll 快照写口（gate:earn 键此前无写径，venue 实有出借零可见）。
func (q *CEXAccountQuerier) accountGateEarn(ctx context.Context) (*AccountInfo, error) {
	body, err := q.gateSignedGet(ctx, "/earn/uni/lends")
	if err != nil {
		return nil, fmt.Errorf("gate earn: %w", err)
	}
	usdt, holdings, err := parseGateUniLends(body)
	if err != nil {
		return nil, fmt.Errorf("gate earn json: %w", err)
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: usdt, Equity: usdt, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}
