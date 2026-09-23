package reconcile

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/exec"
	"arb/internal/httpx"
)

// CEXAccountQuerier 查 CEX 账户资金（D-053）。只读余额，不执行。
// Binance：GET /api/v3/account（HMAC-SHA256 签名 + X-MBX-APIKEY），权益 = Σ 持币×USDT 价（公开 ticker/price 估算）。
// OKX：GET /api/v5/account/balance（OK-ACCESS-* 四头，SIGN=base64(HMAC-SHA256(secret, ts+method+path+body))），totalEq 为交易所权威 USD。
type CEXAccountQuerier struct {
	venue       string // binance / okx
	venueType   string // spot
	apiKey      string
	apiSecret   string
	passphrase  string // OKX 必填；Binance 空
	baseURL     string // spot base（binance 走代理；okx www.okx.com）
	simulated   bool   // OKX 模拟盘：请求带 x-simulated-trading:1（demo key 必开，D-054）
	website     string // D-157 V3：交易所官网（从 ExchangeConfig.website 透传）
	keyOverride string // D-362 S2：撞键非赢家的注册键覆盖（"venue:type#<id>"；空=canonical）
	client      *http.Client
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
// D-362 S2：撞键非赢家经 SetKeyOverride 返回 "venue:type#<id>"（只在键，AccountInfo
// 数据字段 venue/venueType 保持原值——前端徽标区分，SSOT §2）。
func (q *CEXAccountQuerier) VenueKey() string {
	if q.keyOverride != "" {
		return q.keyOverride
	}
	return q.venue + ":" + q.venueType
}

// SetKeyOverride D-362 S2：设置撞键归一后的注册键（由 reconcile.CexVenueKey 计算）。
func (q *CEXAccountQuerier) SetKeyOverride(k string) { q.keyOverride = k }

// Account 查询账户余额/权益。CEX 的 Margin/FreeMargin/Credit/Profit=0（无对应概念）。
func (q *CEXAccountQuerier) Account(ctx context.Context) (*AccountInfo, error) {
	// D-363 S2：资金（funding）/理财（earn）子账户先于交易账户分派（同函数签名零新管道）。
	if q.venueType == "funding" || q.venueType == "earn" {
		return q.accountSubaccount(ctx)
	}
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
			TotalEq  string `json:"totalEq"`  // 交易所权威 USD 总权益
			MgnRatio string `json:"mgnRatio"` // D-405 S2：保证金率（百分数口径，越高越安全）
			Details  []struct {
				Ccy      string `json:"ccy"`
				Eq       string `json:"eq"`       // 本币权益（2026-09-10 实采实证：币种计价，非 USD）
				CashBal  string `json:"cashBal"`  // 本币数量（总持有）
				AvailBal string `json:"availBal"` // 可用余额
				EqUsd    string `json:"eqUsd"`    // USD 等值
			} `json:"details"`
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
	// D-273 S2：币种明细（实采语义核验 2026-09-10——qty=cashBal 缺失 fallback availBal；usdt_value=eqUsd
	//（设计原写 eq，实采证明 eq 为本币权益，BTC eq=1 而 eqUsd=76757，按 eq 存会差 4 个数量级）；解析失败跳过该币）。
	// D-364 v2 S2：holdings 同「不读取」语义——details 循环只保留 Ccy=="USDT" 行，
	// 其余币种连明细行都不进（业主明谕非 USDT 标不读取不统计）。
	var holdings []*v1.Holding
	for _, d := range resp.Data[0].Details {
		if d.Ccy != "USDT" {
			continue
		}
		qtyStr := d.CashBal
		if qtyStr == "" {
			qtyStr = d.AvailBal
		}
		qty, perr := strconv.ParseFloat(qtyStr, 64)
		if perr != nil || qty <= 0 {
			continue
		}
		usd, _ := strconv.ParseFloat(d.EqUsd, 64)
		holdings = append(holdings, &v1.Holding{Ccy: d.Ccy, Qty: qty, UsdtValue: usd})
	}
	// D-364 S2：equity 只计 USDT 标（业主令）——eq=Σ details[USDT].EqUsd（USDT eqUsd=
	// 本币 1:1，复用已解析字段零新增）；details 无 USDT 行 → eq=0 不 panic。
	// 旧 TotalEq=统一账户全币总权益（含 DOGE/PENGU 类杂币折算），不再采用。
	// holdings 明细不动（全币展示保留，D-273 S2）。
	eq := 0.0
	for _, d := range resp.Data[0].Details {
		if d.Ccy != "USDT" {
			continue
		}
		if usd, perr := strconv.ParseFloat(d.EqUsd, 64); perr == nil {
			eq += usd
		}
	}
	// D-405 S2：buffer_multiple=mgnRatio/100（官方口径百分数→倍数写时归一；
	// 空/解析失败 → 0=未采，guard 判 data_stale；空仓账户 mgnRatio 空值同样落 0）。
	var buffer float64
	if mr, perr := strconv.ParseFloat(resp.Data[0].MgnRatio, 64); perr == nil && mr > 0 {
		buffer = mr / 100
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
		BufferMultiple: buffer,
	}, nil
}

// === 签名工具 ===

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
