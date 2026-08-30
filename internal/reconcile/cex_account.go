package reconcile

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"arb/internal/exec"
	"arb/internal/httpx"
)

// CEXAccountQuerier 查 CEX 账户资金（D-053）。只读余额，不执行。
// Binance：GET /api/v3/account（HMAC-SHA256 签名 + X-MBX-APIKEY），权益 = Σ 持币×USDT 价（公开 ticker/price 估算）。
// OKX：GET /api/v5/account/balance（OK-ACCESS-* 四头，SIGN=base64(HMAC-SHA256(secret, ts+method+path+body))），totalEq 为交易所权威 USD。
type CEXAccountQuerier struct {
	venue      string // binance / okx
	venueType  string // spot
	apiKey     string
	apiSecret  string
	passphrase string // OKX 必填；Binance 空
	baseURL    string // spot base（binance 走代理；okx www.okx.com）
	simulated  bool   // OKX 模拟盘：请求带 x-simulated-trading:1（demo key 必开，D-054）
	website    string // D-157 V3：交易所官网（从 ExchangeConfig.website 透传）
	client     *http.Client
}

// NewCEXAccountQuerier 创建 CEX 账户查询器。
// baseURL 为 spot base（Binance 需先做 fapi→api 替换，与采集一致）。
func NewCEXAccountQuerier(venue, venueType, apiKey, apiSecret, passphrase, baseURL, website string, simulated bool) *CEXAccountQuerier {
	return &CEXAccountQuerier{
		venue: venue, venueType: venueType, apiKey: apiKey, apiSecret: apiSecret,
		passphrase: passphrase, baseURL: baseURL, website: website, simulated: simulated, client: httpx.Client(),
	}
}

// VenueKey 账户身份（"{venue}:{venueType}"，与持仓视图同 key 空间）。
func (q *CEXAccountQuerier) VenueKey() string { return q.venue + ":" + q.venueType }

// Account 查询账户余额/权益。CEX 的 Margin/FreeMargin/Credit/Profit=0（无对应概念）。
func (q *CEXAccountQuerier) Account(ctx context.Context) (*AccountInfo, error) {
	switch q.venue {
	case "binance":
		return q.accountBinance(ctx)
	case "okx":
		return q.accountOKX(ctx)
	case "gate":
		return q.accountGate(ctx)
	default:
		return nil, fmt.Errorf("cex account: unsupported venue %q", q.venue)
	}
}

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
	body, err := q.get(ctx, spotBase+"/api/v3/account?"+params.Encode(), h)
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
	total := 0.0
	for _, b := range acc.Balances {
		free, _ := strconv.ParseFloat(b.Free, 64)
		locked, _ := strconv.ParseFloat(b.Locked, 64)
		qty := free + locked
		if qty <= 0 {
			continue
		}
		if b.Asset == "USDT" {
			total += qty
			continue
		}
		price, perr := q.binanceUsdtPrice(ctx, spotBase, b.Asset)
		if perr != nil {
			continue // 无 USDT 对的资产跳过（如仅 BTC 计价的小 alt），不算入估算
		}
		total += qty * price
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: total, Equity: total, Currency: "USDT",
		Simulated: q.simulated, Website: q.website,
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
	body, err := q.get(ctx, q.baseURL+"/fapi/v2/account?"+params.Encode(), h)
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
	body, err := q.get(ctx, spotBase+"/api/v3/ticker/price?symbol="+asset+"USDT", nil) // 令牌由 httpx 注入
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

// === OKX ===

func (q *CEXAccountQuerier) accountOKX(ctx context.Context) (*AccountInfo, error) {
	const path = "/api/v5/account/balance"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path + ""
	sign := okxSign(q.apiSecret, prehash)

	headers := http.Header{
		"OK-ACCESS-KEY":        {q.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {q.passphrase},
		"Content-Type":         {"application/json"},
	}
	// 模拟盘：OKX demo key 必须对实盘域名带 x-simulated-trading:1（独立 demo 域名不存在，D-054）。
	if q.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err := q.get(ctx, q.baseURL+path, headers)
	if err != nil {
		return nil, fmt.Errorf("okx balance: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			TotalEq string `json:"totalEq"` // 交易所权威 USD 总权益
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx balance json: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx balance: code=%s msg=%s", resp.Code, resp.Msg)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("okx balance: empty data")
	}
	eq, _ := strconv.ParseFloat(resp.Data[0].TotalEq, 64)
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website,
	}, nil
}

// === 签名工具 ===

// binanceSign HMAC-SHA256 hex 签名（与 exec.CEXExecutor 同法）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// okxSign OKX 签名：base64(HMAC-SHA256(secret, ts+method+path+body))。
func okxSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// === Gate（USDT 本位合约 perp，D-098 G1）===

// accountGate 查询 Gate perp 账户权益：GET /futures/usdt/accounts（签名 GET，GateSign 共享）。
// 解析 total（USDT 总权益）→ AccountInfo。401/网络错 → error（走既有 offline 快照语义，不 panic）。
// venueType=spot → 如实返回 unsupported error（G1 只 perp，勿猜 spot 端点）。
// 根因（D-098 复审补漏）：gate 前端登记落库但 Account() switch 无 gate case → default 返回
// unsupported venue → gate 账户永不进余额条。本方法补齐 gate perp 账户查询。
func (q *CEXAccountQuerier) accountGate(ctx context.Context) (*AccountInfo, error) {
	if q.venueType != "perp" {
		return nil, fmt.Errorf("gate account: venueType %q unsupported（G1 仅 perp）", q.venueType)
	}
	const endpoint = "/futures/usdt/accounts"
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := exec.GateSign(q.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {q.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := q.get(ctx, q.baseURL+endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("gate account: %w", err)
	}
	var acc struct {
		Total string `json:"total"` // USDT 总权益（字符串）
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("gate account json: %w", err)
	}
	eq, _ := strconv.ParseFloat(acc.Total, 64)
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website,
	}, nil
}

// get 发起 GET 并读回 body（非 200 → 错误带响应体）。
func (q *CEXAccountQuerier) get(ctx context.Context, u string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
