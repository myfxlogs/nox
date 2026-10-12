// cex_executor_gate_leverage.go —— D-564 S1：gate 杠杆纯读化（取代 D-563 推不设读）。
// 官方读口=GET /futures/usdt/get_leverage/{contract}?pos_margin_mode={isolated|cross}
// &dual_side={dual_long|dual_short} → FuturesLeverage{Lever}（本账户 dual 实测缺
// pos_margin_mode 即 400——按必填处理，server-facts-over-docs）。
// 禁令边界：旧 push 端点（positions|dual_comp + /leverage）带「设杠杆连带切保证金模式」
// 副作用，永久禁用；配置冒充事实退役——venue 实况读为准，读不到 fail-closed。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"nox/internal/platform/signutil"
)

// gatePositionMode accounts 响应模式探测值（U5：position_mode 权威三值，
// 旧 in_dual_mode 布尔区分不了 dual_plus——退位）。
type gatePositionMode string

const (
	gateModeSingle   gatePositionMode = "single"
	gateModeDual     gatePositionMode = "dual"
	gateModeDualPlus gatePositionMode = "dual_plus"
)

// isDual 双仓族（dual/dual_plus 共用 dual_comp 面）。
func (m gatePositionMode) isDual() bool {
	return m == gateModeDual || m == gateModeDualPlus
}

// gateAccountsGet GET /futures/usdt/accounts（签名面复用；S2/S3 模式感知路由同源）。
func (e *CEXExecutor) gateAccountsGet(ctx context.Context) ([]byte, error) {
	endpoint := "/futures/usdt/accounts"
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	return e.get(ctx, e.baseURL+endpoint, http.Header{
		"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
	})
}

// gatePositionModeFetch accounts 响应 position_mode 探测（U5：三值权威字段，
// 旧 in_dual_mode 布尔区分不了 dual_plus——退位）。禁缓存——业主随时可能在 app
// 切模式，缓存=快照进决策违禁，每次实读。
func (e *CEXExecutor) gatePositionModeFetch(ctx context.Context) (gatePositionMode, error) {
	body, err := e.gateAccountsGet(ctx)
	if err != nil {
		return "", err
	}
	var acc struct {
		PositionMode string `json:"position_mode"`
		InDualMode   bool   `json:"in_dual_mode"` // 旧字段兜底（老响应三值缺省时）
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return "", fmt.Errorf("gate position_mode json: %w", err)
	}
	switch gatePositionMode(acc.PositionMode) {
	case gateModeSingle, gateModeDual, gateModeDualPlus:
		return gatePositionMode(acc.PositionMode), nil
	}
	if acc.InDualMode {
		return gateModeDual, nil
	}
	return gateModeSingle, nil
}

// gateGetLeverage 单 margin 模式读：GET get_leverage/{contract} → Lever（string 数值）。
// 缺/空/非数字/HTTP 错 → error（调用方 fail-closed）。
func (e *CEXExecutor) gateGetLeverage(ctx context.Context, contract, posMarginMode, dualSide string) (float64, error) {
	query := "pos_margin_mode=" + posMarginMode
	if dualSide != "" {
		query += "&dual_side=" + dualSide
	}
	endpoint := "/futures/usdt/get_leverage/" + contract
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, query, "", ts)
	body, err := e.get(ctx, e.baseURL+endpoint+"?"+query, http.Header{
		"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
	})
	if err != nil {
		return 0, fmt.Errorf("get_leverage %s (%s/%s): %w", contract, posMarginMode, dualSide, err)
	}
	var lv struct {
		Lever string `json:"Lever"` // 文档钉死大写字段（gateapi-go FuturesLeverage）
	}
	if err := json.Unmarshal(body, &lv); err != nil {
		return 0, fmt.Errorf("get_leverage %s 解析: %w", contract, err)
	}
	if lv.Lever == "" {
		return 0, fmt.Errorf("get_leverage %s (%s/%s) Lever 空", contract, posMarginMode, dualSide)
	}
	v, err := strconv.ParseFloat(lv.Lever, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("get_leverage %s (%s/%s) Lever=%q 非正/非数字", contract, posMarginMode, dualSide, lv.Lever)
	}
	return v, nil
}

// gateEffectiveLeverage 杠杆实况（U1 纯读）：dual 族按腿方向 dual_side 双 margin
// （isolated+cross）各读一次取 min（保证金需求上界=保守，永不虚放行）；single 读
// isolated 单次（无 dual_side 参数）。任一读败 → error（调用方 fail-closed）。
func (e *CEXExecutor) gateEffectiveLeverage(ctx context.Context, contract string, mode gatePositionMode, sellLeg bool) (float64, error) {
	dualSide := ""
	if mode.isDual() {
		dualSide = "dual_long"
		if sellLeg {
			dualSide = "dual_short"
		}
		iso, err := e.gateGetLeverage(ctx, contract, "isolated", dualSide)
		if err != nil {
			return 0, err
		}
		cross, err := e.gateGetLeverage(ctx, contract, "cross", dualSide)
		if err != nil {
			return 0, err
		}
		eff := iso
		if cross < eff {
			eff = cross
		}
		e.warnLeverageMax(ctx, contract, eff)
		return eff, nil
	}
	v, err := e.gateGetLeverage(ctx, contract, "isolated", "")
	if err != nil {
		return 0, err
	}
	e.warnLeverageMax(ctx, contract, v)
	return v, nil
}

// warnLeverageMax D-564 S4 U6：venue 实况 lever>leverage_max（矛盾态，理论不该出现）
// → warn 不拦（venue 实况为准）+记日志；spec 读败静默（warn 附属不阻断主判定）。
func (e *CEXExecutor) warnLeverageMax(ctx context.Context, contract string, lever float64) {
	spec, err := e.gateSpecFetch(ctx, contract)
	if err != nil || spec.LeverageMax <= 0 || lever <= spec.LeverageMax {
		return
	}
	log.Printf("[gate] 杠杆矛盾态：get_leverage=%.0f > leverage_max=%.0f（venue 实况为准不拦）%s", lever, spec.LeverageMax, contract)
}
