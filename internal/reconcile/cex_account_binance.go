// cex_account_binance.go D-273 拆分：Binance spot/perp 账户查询 + ticker 估价 + 签名
// （自 cex_account.go 纯搬家，零逻辑变化；check-lines ≤300）。
package reconcile

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// === Binance ===

func (q *CEXAccountQuerier) accountBinance(ctx context.Context) (*AccountInfo, error) {
	if q.venueType == "perp" {
		return q.accountBinancePerp(ctx)
	}
	// spot base：BINANCE_PROXY_URL 是 fapi 风格（采集同法 replace fapi→api 得 spot）。
	spotBase := strings.Replace(q.baseURL, "fapi", "api", 1)
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	sig := binanceSign(q.apiSecret, params.Encode())
	params.Set("signature", sig)

	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey) // 令牌由 httpx 传输层自动注入
	body, err := q.get(ctx, spotBase+"/v3/account?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance account: %w", err)
	}
	var acc struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("binance account json: %w", err)
	}

	// 权益估算：Σ (free+locked) × USDT 价；USDT 价=1，其余取公开 ticker/price（失败该币跳过，诚实估算）。
	// D-364 v2 S1：非 USDT 标资产不读取不统计（业主明谕「其他的币种都不用读取和统计」
	// ——USDT=CEX 本位币对标 CFD USD）——循环首行过滤，非 USDT 标不进 total/holdings/
	// 不发 ticker 定价请求（~90 个 LD* 杂币每轮省 ~90 次 ticker 调用）。
	// D-361 LD 计价链（binanceUsdtPrice 兜底序）随「不读取」不可达，整段删除（史实在 git）。
	total := 0.0
	var holdings []*v1.Holding
	for _, b := range acc.Balances {
		free, _ := strconv.ParseFloat(b.Free, 64)
		locked, _ := strconv.ParseFloat(b.Locked, 64)
		qty := free + locked
		if qty <= 0 {
			continue
		}
		if b.Asset != "USDT" && strings.TrimPrefix(b.Asset, "LD") != "USDT" {
			continue // 非 USDT 标（LDUSDC/LDTRX/LDO/BFUSD/杂币）：不读取不统计
		}
		// USDT/LDUSDT 皆 1:1 计 qty，无需价
		total += qty
		holdings = append(holdings, &v1.Holding{Ccy: b.Asset, Qty: qty, UsdtValue: qty})
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: total, Equity: total, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// accountBinancePerp 合约账户权益：GET /fapi/v2/account，totalWalletBalance 为权威 USDT 权益
// （模拟盘合约在 demo-fapi.binance.com，D-059 实施修正② 复核实测：demo key 对 USDT-M 合约
// 同 key 有效，-1109 已解；baseURL 由调用方给到 demofapi 代理前缀，此处直接拼 /fapi 路径）。
func (q *CEXAccountQuerier) accountBinancePerp(ctx context.Context) (*AccountInfo, error) {
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	sig := binanceSign(q.apiSecret, params.Encode())
	params.Set("signature", sig)

	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey) // 令牌由 httpx 传输层自动注入
	body, err := q.get(ctx, q.baseURL+"/v2/account?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance perp account: %w", err)
	}
	var acc struct {
		TotalWalletBalance string `json:"totalWalletBalance"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("binance perp account json: %w", err)
	}
	eq, _ := strconv.ParseFloat(acc.TotalWalletBalance, 64)
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website,
	}, nil
}

// binanceSign HMAC-SHA256 hex 签名（与 exec.CEXExecutor 同法）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
