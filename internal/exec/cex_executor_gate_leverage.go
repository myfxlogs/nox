// cex_executor_gate_leverage.go —— D-563 S1：gate 杠杆推送条款（推不设读）。
// accounts 响应无 leverage 字段（实探）+ 双仓模式无杠杆读回端点 →
// 开仓前显式 POST SET 合约杠杆，响应回显 leverage==target 作「已确认」证据。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"arb/internal/signutil"
)

// gateDualModeFetch D-563 S1：accounts 响应 in_dual_mode 探测（per-executor 缓存；
// 探测败 fail-closed 上抛——模式未知无法选端点，不猜）。
func (e *CEXExecutor) gateDualModeFetch(ctx context.Context) (bool, error) {
	e.gateLevMu.Lock()
	if e.gateDualMode != nil {
		dual := *e.gateDualMode
		e.gateLevMu.Unlock()
		return dual, nil
	}
	e.gateLevMu.Unlock()
	endpoint := "/futures/usdt/accounts"
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	body, err := e.get(ctx, e.baseURL+endpoint, http.Header{
		"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
	})
	if err != nil {
		return false, fmt.Errorf("gate dual-mode 探测: %w", err)
	}
	var acc struct {
		InDualMode bool `json:"in_dual_mode"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return false, fmt.Errorf("gate dual-mode json: %w", err)
	}
	e.gateLevMu.Lock()
	e.gateDualMode = &acc.InDualMode
	e.gateLevMu.Unlock()
	return acc.InDualMode, nil
}

// ensureGateLeverage D-563 S1：开仓前显式 SET 合约杠杆=推送条款（Gate 双仓模式无
// 杠杆读回端点——推不设读；POST 响应回显 leverage==target 即「已确认」证据）。
// set-mark（contract|dual_side 键，gateLevMu 护）防并发重复 SET；SET 失败/回显不符/
// 目标未配置 → fail-closed 拒开不落单。sellLeg=true→dual_short，false→dual_long。
func (e *CEXExecutor) ensureGateLeverage(ctx context.Context, contract string, sellLeg bool) error {
	target := e.gateDefaultLeverage
	if target <= 0 {
		return fmt.Errorf("gate 目标杠杆未配置（ARB_GATE_DEFAULT_LEVERAGE）fail-closed 拒开 %s", contract)
	}
	side := "dual_long"
	if sellLeg {
		side = "dual_short"
	}
	key := contract + "|" + side
	e.gateLevMu.Lock()
	if e.gateLeverageSet[key] {
		e.gateLevMu.Unlock()
		return nil
	}
	e.gateLevMu.Unlock()

	dual, err := e.gateDualModeFetch(ctx)
	if err != nil {
		return err
	}
	var endpoint, query string
	levStr := strconv.FormatFloat(target, 'f', -1, 64)
	if dual {
		endpoint = "/futures/usdt/dual_comp/positions/" + contract + "/leverage"
		query = "leverage=" + levStr + "&dual_side=" + side
	} else {
		endpoint = "/futures/usdt/positions/" + contract + "/leverage"
		query = "leverage=" + levStr
	}
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "POST", urlPath, query, "", ts)
	raw, err := e.post(ctx, e.baseURL+endpoint+"?"+query, "", http.Header{
		"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
	})
	if err != nil {
		return fmt.Errorf("gate leverage SET %s %s: %w（fail-closed 不落单）", contract, side, err)
	}
	// 响应回显核对=「已确认」证据（不回显/不等=未确认 fail-closed）。
	var resp struct {
		Leverage json.Number `json:"leverage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("gate leverage SET %s %s 响应无回显: %w", contract, side, err)
	}
	got, err := resp.Leverage.Float64()
	if err != nil || got != target {
		return fmt.Errorf("gate leverage SET %s %s 回显 %q≠目标 %g（未确认 fail-closed）", contract, side, resp.Leverage, target)
	}
	e.gateLevMu.Lock()
	e.gateLeverageSet[key] = true
	e.gateLevMu.Unlock()
	return nil
}
