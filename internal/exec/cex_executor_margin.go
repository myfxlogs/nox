package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SetMarginReservePct 注入资金预检安全垫（cfg.MarginReservePct；不设=缺省 0.20）。
func (e *CEXExecutor) SetMarginReservePct(pct float64) { e.marginReservePct = pct }

// SetLiqGuards D-472 S4：注入强平距离预检参数（cfg 注入）。
// minLiqDistPct=isolated 距离下限（ARB_MIN_LIQ_DISTANCE_PCT，缺省 0=显式关闸——
// 开启前置=回放定标报告，复审方标定后才允许非零值上线）；crossNotionalEquityCap=
// cross 名义/权益占比上限（ARB_CROSS_NOTIONAL_EQUITY_CAP，缺省 0.5；≤0=cross 支路关）。
func (e *CEXExecutor) SetLiqGuards(minLiqDistPct, crossNotionalEquityCap float64) {
	e.minLiqDistPct = minLiqDistPct
	e.crossNotionalEquityCap = crossNotionalEquityCap
}

// SetGateDefaultLeverage D-472 S2：注入 gate venue 级缺省杠杆档
// （ARB_GATE_DEFAULT_LEVERAGE，缺省 5——账户级杠杆取不到时的第二级，不得 error）。
func (e *CEXExecutor) SetGateDefaultLeverage(lev float64) { e.gateDefaultLeverage = lev }

// MarginPrecheck D-415 §1.3 + D-472 S1/S2/S4：开仓前逐腿资金预检。
// 量纲锚（B-001 钉死）：exec.Order.Qty=基础币数量（pipeline orderQtyFor ×cs 换算后），
// requiredIM=o.Qty×mark/leverage——禁二次乘 ct_val/quanto（binance ct_val≡1、gate quanto
// 已含在 price 定义内、okx 现路径 max-size 服务端已算）。
// 范围：okx/binance/gate 的 perp 分支补实；其余 venue/type 返 nil（MT5 有独立实现）。
// 任一调用失败 → 返 err 拒开（fail-closed，同 okx 先例）。
//
// D-472 S4 MMR/杠杆实测定标口径（禁「保守值」模糊表达，B-006）：
//   - okx XPT/XPD tier-1（cross）：mmr=1.0%、imr=2.0%、maxLever=50
//     （来源=/api/v5/public/position-tiers?instType=SWAP&uly=XPT-USDT&tdMode=cross，2026-09-25 实拉；
//     okx 下单 tdMode 硬编码 cross → 恒走占比闸，该常量仅文档用不参与判定）；
//   - binance：isolated 分支 MMR 运行时取 /fapi/v1/leverageBracket（签名，per-symbol 缓存），
//     取不到=error 不猜——2026-09-25 生产密钥实测 -2015 Invalid API-key/IP/permissions
//     （证据在案），禁硬编码防保守值取错方向=静默失效；
//   - gate：全账户 cross 强平模型（contract payload 实测无 mmr 字段，2026-09-25），
//     本票 gate 恒走占比闸，MMR 不适用。
func (e *CEXExecutor) MarginPrecheck(ctx context.Context, o Order) error {
	switch {
	case e.venue == "okx" && e.venueType == "perp":
		if err := e.okxMarginPrecheck(ctx, o); err != nil {
			return err
		}
		// okx tdMode 硬编码 cross（cex_executor.go placeOkx）→ S4 占比闸。
		// 关闸（缺省）=零额外调用（mark/balance 两请求仅在闸开启时发生）。
		if e.minLiqDistPct <= 0 {
			return nil
		}
		mark, eq, err := e.okxMarkAndEquity(ctx, o.Symbol)
		if err != nil {
			return fmt.Errorf("okx liq guard %s: %w", o.Symbol, err)
		}
		return e.liqDistanceGuard(ctx, o, liqInput{venue: "okx", mark: mark, equity: eq})
	case e.venue == "binance" && e.venueType == "perp":
		return e.binanceMarginPrecheck(ctx, o)
	case e.venue == "gate" && e.venueType == "perp":
		return e.gateMarginPrecheck(ctx, o)
	case e.venueType == "spot":
		// D-473 S2：现货腿全额现金占用预检（carry 场景 BUY 腿）；SELL 腿内部放行。
		return e.spotCashPrecheck(ctx, o)
	}
	return nil
}

