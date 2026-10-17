// cex_executor_margin_binance.go —— D-479 S1：自 cex_executor_margin.go 按 venue 缝整段搬移（纯搬移零逻辑变更）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

func (e *CEXExecutor) binanceMarginPrecheck(ctx context.Context, o Order) error {
	acc, err := e.signedBinanceRequest(ctx, "GET", "/v2/account", url.Values{})
	if err != nil {
		return fmt.Errorf("binance margin precheck %s: %w", o.Symbol, err)
	}
	avail, aerr := jsonNum(acc["availableBalance"])
	if aerr != nil {
		return fmt.Errorf("binance margin precheck %s availableBalance: %w", o.Symbol, aerr)
	}
	if avail < 0 {
		return fmt.Errorf("binance margin precheck %s: availableBalance invalid", o.Symbol)
	}
	// B-002：hedge 模式 positions[] 返回 LONG/SHORT 多行（杠杆可不同）→ 按当前腿侧匹配
	// （BUY→LONG、SELL→SHORT，与 placeBinance 开仓推导同式）；one-way 单行 BOTH 直取；
	// 同侧无匹配 → error 不猜。D-565：匹配逻辑提取 binanceAccPositionLeverage 共用。
	lev, isolated, err := e.binanceAccPositionLeverage(ctx, acc, o.Symbol, o.Side == "SELL")
	if err != nil {
		return fmt.Errorf("binance margin precheck %s: %w", o.Symbol, err)
	}

	mark, err := e.binanceMarkPrice(ctx, o.Symbol)
	if err != nil {
		return fmt.Errorf("binance margin precheck %s: %w", o.Symbol, err)
	}
	required := o.Qty * mark / lev
	if required > avail*(1-e.marginReservePct) {
		// D-549 通用化（binance 补齐）：UM futures 池缺口 → earn→spot→futures
		// 装配后重读 /v2/account 复判；装配败=fail-closed 如实拒因（禁放大）。
		need := required - avail*(1-e.marginReservePct)
		if _, aerr := e.binanceAssembleMargin(ctx, need); aerr != nil {
			return fmt.Errorf("binance margin insufficient（装配败: %v）: need %.2f > avail %.2f × %.2f (lev %.0f mark %.4g)",
				aerr, required, avail, 1-e.marginReservePct, lev, mark)
		}
		acc, err = e.signedBinanceRequest(ctx, "GET", "/v2/account", url.Values{})
		if err != nil {
			return fmt.Errorf("binance margin 装配后重读 %s: %w", o.Symbol, err)
		}
		if avail, aerr = jsonNum(acc["availableBalance"]); aerr != nil {
			return fmt.Errorf("binance margin 装配后 availableBalance: %w", aerr)
		}
		if required > avail*(1-e.marginReservePct) {
			return fmt.Errorf("binance margin insufficient（装配后仍不足）: need %.2f > avail %.2f × %.2f (lev %.0f mark %.4g)",
				required, avail, 1-e.marginReservePct, lev, mark)
		}
	}
	eq, eqerr := jsonNum(acc["totalMarginBalance"])
	if eqerr != nil {
		return fmt.Errorf("binance totalMarginBalance: %w", eqerr)
	}
	return e.liqDistanceGuard(ctx, o, liqInput{venue: "binance", isolated: isolated, leverage: lev, mark: mark, equity: eq})
}

// binanceMarkPrice D-472 S1：公开 premiumIndex 标记价（baseURL 已含 /fapi 段，免签名路径）。
func (e *CEXExecutor) binanceMarkPrice(ctx context.Context, symbol string) (float64, error) {
	body, err := e.get(ctx, e.baseURL+"/v1/premiumIndex?symbol="+url.QueryEscape(symbol), nil)
	if err != nil {
		return 0, fmt.Errorf("premiumIndex: %w", err)
	}
	var mp struct {
		MarkPrice string `json:"markPrice"`
	}
	if err := json.Unmarshal(body, &mp); err != nil {
		return 0, fmt.Errorf("premiumIndex json: %w", err)
	}
	mark, err := strconv.ParseFloat(mp.MarkPrice, 64)
	if err != nil || mark <= 0 {
		return 0, fmt.Errorf("premiumIndex %s: markPrice=%q invalid", symbol, mp.MarkPrice)
	}
	return mark, nil
}

// gateMarginPrecheck D-472 S2：gate perp 资金预检（原空实现返 nil 补实）。
// signed GET /futures/usdt/accounts → available/total；杠杆=venue 实况
// get_leverage 实读（D-564 S1 纯读化——配置/SET 全链退役）。
// requiredIM = o.Qty(base) × mark / leverage（quanto 已含在 price 定义内，禁二次乘）。
func (e *CEXExecutor) binanceMMRTier1(ctx context.Context, symbol string) (float64, error) {
	e.mmMu.Lock()
	if mmr, ok := e.mmCache[symbol]; ok {
		e.mmMu.Unlock()
		return mmr, nil
	}
	e.mmMu.Unlock()
	rows, err := e.signedBinanceRequestList(ctx, "GET", "/v1/leverageBracket", url.Values{"symbol": {symbol}})
	if err != nil {
		return 0, fmt.Errorf("leverageBracket: %w", err)
	}
	for _, r := range rows {
		if r["symbol"] != symbol {
			continue
		}
		brs, _ := r["brackets"].([]any)
		if len(brs) == 0 {
			break
		}
		b0, _ := brs[0].(map[string]any)
		mmr, merr := jsonNum(b0["mmr"])
		if merr != nil {
			return 0, fmt.Errorf("binance leverageBracket mmr: %w", merr)
		}
		if mmr <= 0 {
			break
		}
		e.mmMu.Lock()
		if e.mmCache == nil {
			e.mmCache = map[string]float64{}
		}
		e.mmCache[symbol] = mmr
		e.mmMu.Unlock()
		return mmr, nil
	}
	return 0, fmt.Errorf("leverageBracket %s: tier-1 mmr 缺失（fail-closed 不猜）", symbol)
}
