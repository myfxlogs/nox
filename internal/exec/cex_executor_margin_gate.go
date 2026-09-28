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
		Available string  `json:"available"`
		Total     string  `json:"total"`
		Leverage  float64 `json:"leverage"`
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
	lev := acc.Leverage
	if lev <= 0 {
		// D-562 S1 三禁令①：账户响应无 leverage 字段（Gate 双仓平仓态读端点 405 实证）——
		// 禁静默兜底 env 缺省（phantom lev=5 拦真单实证）。推不设读（practices §实时性三禁#4）：
		// 显式 POST 设定该合约杠杆=配置档（ARB_GATE_DEFAULT_LEVERAGE→我方推送的合约条款，
		// 非兜底值），设定成功用该值；失败=fail-closed 拒开。
		setLev, err := e.gateSetLeverage(ctx, o.Symbol, e.gateDefaultLeverage)
		if err != nil {
			return fmt.Errorf("gate margin precheck %s: 账户杠杆不可读且设定失败（fail-closed 不猜）: %w", o.Symbol, err)
		}
		lev = setLev
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

// gateSetLeverage D-562 S1：推不设读——POST /futures/usdt/positions/{contract}/leverage
// 显式设定合约杠杆（Gate 双仓模式 leverage 双值 JSON；单项模式 integer，双值体两处同值兼容）。
// 返回生效杠杆。配置值来源=ARB_GATE_DEFAULT_LEVERAGE 链（业主显式配置=合约条款，非兜底）。
func (e *CEXExecutor) gateSetLeverage(ctx context.Context, contract string, lev float64) (float64, error) {
	if lev <= 0 {
		return 0, fmt.Errorf("配置杠杆档未设置（ARB_GATE_DEFAULT_LEVERAGE 空）")
	}
	endpoint := "/futures/usdt/positions/" + contract + "/leverage"
	urlPath := "/api/v4" + endpoint
	body := fmt.Sprintf(`{"leverage":%d,"leverage_0":%d,"leverage_1":%d}`, int64(lev), int64(lev), int64(lev))
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "POST", urlPath, "", body, ts)
	headers := http.Header{
		"KEY":          {e.apiKey},
		"Timestamp":    {strconv.FormatInt(ts, 10)},
		"SIGN":         {sign},
		"Accept":       {"application/json"},
		"Content-Type": {"application/json"},
	}
	if _, err := e.post(ctx, e.baseURL+endpoint, body, headers); err != nil {
		return 0, fmt.Errorf("POST leverage %s: %w", contract, err)
	}
	return lev, nil
}