// okxMarginPrecheck D-415 原路径（提取自 MarginPrecheck，零语义变更）：
// GET /api/v5/account/max-size?instId&tdMode=cross 服务端已含余额+杠杆+持仓，
// szContracts ≤ avail×(1−reservePct) 才放行。
func (e *CEXExecutor) okxMarginPrecheck(ctx context.Context, o Order) error {
	instID := o.Symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	ctVal, _, err := e.okxContractVal(ctx, instID)
	if err != nil {
		return fmt.Errorf("okx margin precheck %s: %w", instID, err)
	}
	szContracts := o.Qty / ctVal

	path := "/api/v5/account/max-size?instId=" + url.QueryEscape(instID) + "&tdMode=cross"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return fmt.Errorf("okx max-size %s: %w", instID, err)
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			MaxBuy  string `json:"maxBuy"`
			MaxSell string `json:"maxSell"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("okx max-size %s json: %w", instID, err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return fmt.Errorf("okx max-size %s: code=%s data_len=%d", instID, resp.Code, len(resp.Data))
	}
	availStr := resp.Data[0].MaxBuy
	if o.Side == "SELL" {
		availStr = resp.Data[0].MaxSell
	}
	avail, err := strconv.ParseFloat(availStr, 64)
	if err != nil || avail < 0 {
		return fmt.Errorf("okx max-size %s: avail=%q invalid", instID, availStr)
	}
	if szContracts > avail*(1-e.marginReservePct) {
		return fmt.Errorf("okx margin insufficient: sz %.4f 张 > avail %.4f 张 × %.2f (%s)",
			szContracts, avail, 1-e.marginReservePct, instID)
	}
	return nil
}

// okxMarkAndEquity D-472 S4：okx 标记价（公开 mark-price）+ 账户权益（signed balance totalEq）。
func (e *CEXExecutor) okxMarkAndEquity(ctx context.Context, symbol string) (mark, equity float64, err error) {
	instID := symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	body, err := e.get(ctx, e.baseURL+"/api/v5/public/mark-price?instId="+url.QueryEscape(instID), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("mark-price: %w", err)
	}
	var mp struct {
		Data []struct {
			MarkPx string `json:"markPx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &mp); err != nil || len(mp.Data) == 0 {
		return 0, 0, fmt.Errorf("mark-price %s: len=%d err=%v", instID, len(mp.Data), err)
	}
	mark, err = strconv.ParseFloat(mp.Data[0].MarkPx, 64)
	if err != nil || mark <= 0 {
		return 0, 0, fmt.Errorf("mark-price %s: markPx=%q invalid", instID, mp.Data[0].MarkPx)
	}

	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	path := "/api/v5/account/balance"
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err = e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return 0, 0, fmt.Errorf("balance: %w", err)
	}
	var bal struct {
		Code string `json:"code"`
		Data []struct {
			TotalEq string `json:"totalEq"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &bal); err != nil {
		return 0, 0, fmt.Errorf("balance json: %w", err)
	}
	if bal.Code != "0" || len(bal.Data) == 0 {
		return 0, 0, fmt.Errorf("balance: code=%s data_len=%d", bal.Code, len(bal.Data))
	}
	equity, err = strconv.ParseFloat(bal.Data[0].TotalEq, 64)
	if err != nil || equity < 0 {
		return 0, 0, fmt.Errorf("balance: totalEq=%q invalid", bal.Data[0].TotalEq)
	}
	return mark, equity, nil
}

// binanceMarginPrecheck D-472 S1：binance perp 资金预检（原空实现返 nil 补实）。
// signed GET /v2/account（baseURL 已含 /fapi 段，同 placeBinance /v1/order 惯例）→ availableBalance + positions[]（per-symbol leverage/
// isolated/positionSide）；mark 用公开 /fapi/v1/premiumIndex（baseURL 免签名路径）。
// requiredIM = o.Qty(base) × mark / leverage（B-001 禁二次乘 ct_val——binance ct_val≡1）。
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
type liqInput struct {
	venue    string
	isolated bool    // binance positions[].isolated 实值；okx/gate 恒 cross
	leverage float64 // isolated 分支用（binance 腿级实值）
	mark     float64
	equity   float64 // cross 占比闸分母（binance totalMarginBalance / gate total / okx totalEq）
}

// liqDistanceGuard D-472 S4：强平距离预检（现闸只答「能不能开」，本闸答「开完离爆多远」）。
// 缺省 ARB_MIN_LIQ_DISTANCE_PCT=0=显式关闸（开启前置=5 组合回放定标报告，复审方标定后
// 才允许非零值上线）。模式分流（B-006）：
//   - isolated：estLiqDist=(1/leverage_eff)−mmr_est ≥ minPct；MMR 运行时取 leverageBracket
//     （fail-closed，不硬编码——binance -2015 实测在案）；
//   - cross：禁按腿杠杆估（语义=全账扛，按腿算=系统性高估恒放行）→ 名义/权益占比闸
//     notional/equity ≤ CrossNotionalEquityCap；equity 非正=fail-closed。
//
// 拒文含 liq-distance 子串 → gate 层 S3 节流告警同规则捕获。
func (e *CEXExecutor) liqDistanceGuard(ctx context.Context, o Order, in liqInput) error {
	if e.minLiqDistPct <= 0 {
		return nil // 显式关闸（缺省）
	}
	notional := o.Qty * in.mark
	if in.isolated {
		mmr, err := e.binanceMMRTier1(ctx, o.Symbol)
		if err != nil {
			return fmt.Errorf("%s liq guard %s: %w", in.venue, o.Symbol, err)
		}
		est := 1/in.leverage - mmr
		if est < e.minLiqDistPct {
			return fmt.Errorf("%s liq-distance est=%.4f < floor=%.4f (isolated lev=%.0f mmr=%.4f notional=%.2f)",
				in.venue, est, e.minLiqDistPct, in.leverage, mmr, notional)
		}
		return nil
	}
	if e.crossNotionalEquityCap <= 0 {
		return nil // cross 支路显式关（cap≤0）
	}
	if in.equity <= 0 {
		return fmt.Errorf("%s liq guard %s: equity=%.4g 非正（cross 占比闸 fail-closed）", in.venue, o.Symbol, in.equity)
	}
	if ratio := notional / in.equity; ratio > e.crossNotionalEquityCap {
		return fmt.Errorf("%s liq-distance cross notional/equity=%.3f > cap=%.3f (notional=%.2f equity=%.2f，占比闸禁腿杠杆估)",
			in.venue, ratio, e.crossNotionalEquityCap, notional, in.equity)
	}
	return nil
}

// binanceMMRTier1 D-472 S4：binance perp tier-1 维持保证金率（signed
// /fapi/v1/leverageBracket，per-symbol 缓存）。运行时取值 fail-closed——不硬编码常量
// （2026-09-25 生产密钥实测 -2015 Invalid API-key/IP/permissions，取错方向=静默失效）。
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

// jsonNum JSON number/string → float64（binance 响应数值字段 string/number 混形）。
func jsonNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
