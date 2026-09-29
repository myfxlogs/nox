// cex_executor_leverage.go —— D-565 S2：venue 杠杆实读出口（dashboard allocRisk/
// dossier 用，消灭 perp/unified 恒 1/5 硬编）。
// gate=D-564 S1 get_leverage 实况（dual 族按腿侧 dual_side 双 margin 取 min）；
// binance=signed GET /fapi/v2/account positions[] 按 symbol+positionSide 取 leverage
// （与 binanceMarginPrecheck 同表同式）；okx=GET /api/v5/account/leverage-info
// ?instId&mgnMode=cross 取腿侧 posSide 的 lever。
// 全链 fail-closed：读败/无匹配行/非正 → error，调用方标「杠杆实况不可得」不猜。
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

// ReadVenueLeverage venue 杠杆实读（sellLeg=true→腿侧=short/SHORT）。
// venueType 非保证金域（spot 等）或 venue 未接入 → error。
func (e *CEXExecutor) ReadVenueLeverage(ctx context.Context, symbol string, sellLeg bool) (float64, error) {
	switch e.venue {
	case "gate":
		mode, err := e.gatePositionModeFetch(ctx)
		if err != nil {
			return 0, fmt.Errorf("gate leverage %s: %w", symbol, err)
		}
		return e.gateEffectiveLeverage(ctx, symbol, mode, sellLeg)
	case "binance":
		acc, err := e.signedBinanceRequest(ctx, "GET", "/v2/account", url.Values{})
		if err != nil {
			return 0, fmt.Errorf("binance leverage %s: %w", symbol, err)
		}
		lev, _, err := e.binanceAccPositionLeverage(ctx, acc, symbol, sellLeg)
		return lev, err
	case "okx":
		return e.okxLeverageInfo(ctx, symbol, sellLeg)
	}
	return 0, fmt.Errorf("venue %s 杠杆实读未接入", e.venue)
}

// binanceAccPositionLeverage v2/account positions[] 腿侧杠杆行匹配（D-565 自
// binanceMarginPrecheck 提取共用——判定同式：hedge=买腿→LONG/卖腿→SHORT，one-way/
// 未探测=BOTH；无匹配行→error 不猜）。
func (e *CEXExecutor) binanceAccPositionLeverage(ctx context.Context, acc map[string]any, symbol string, sellLeg bool) (lev float64, isolated bool, err error) {
	positions, _ := acc["positions"].([]any)
	dual, probed := e.binanceDualSide(ctx)
	wantSide := "BOTH"
	if probed && dual {
		wantSide = "LONG"
		if sellLeg {
			wantSide = "SHORT"
		}
	}
	found := false
	for _, pi := range positions {
		pm, ok := pi.(map[string]any)
		if !ok {
			continue
		}
		if pm["symbol"] != symbol {
			continue
		}
		ps, _ := pm["positionSide"].(string)
		if wantSide != "BOTH" && ps != wantSide {
			continue
		}
		// hedge 精确侧命中；one-way/未探测=BOTH 行
		lv, lerr := jsonNum(pm["leverage"])
		if lerr != nil {
			return 0, false, fmt.Errorf("binance leverage parse: %w", lerr)
		}
		iso, _ := pm["isolated"].(bool)
		lev, isolated, found = lv, iso, true
		if wantSide != "BOTH" || ps == "BOTH" {
			break
		}
	}
	if !found || lev <= 0 {
		return 0, false, fmt.Errorf("binance leverage %s: positionSide=%s 无匹配杠杆行（lev=%.0f found=%v，不猜）", symbol, wantSide, lev, found)
	}
	return lev, isolated, nil
}

// okxLeverageInfo GET /api/v5/account/leverage-info?instId&mgnMode=cross → 腿侧
// posSide 的 lever（long/short 持仓模式=long|short 行；单向=net 行直取）。
func (e *CEXExecutor) okxLeverageInfo(ctx context.Context, symbol string, sellLeg bool) (float64, error) {
	instID := symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	path := "/api/v5/account/leverage-info?instId=" + url.QueryEscape(instID) + "&mgnMode=cross"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return 0, fmt.Errorf("okx leverage-info %s: %w", instID, err)
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			PosSide string `json:"posSide"`
			Lever   string `json:"lever"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("okx leverage-info %s json: %w", instID, err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return 0, fmt.Errorf("okx leverage-info %s: code=%s data_len=%d", instID, resp.Code, len(resp.Data))
	}
	want := "long"
	if sellLeg {
		want = "short"
	}
	var netLever string
	for _, d := range resp.Data {
		switch d.PosSide {
		case want:
			if v, perr := strconv.ParseFloat(d.Lever, 64); perr == nil && v > 0 {
				return v, nil
			}
			return 0, fmt.Errorf("okx leverage-info %s: posSide=%s lever=%q 非正/非数字", instID, want, d.Lever)
		case "net", "":
			netLever = d.Lever
		}
	}
	// 单向持仓模式只有 net 行；posSide 缺省行同径兜底。
	if netLever != "" {
		if v, perr := strconv.ParseFloat(netLever, 64); perr == nil && v > 0 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("okx leverage-info %s: 无 %s/net 杠杆行（不猜）", instID, want)
}
