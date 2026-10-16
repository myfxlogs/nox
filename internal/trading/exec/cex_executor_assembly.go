// cex_executor_assembly.go —— D-549 装配模式通用化（xasset CEX 腿保证金装配段）：
// 目标池净值不足时按所内可用腾挪序补位——okx: savings 赎回→funding→unified；
// gate: spot→futures 划转。装配额=min(缺口, 可动源) 禁放大禁预腾攒池；
// 读败/腾挪败 fail-closed 如实拒因。仅执行时按需调用，非常驻搬运。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"nox/internal/platform/signutil"
	"strconv"
)

// okxAssembleMargin okx 所内装配：needed USD 缺口 → funding(6) 现值读 →
// 不足先赎 min(needed−funding, savings 可赎) 落 funding → 6→18 划转 min(needed, funding)
// 落 unified（trading）。返回装配金额；0/败=调用方维持 insufficient 拒。
func (e *CEXExecutor) okxAssembleMargin(ctx context.Context, needed float64) (float64, error) {
	if needed <= 0 {
		return 0, nil
	}
	get := func(path string, out any) error {
		ts := e.okxNowISO()
		h := http.Header{
			"OK-ACCESS-KEY":        {e.apiKey},
			"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
			"OK-ACCESS-TIMESTAMP":  {ts},
			"OK-ACCESS-PASSPHRASE": {e.passphrase},
		}
		b, err := e.get(ctx, e.baseURL+path, h)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
	post := func(path, body string) error {
		ts := e.okxNowISO()
		h := http.Header{
			"OK-ACCESS-KEY":        {e.apiKey},
			"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"POST"+path+body)},
			"OK-ACCESS-TIMESTAMP":  {ts},
			"OK-ACCESS-PASSPHRASE": {e.passphrase},
			"Content-Type":         {"application/json"},
		}
		b, err := e.post(ctx, e.baseURL+path, body, h)
		if err != nil {
			return err
		}
		var r struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		if jerr := json.Unmarshal(b, &r); jerr != nil {
			return jerr
		}
		if r.Code != "0" {
			return fmt.Errorf("code=%s msg=%s", r.Code, r.Msg)
		}
		return nil
	}
	// funding(6) USDT 可用额
	var bal struct {
		Data []struct {
			Details []struct {
				Ccy, AvailBal string
			} `json:"details"`
		} `json:"data"`
	}
	if err := get("/api/v5/asset/balances", &bal); err != nil {
		return 0, fmt.Errorf("funding 余额读败（fail-closed 不腾挪）: %w", err)
	}
	funding := 0.0
	for _, d := range bal.Data {
		for _, det := range d.Details {
			if det.Ccy == "USDT" {
				funding, _ = strconv.ParseFloat(det.AvailBal, 64)
			}
		}
	}
	// funding 盖不住 → savings 赎 min(缺口, 可赎) 落 funding（D-549 第三源同径）
	if funding < needed {
		var sav struct {
			Data []struct {
				Amt string `json:"amt"`
			} `json:"data"`
		}
		if err := get("/api/v5/finance/savings/balance?ccy=USDT", &sav); err != nil {
			return 0, fmt.Errorf("savings 可赎额读败（fail-closed 不赎）: %w", err)
		}
		savAvail := 0.0
		if len(sav.Data) > 0 {
			savAvail, _ = strconv.ParseFloat(sav.Data[0].Amt, 64)
		}
		redeem := needed - funding
		if savAvail < redeem {
			redeem = savAvail
		}
		if redeem > 1e-6 {
			if err := post("/api/v5/finance/savings/purchase-redempt",
				fmt.Sprintf(`{"ccy":"USDT","amt":"%s","side":"redempt"}`,
					strconv.FormatFloat(redeem, 'f', 8, 64))); err != nil {
				return 0, fmt.Errorf("savings 赎回败（fail-closed 不划不申）: %w", err)
			}
			funding += redeem
		}
	}
	// 6→18 划转 min(缺口, funding 现值)
	mv := needed
	if funding < mv {
		mv = funding
	}
	if mv <= 1e-6 {
		return 0, fmt.Errorf("可装配额零（funding=%.4f savings 耗尽）", funding)
	}
	if err := post("/api/v5/asset/transfer",
		fmt.Sprintf(`{"ccy":"USDT","amt":"%s","from":"6","to":"18","type":"0"}`,
			strconv.FormatFloat(mv, 'f', 8, 64))); err != nil {
		return 0, fmt.Errorf("funding→unified 划转败: %w", err)
	}
	return mv, nil
}

// gateAssembleMargin gate 所内装配：needed USD 缺口 → spot USDT 可用读 →
// 划转 min(缺口, spot 可用) spot→futures。返回装配金额；0/败=维持拒。
func (e *CEXExecutor) gateAssembleMargin(ctx context.Context, needed float64) (float64, error) {
	if needed <= 0 {
		return 0, nil
	}
	get := func(path string, out any) error {
		ts := gateNow()
		h := http.Header{
			"KEY":       {e.apiKey},
			"Timestamp": {strconv.FormatInt(ts, 10)},
			"SIGN":      {signutil.GateSign(e.apiSecret, "GET", "/api/v4"+path, "", "", ts)},
		}
		b, err := e.get(ctx, e.baseURL+path, h)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
	var spot []struct {
		Currency  string `json:"currency"`
		Available string `json:"available"`
	}
	if err := get("/spot/accounts?currency=USDT", &spot); err != nil {
		return 0, fmt.Errorf("spot 余额读败（fail-closed 不腾挪）: %w", err)
	}
	spotAvail := 0.0
	for _, a := range spot {
		if a.Currency == "USDT" {
			spotAvail, _ = strconv.ParseFloat(a.Available, 64)
		}
	}
	mv := needed
	if spotAvail < mv {
		mv = spotAvail
	}
	if mv <= 1e-6 {
		return 0, fmt.Errorf("spot 可动额零（spot=%.4f）", spotAvail)
	}
	ts := gateNow()
	bodyStr := fmt.Sprintf(`{"currency":"USDT","from":"spot","to":"futures","amount":"%s"}`,
		strconv.FormatFloat(mv, 'f', 8, 64))
	h := http.Header{
		"KEY":          {e.apiKey},
		"Timestamp":    {strconv.FormatInt(ts, 10)},
		"SIGN":         {signutil.GateSign(e.apiSecret, "POST", "/api/v4/wallet/transfers", "", bodyStr, ts)},
		"Content-Type": {"application/json"},
	}
	if _, err := e.post(ctx, e.baseURL+"/wallet/transfers", bodyStr, h); err != nil {
		return 0, fmt.Errorf("spot→futures 划转败: %w", err)
	}
	return mv, nil
}
