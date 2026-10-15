// cex_account_gate_subaccounts.go —— gate earn（uni-lend 出借中）子账户查询实现
// （自 cex_account_subaccounts.go 按 venue 搬出——check-lines 拆分，零语义变化）。
// 行解析共享面 parseGateUniLendRows 在 cex_availability_gate.go（availability 同径）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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

// accountGateEarn gate 理财（uni-lend 出借中 + 双币/结构化在仓单）：GET /earn/uni/lends
// 签名读——D-523③ 接 rest_poll 快照写口（gate:earn 键此前无写径，venue 实有出借零可见）。
// 同型漏读补齐（okx DCI 10 USDT 零行实证同族）：gate 双币 /earn/dual/orders 与结构化
// /earn/structured/orders 走独立端点不在 uni-lends 读面，在仓本金并入 earn 池。
// 三端点读败皆 fail-closed；行缺 ccy/amt/状态字段返错不静默跳。
func (q *CEXAccountQuerier) accountGateEarn(ctx context.Context) (*AccountInfo, error) {
	body, err := q.gateSignedGet(ctx, "/earn/uni/lends")
	if err != nil {
		return nil, fmt.Errorf("gate earn: %w", err)
	}
	_, holdings, err := parseGateUniLends(body)
	if err != nil {
		return nil, fmt.Errorf("gate earn json: %w", err)
	}
	per := map[string]float64{}
	var ccys []string
	for _, h := range holdings {
		if _, ok := per[h.Ccy]; !ok {
			ccys = append(ccys, h.Ccy)
		}
		per[h.Ccy] += h.Qty
	}
	add := func(c string, v float64) {
		if _, ok := per[c]; !ok {
			ccys = append(ccys, c)
		}
		per[c] += v
	}
	if err := q.gateDualOrders(ctx, add); err != nil {
		return nil, err
	}
	if err := q.gateStructuredOrders(ctx, add); err != nil {
		return nil, err
	}
	eq := 0.0
	holdings = holdings[:0]
	for _, c := range ccys {
		eq += per[c]
		uv := 0.0
		if c == "USDT" {
			uv = per[c]
		}
		holdings = append(holdings, &v1.Holding{Ccy: c, Qty: per[c], UsdtValue: uv})
	}
	ccy := "USDT"
	if len(ccys) == 1 {
		ccy = ccys[0]
	} else if len(ccys) > 1 {
		ccy = "MULTI"
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: ccy,
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// gateDualOrders gate 双币在仓单读：GET /earn/dual/orders（SG 10-15 实拍 200 空集）。
// 字段=gateapi 文档 DualGetOrders：status INIT/SETTLEMENT_PROCESSING=在仓，
// SETTLEMENT_SUCCESS/CANCELED/FAILED 终态剔除；本金=invest_currency×invest_amount。
// 状态空串 fail-closed（禁线：空状态不过滤）。
func (q *CEXAccountQuerier) gateDualOrders(ctx context.Context, add func(string, float64)) error {
	body, err := q.gateSignedGet(ctx, "/earn/dual/orders")
	if err != nil {
		return fmt.Errorf("gate dual orders: %w", err)
	}
	var rows []struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Ccy    string `json:"invest_currency"`
		Amt    string `json:"invest_amount"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return fmt.Errorf("gate dual orders json: %w", err)
	}
	for _, r := range rows {
		if r.Status == "" {
			return fmt.Errorf("gate dual order %d 状态字段空", r.ID)
		}
		if !strings.EqualFold(r.Status, "INIT") && !strings.EqualFold(r.Status, "SETTLEMENT_PROCESSING") {
			continue
		}
		v, perr := strconv.ParseFloat(r.Amt, 64)
		if r.Ccy == "" || perr != nil {
			return fmt.Errorf("gate dual order %d ccy/amount 字段缺（%q/%q）", r.ID, r.Ccy, r.Amt)
		}
		if v > 0 {
			add(r.Ccy, v)
		}
	}
	return nil
}

// gateStructuredOrders gate 结构化产品在仓单读：GET /earn/structured/orders
// （SG 10-15 实拍 200 空集）。字段=gateapi 文档 StructuredOrderList：
// status SUCCESS=在仓（DONE/FAILED 终态剔除）；本金=lock_coin×amount。
func (q *CEXAccountQuerier) gateStructuredOrders(ctx context.Context, add func(string, float64)) error {
	body, err := q.gateSignedGet(ctx, "/earn/structured/orders")
	if err != nil {
		return fmt.Errorf("gate structured orders: %w", err)
	}
	var rows []struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Ccy    string `json:"lock_coin"`
		Amt    string `json:"amount"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return fmt.Errorf("gate structured orders json: %w", err)
	}
	for _, r := range rows {
		if r.Status == "" {
			return fmt.Errorf("gate structured order %d 状态字段空", r.ID)
		}
		if !strings.EqualFold(r.Status, "SUCCESS") {
			continue
		}
		v, perr := strconv.ParseFloat(r.Amt, 64)
		if r.Ccy == "" || perr != nil {
			return fmt.Errorf("gate structured order %d ccy/amount 字段缺（%q/%q）", r.ID, r.Ccy, r.Amt)
		}
		if v > 0 {
			add(r.Ccy, v)
		}
	}
	return nil
}
