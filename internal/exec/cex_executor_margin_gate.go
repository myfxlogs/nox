// cex_executor_margin_gate.go —— D-479 S1：自 cex_executor_margin.go 按 venue 缝整段搬移（纯搬移零逻辑变更）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (e *CEXExecutor) gateMarginPrecheck(ctx context.Context, o Order) error {
	endpoint := "/futures/usdt/accounts"
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, headers)
	if err != nil {
		return fmt.Errorf("gate margin precheck %s: %w", o.Symbol, err)
	}
	var acc struct {
		Available string  `json:"available"`
		Total     string  `json:"total"`
		Leverage  float64 `json:"leverage"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return fmt.Errorf("gate margin precheck %s json: %w", o.Symbol, err)
	}
	avail, _ := strconv.ParseFloat(acc.Available, 64)
	equity, _ := strconv.ParseFloat(acc.Total, 64)
	lev := acc.Leverage
	if lev <= 0 {
		lev = e.gateDefaultLeverage // 两级第二档：venue 级 env 缺省（不得 error，B-003）
	}
	if lev <= 0 || avail < 0 {
		return fmt.Errorf("gate margin precheck %s: lev=%.2g avail=%.2g invalid", o.Symbol, lev, avail)
	}

	mark, err := e.gateMarkPrice(ctx, o.Symbol)
	if err != nil {
		return fmt.Errorf("gate margin precheck %s: %w", o.Symbol, err)
	}
	required := o.Qty * mark / lev
	if required > avail*(1-e.marginReservePct) {
		return fmt.Errorf("gate margin insufficient: need %.2f > avail %.2f × %.2f (lev %.0f mark %.4g)",
			required, avail, 1-e.marginReservePct, lev, mark)
	}
	return e.liqDistanceGuard(ctx, o, liqInput{venue: "gate", mark: mark, equity: equity})
}

// gateMarkPrice D-472 S2：合约规格 GET 的 mark_price（签名 GET 同 gateContractSize 端点）。
func (e *CEXExecutor) gateMarkPrice(ctx context.Context, contract string) (float64, error) {
	endpoint := "/futures/usdt/contracts/" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, headers)
	if err != nil {
		return 0, fmt.Errorf("contracts: %w", err)
	}
	var c struct {
		MarkPrice string `json:"mark_price"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return 0, fmt.Errorf("contracts json: %w", err)
	}
	mark, err := strconv.ParseFloat(c.MarkPrice, 64)
	if err != nil || mark <= 0 {
		return 0, fmt.Errorf("contracts %s: mark_price=%q invalid", contract, c.MarkPrice)
	}
	return mark, nil
}

// liqInput D-472 S4：强平距离闸输入（venue 分支各自实拉）。
