// cex_executor_leverage_set.go —— D-079：perp 腿开仓前置杠杆策略化（推不设读前置写）。
// 开仓 MarginPrecheck 前把 perp 腿杠杆设到配置目标（|cur−target|<0.5 → 零 POST）：
//
//	okx     POST /api/v5/account/set-leverage {"instId","lever","mgnMode":"cross"}
//	        （cross 下无 posSide——10-05 业主实测 long/short 两侧同变；
//	        https://www.okx.com/docs-v5/en/#trading-account-rest-api-set-the-leverage）
//	binance signed POST /fapi/v1/leverage symbol=&leverage=
//	        （https://developers.binance.com/docs/derivatives/usds-margined-futures/account/rest-api/Change-Initial-Leverage）
//	gate    POST /futures/usdt/positions/{contract}/set_leverage
//	        ?leverage=&margin_mode={isolated|cross}&dual_side={dual_long|dual_short}
//	        （官方「指定模式设杠杆」新面，与读口 get_leverage?pos_margin_mode&dual_side
//	        一一同域对齐；旧 /leverage 端点带「连带切保证金模式」副作用=D-564 头注
//	        明文永久禁用，不用；https://www.gate.com/docs/developers/apiv4/en/#update-leverage-for-specified-mode）
//
// 设后 ReadVenueLeverage 复读（venue 有读口必须复读核实），仍≠target → error。
// 全链 err 上抛不吞；simulated → nil（纸面不触 venue）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"nox/internal/platform/signutil"
)

// EnsureVenueLeverage perp 腿杠杆策略化（开仓前置，pipeline leverageSetter 缝）。
// symbol=venue 原生符号（okx 缺 -SWAP 后缀时补齐，同 okxLeverageInfo 规范化）。
func (e *CEXExecutor) EnsureVenueLeverage(ctx context.Context, symbol string, sellLeg bool, target float64) error {
	if e.simulated {
		return nil
	}
	if target <= 0 {
		return nil // 未配置=停用（调用方已闸，防御兜底）
	}
	cur, err := e.ReadVenueLeverage(ctx, symbol, sellLeg)
	if err != nil {
		return fmt.Errorf("%s %s 杠杆实读: %w", e.venue, symbol, err)
	}
	if math.Abs(cur-target) < 0.5 {
		return nil
	}
	levStr := strconv.FormatFloat(target, 'f', -1, 64)
	switch e.venue {
	case "okx":
		err = e.okxSetLeverage(ctx, symbol, levStr)
	case "binance":
		err = e.binanceSetLeverage(ctx, symbol, levStr)
	case "gate":
		err = e.gateSetLeverage(ctx, symbol, sellLeg, levStr)
	default:
		err = fmt.Errorf("venue %s 杠杆设定未接入", e.venue)
	}
	if err != nil {
		return err
	}
	log.Printf("[exec] %s %s leverage %.0f→%.0f", e.venue, symbol, cur, target)
	now, err := e.ReadVenueLeverage(ctx, symbol, sellLeg)
	if err != nil {
		return fmt.Errorf("venue %s %s 杠杆设定后复读失败: %w", e.venue, symbol, err)
	}
	if math.Abs(now-target) >= 0.5 {
		return fmt.Errorf("venue %s %s 杠杆设定后复读 %.0f≠目标 %.0f（fail-closed）", e.venue, symbol, now, target)
	}
	return nil
}

// okxSetLeverage POST /api/v5/account/set-leverage（cross 两侧同设，无 posSide）。
func (e *CEXExecutor) okxSetLeverage(ctx context.Context, symbol, lever string) error {
	instID := symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	bodyStr, _ := json.Marshal(map[string]string{"instId": instID, "lever": lever, "mgnMode": "cross"})
	path := "/api/v5/account/set-leverage"
	ts := e.okxNowISO()
	resp, err := e.post(ctx, e.baseURL+path, string(bodyStr), http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"POST"+path+string(bodyStr))},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
		"Content-Type":         {"application/json"},
	})
	if err != nil {
		return fmt.Errorf("okx set-leverage %s: %w", instID, err)
	}
	var r struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(resp, &r); err != nil || r.Code != "0" {
		return fmt.Errorf("okx set-leverage %s resp=%s", instID, resp)
	}
	return nil
}

// binanceSetLeverage signed POST /fapi/v1/leverage（symbol 级设定，hedge 模式两腿侧共用）。
func (e *CEXExecutor) binanceSetLeverage(ctx context.Context, symbol, lever string) error {
	_, err := e.signedBinanceRequest(ctx, "POST", "/v1/leverage", url.Values{
		"symbol":   {symbol},
		"leverage": {lever},
	})
	if err != nil {
		return fmt.Errorf("binance set leverage %s: %w", symbol, err)
	}
	return nil
}

// gateSetLeverage set_leverage 指定模式面：读口径=dual 腿侧 min(iso,cross)/single 读 iso——
// iso+cross 两域都设到 target 才与读法对齐（cross 无 dual_side，官方示例合约级共用）。
// 不用 gateSignedPost（X-Gate-Exptime 下单专用头）；签名=GateSign(POST,urlPath,query,"",ts)。
func (e *CEXExecutor) gateSetLeverage(ctx context.Context, contract string, sellLeg bool, lever string) error {
	mode, err := e.gatePositionModeFetch(ctx)
	if err != nil {
		return fmt.Errorf("gate set_leverage %s mode: %w", contract, err)
	}
	endpoint := "/futures/usdt/positions/" + contract + "/set_leverage"
	urlPath := "/api/v4" + endpoint
	post := func(q url.Values) error {
		qs := q.Encode()
		ts := gateNow()
		sign := signutil.GateSign(e.apiSecret, "POST", urlPath, qs, "", ts)
		_, err := e.post(ctx, e.baseURL+endpoint+"?"+qs, "", http.Header{
			"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
		})
		return err
	}
	isoQ := url.Values{"leverage": {lever}, "margin_mode": {"isolated"}}
	if mode.isDual() {
		side := "dual_long"
		if sellLeg {
			side = "dual_short"
		}
		isoQ.Set("dual_side", side)
	}
	if err := post(isoQ); err != nil {
		return fmt.Errorf("gate set_leverage %s isolated: %w", contract, err)
	}
	if err := post(url.Values{"leverage": {lever}, "margin_mode": {"cross"}}); err != nil {
		return fmt.Errorf("gate set_leverage %s cross: %w", contract, err)
	}
	return nil
}
