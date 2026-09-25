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
	avail := jsonNum(acc["availableBalance"])
	if avail < 0 {
		return fmt.Errorf("binance margin precheck %s: availableBalance invalid", o.Symbol)
	}
	positions, _ := acc["positions"].([]any)

	// B-002：hedge 模式 positions[] 返回 LONG/SHORT 多行（杠杆可不同）→ 按当前腿侧匹配
	// （BUY→LONG、SELL→SHORT，与 placeBinance 开仓推导同式）；one-way 单行 BOTH 直取；
	// 同侧无匹配 → error 不猜。
	dual, probed := e.binanceDualSide(ctx)
	wantSide := "BOTH"
	if probed && dual {
		wantSide = "LONG"
		if o.Side == "SELL" {
			wantSide = "SHORT"
		}
	}
	lev := 0.0
	isolated := false
	found := false
	for _, pi := range positions {
		pm, ok := pi.(map[string]any)
		if !ok {
			continue
		}
		if pm["symbol"] != o.Symbol {
			continue
		}
		ps, _ := pm["positionSide"].(string)
		if wantSide != "BOTH" && ps != wantSide {
			continue
		}
		// hedge 精确侧命中；one-way/未探测=BOTH 行
		lev = jsonNum(pm["leverage"])
		iso, _ := pm["isolated"].(bool)
		isolated = iso
		found = true
		if wantSide != "BOTH" || ps == "BOTH" {
			break
		}
	}
	if !found || lev <= 0 {
		return fmt.Errorf("binance margin precheck %s: positionSide=%s 无匹配杠杆行（lev=%.0f found=%v，不猜）",
			o.Symbol, wantSide, lev, found)
	}

	mark, err := e.binanceMarkPrice(ctx, o.Symbol)
	if err != nil {
		return fmt.Errorf("binance margin precheck %s: %w", o.Symbol, err)
	}
	required := o.Qty * mark / lev
	if required > avail*(1-e.marginReservePct) {
		return fmt.Errorf("binance margin insufficient: need %.2f > avail %.2f × %.2f (lev %.0f mark %.4g)",
			required, avail, 1-e.marginReservePct, lev, mark)
	}
	return e.liqDistanceGuard(ctx, o, liqInput{venue: "binance", isolated: isolated, leverage: lev, mark: mark, equity: jsonNum(acc["totalMarginBalance"])})
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
// signed GET /futures/usdt/accounts → available/total；杠杆两级（B-003）：账户级
// leverage 字段 → venue 级缺省档（gateDefaultLeverage，缺省 5），不得 error——
// 首仓必拒会让防线对未持仓组合永久失效（组合 13/14 实证尚未持仓）。
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
		mmr := jsonNum(b0["mmr"])
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
