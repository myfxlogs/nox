// cex_executor_margin_gate.go —— D-479 S1：自 cex_executor_margin.go 按 venue 缝整段搬移（纯搬移零逻辑变更）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"nox/internal/platform/signutil"
	"strconv"
)

func (e *CEXExecutor) gateMarginPrecheck(ctx context.Context, o Order) error {
	endpoint := "/futures/usdt/accounts"
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
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
	// 杠杆纯读化——venue 实况 get_leverage 实读（dual 族双 margin 取 min=
	// 保证金需求上界保守；读不到 fail-closed 拒开，不兜底不配置冒充）。配置链已退役。
	mode, err := e.gatePositionModeFetch(ctx)
	if err != nil {
		return fmt.Errorf("gate margin precheck %s: %w", o.Symbol, err)
	}
	lev, err := e.gateEffectiveLeverage(ctx, o.Symbol, mode, o.Side == "SELL")
	if err != nil {
		return fmt.Errorf("gate margin precheck %s: gate 杠杆实况不可得（fail-closed 不猜）: %w", o.Symbol, err)
	}

	mark, err := e.gateMarkPrice(ctx, o.Symbol)
	if err != nil {
		return fmt.Errorf("gate margin precheck %s: %w", o.Symbol, err)
	}
	required := o.Qty * mark / lev
	if required > avail*(1-e.marginReservePct) {
		// D-549 通用化：futures 池缺口 → spot→futures 装配后重读 accounts 复判；
		// 装配败=fail-closed 如实拒因（min(缺口,spot可用) 禁放大禁预腾）。
		need := required - avail*(1-e.marginReservePct)
		if _, aerr := e.gateAssembleMargin(ctx, need); aerr != nil {
			return fmt.Errorf("gate margin insufficient（装配败: %v）: need %.2f > avail %.2f × %.2f (lev %.0f mark %.4g)",
				aerr, required, avail, 1-e.marginReservePct, lev, mark)
		}
		if aerr := e.gateFuturesAvail(ctx, &avail, &equity); aerr != nil {
			return fmt.Errorf("gate margin 装配后重读 %s: %w", o.Symbol, aerr)
		}
		if required > avail*(1-e.marginReservePct) {
			return fmt.Errorf("gate margin insufficient（装配后仍不足）: need %.2f > avail %.2f × %.2f (lev %.0f mark %.4g)",
				required, avail, 1-e.marginReservePct, lev, mark)
		}
	}
	return e.liqDistanceGuard(ctx, o, liqInput{venue: "gate", mark: mark, equity: equity})
}

// gateMarkPrice D-472 S2：合约规格 GET 的 mark_price（签名 GET 同 gateContractSize 端点）。
func (e *CEXExecutor) gateMarkPrice(ctx context.Context, contract string) (float64, error) {
	endpoint := "/futures/usdt/contracts/" + contract
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
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

// gateFuturesAvail futures 账户 available/total 重读（装配后复判用）——指针回写。
func (e *CEXExecutor) gateFuturesAvail(ctx context.Context, avail, equity *float64) error {
	endpoint := "/futures/usdt/accounts"
	ts := gateNow()
	h := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {signutil.GateSign(e.apiSecret, "GET", "/api/v4"+endpoint, "", "", ts)},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, h)
	if err != nil {
		return err
	}
	var acc struct {
		Available string `json:"available"`
		Total     string `json:"total"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return err
	}
	a, err := f64z(acc.Available)
	if err != nil {
		return err
	}
	tot, err := f64z(acc.Total)
	if err != nil {
		return err
	}
	*avail, *equity = a, tot
	return nil
}
