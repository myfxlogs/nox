// cex_executor_assembly_binance.go —— binance 装配链补齐（D-549 装配模式第三所：
// earn 活期→spot→UM futures 三段提取）。目标池（UM futures availableBalance）
// 不足时：spot 免费读 → 缺口先赎 min(缺口, earn 活期可赎) 落 spot →
// 划转 min(缺口, spot 实值) MAIN_UMFUTURE。装配额=min(缺口, 可动源) 禁放大
// 禁预腾攒池；读败/腾挪败 fail-closed 如实拒因。仅执行时按需调用。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"nox/internal/platform/httpx"
	"strconv"
	"time"
)

// binanceAssembleMargin binance 所内装配：needed USD 缺口 → spot USDT free 读 →
// 缺口先赎 earn 活期（simple-earn flexible position 行）落 spot → spot 重读 →
// POST /sapi/v1/asset/transfer type=MAIN_UMFUTURE min(缺口, spot 实值)。
// 返回装配金额；0/败=调用方维持 insufficient 拒。
func (e *CEXExecutor) binanceAssembleMargin(ctx context.Context, needed float64) (float64, error) {
	if needed <= 0 {
		return 0, nil
	}
	apiBase := httpx.BinanceApiSegBase(e.baseURL, "api")   // spot /v3/account
	sapiBase := httpx.BinanceApiSegBase(e.baseURL, "sapi") // earn/asset 族
	get := func(base, path string, params url.Values, out any) error {
		params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli()+binanceClockOffset.Load(), 10))
		params.Set("recvWindow", "5000")
		params.Set("signature", binanceSign(e.apiSecret, params.Encode()))
		b, err := e.get(ctx, base+path+"?"+params.Encode(), http.Header{"X-MBX-APIKEY": {e.apiKey}})
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	}
	post := func(base, path string, params url.Values) error {
		params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli()+binanceClockOffset.Load(), 10))
		params.Set("recvWindow", "5000")
		params.Set("signature", binanceSign(e.apiSecret, params.Encode()))
		_, err := e.post(ctx, base+path, params.Encode(), http.Header{
			"X-MBX-APIKEY": {e.apiKey}, "Content-Type": {"application/x-www-form-urlencoded"},
		})
		return err
	}
	var spotAcc struct {
		Balances []struct {
			Asset string `json:"asset"`
			Free  string `json:"free"`
		} `json:"balances"`
	}
	if err := get(apiBase, "/v3/account", url.Values{}, &spotAcc); err != nil {
		return 0, fmt.Errorf("spot 余额读败（fail-closed 不腾挪）: %w", err)
	}
	spotAvail := 0.0
	for _, b := range spotAcc.Balances {
		if b.Asset == "USDT" {
			spotAvail, _ = strconv.ParseFloat(b.Free, 64)
		}
	}
	// earn 赎回段（D-549 同构第三源）：simple-earn 活期持仓赎回落 spot。
	// venue 语义（bnapi_earn 写径同源）：fast 赎回即到账——划转额仍只认
	// 赎回提交后 spot 重读实值；未到账部分如实部分装配，下拍重入续装。
	if spotAvail < needed {
		var pos struct {
			Rows []struct {
				ProductID   string `json:"productId"`
				TotalAmount string `json:"totalAmount"`
				Amount      string `json:"amount"`
			} `json:"rows"`
		}
		if err := get(sapiBase, "/v1/simple-earn/flexible/position",
			url.Values{"asset": {"USDT"}}, &pos); err != nil {
			return 0, fmt.Errorf("earn 活期可赎额读败（fail-closed 不赎不划）: %w", err)
		}
		remain := needed - spotAvail
		for _, row := range pos.Rows {
			if remain <= 1e-6 || row.ProductID == "" {
				break
			}
			amt := row.TotalAmount
			if amt == "" {
				amt = row.Amount
			}
			hold, _ := strconv.ParseFloat(amt, 64)
			if hold <= 1e-6 {
				continue
			}
			mv := remain
			if hold < mv {
				mv = hold
			}
			if err := post(sapiBase, "/v1/simple-earn/flexible/redeem",
				url.Values{"productId": {row.ProductID},
					"amount": {strconv.FormatFloat(mv, 'f', 8, 64)}}); err != nil {
				return 0, fmt.Errorf("earn 赎回败（fail-closed 不划）%s: %w", row.ProductID, err)
			}
			remain -= mv
		}
		if remain < needed-spotAvail { // 有赎回提交→spot 重读实值
			spotAcc.Balances = nil
			if err := get(apiBase, "/v3/account", url.Values{}, &spotAcc); err != nil {
				return 0, fmt.Errorf("赎回后 spot 重读败（fail-closed 不划）: %w", err)
			}
			spotAvail = 0.0
			for _, b := range spotAcc.Balances {
				if b.Asset == "USDT" {
					spotAvail, _ = strconv.ParseFloat(b.Free, 64)
				}
			}
		}
	}
	mv := needed
	if spotAvail < mv {
		mv = spotAvail
	}
	if mv <= 1e-6 {
		return 0, fmt.Errorf("spot 可动额零（spot=%.4f）", spotAvail)
	}
	if err := post(sapiBase, "/v1/asset/transfer",
		url.Values{"type": {"MAIN_UMFUTURE"}, "asset": {"USDT"},
			"amount": {strconv.FormatFloat(mv, 'f', 8, 64)}}); err != nil {
		return 0, fmt.Errorf("spot→UM futures 划转败: %w", err)
	}
	return mv, nil
}
