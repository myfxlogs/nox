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
	// D-273 S2：同步保留币种明细（无 USDT 对的资产 usdt_value=0 但 qty 保留——earn 持币判断需要）。
	total := 0.0
	var holdings []*v1.Holding
	for _, b := range acc.Balances {
		free, _ := strconv.ParseFloat(b.Free, 64)
		locked, _ := strconv.ParseFloat(b.Locked, 64)
		qty := free + locked
		if qty <= 0 {
			continue
		}
		// D-361 S2：Simple Earn 封装币（LD 前缀）剥前缀按底层币计价——LDUSDT→USDT=1、
		// LDTRX→TRXUSDT ticker；剥后仍无交易对 → 跳过同现规约（明细保留不猜价，
		// BFUSD/LDXUSD 类「LD 后接非常规币」走同一路径）。
		asset := strings.TrimPrefix(b.Asset, "LD")
		if asset == "USDT" {
			total += qty
			holdings = append(holdings, &v1.Holding{Ccy: b.Asset, Qty: qty, UsdtValue: qty})
			continue
		}
		price, perr := q.binanceUsdtPrice(ctx, spotBase, asset)
		if perr != nil {
			// 无 USDT 对的资产（如仅 BTC 计价的小 alt）：不计入估算，明细保留
			holdings = append(holdings, &v1.Holding{Ccy: b.Asset, Qty: qty})
			continue
		}
		total += qty * price
		holdings = append(holdings, &v1.Holding{Ccy: b.Asset, Qty: qty, UsdtValue: qty * price})
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

// binanceUsdtPrice 取资产 USDT 价（公开接口，无需签名）。
func (q *CEXAccountQuerier) binanceUsdtPrice(ctx context.Context, spotBase, asset string) (float64, error) {
	body, err := q.get(ctx, spotBase+"/v3/ticker/price?symbol="+asset+"USDT", nil) // 令牌由 httpx 注入
	if err != nil {
		return 0, err
	}
	var tp struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(body, &tp); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(tp.Price, 64)
}

// binanceSign HMAC-SHA256 hex 签名（与 exec.CEXExecutor 同法）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
