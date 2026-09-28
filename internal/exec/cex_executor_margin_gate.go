// cex_executor_margin_gate.go —— D-479 S1：自 cex_executor_margin.go 按 venue 缝整段搬移（纯搬移零逻辑变更）。
package exec

import (
	"arb/internal/signutil"
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
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
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
		Available string `json:"available"`
		Total     string `json:"total"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return fmt.Errorf("gate margin precheck %s json: %w", o.Symbol, err)
	}
	avail, err := f64z(acc.Available)
	if err != nil {
		return fmt.Errorf("gate margin precheck %s avail=%q: %w", o.Symbol, acc.Available, err)
	}
	equity, err := f64z(acc.Total)
	if err != nil {
		return fmt.Errorf("gate margin precheck %s equity=%q: %w", o.Symbol, acc.Total, err)
	}
	// D-563 S1：accounts 响应无 leverage 字段（实探为空）——phantom 杠杆静默兜底已删；
	// 杠杆=venue 级目标配置（开仓前系统显式 SET，配置=推送条款）。缺失/非正 fail-closed。
	lev := e.gateDefaultLeverage
	if lev <= 0 || avail < 0 {
		return fmt.Errorf("gate margin precheck %s: gate 目标杠杆未配置（ARB_GATE_DEFAULT_LEVERAGE）fail-closed 拒开（lev=%.2g avail=%.2g）", o.Symbol, lev, avail)
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
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
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
	mark, err := f64z(c.MarkPrice)
	if err != nil || mark <= 0 {
		return 0, fmt.Errorf("contracts %s: mark_price=%q invalid", contract, c.MarkPrice)
	}
	return mark, nil
}

// liqInput D-472 S4：强平距离闸输入（venue 分支各自实拉）。
