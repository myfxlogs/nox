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

	"arb/internal/httpx"
)

// CEXExecutor 是 Binance/OKX REST 签名下单执行器（§13.2）。
// 每个市场一个实例（spot/perp 是不同实例）。D-059：补 OKX 下单（OK-ACCESS-* 四头 +
// base64 HMAC + x-simulated-trading:1 模拟盘），venue 分派。
type CEXExecutor struct {
	venue      string
	venueType  string // spot / perp
	apiKey     string
	apiSecret  string
	passphrase string // OKX 必填；Binance 空
	baseURL    string // binance: proxy/demo 根；okx: www.okx.com
	simulated  bool   // OKX 模拟盘：请求带 x-simulated-trading:1（demo key 必开，D-054 同款）
	client     *http.Client
}

// NewCEXExecutor 创建 CEX 执行器。
// baseURL 为 venue 根（binance 走代理 fapi 风格需已含 /demo 路径；okx 实盘域名）。
// simulated 仅 OKX 生效（Binance demo 由 baseURL 的 /demo 前缀路由，D-055）。
func NewCEXExecutor(venue, venueType, apiKey, apiSecret, passphrase, baseURL string, simulated bool) *CEXExecutor {
	return &CEXExecutor{
		venue:      venue,
		venueType:  venueType,
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		passphrase: passphrase,
		baseURL:    baseURL,
		simulated:  simulated,
		client:     httpx.Client(), // CA 信任 + X-Proxy-Token 注入（D-055）；OKX 公网忽略注入头
	}
}

func (e *CEXExecutor) Key() string { return e.venue + ":" + e.venueType }

func (e *CEXExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	switch e.venue {
	case "binance":
		return e.placeBinance(ctx, o)
	case "okx":
		return e.placeOKX(ctx, o)
	case "gate":
		return e.placeGate(ctx, o)
	default:
		return Fill{}, fmt.Errorf("cex executor: unsupported venue %q", e.venue)
	}
}

// placeBinance 下单：spot /api/v3/order、perp /fapi/v1/order，HMAC-SHA256 hex 签名 + X-MBX-APIKEY。
func (e *CEXExecutor) placeBinance(ctx context.Context, o Order) (Fill, error) {
	side := "BUY"
	if o.Side == "SELL" {
		side = "SELL"
	}
	params := url.Values{}
	params.Set("symbol", o.Symbol)
	params.Set("side", side)
	params.Set("type", "MARKET")
	params.Set("quantity", strconv.FormatFloat(o.Qty, 'f', -1, 64))

	endpoint := "/api/v3/order"
	if e.venueType == "perp" {
		endpoint = "/fapi/v1/order"
	}

	body, err := e.signedBinanceRequest(ctx, "POST", endpoint, params)
	if err != nil {
		return Fill{}, fmt.Errorf("cex order: %w", err)
	}

	execType, _ := body["executedQty"].(string)
	orderID, _ := body["orderId"].(json.Number)
	if orderID == "" {
		if id, ok := body["orderId"].(float64); ok {
			orderID = json.Number(strconv.FormatFloat(id, 'f', 0, 64))
		}
	}

	qty, _ := strconv.ParseFloat(execType, 64)
	avgPrice := binanceOrderAvgPrice(body, qty)

	if e.venueType == "perp" && (qty <= 0 || avgPrice <= 0) {
		// 合约市价单 POST 可能先于撮合返回（status=NEW/executedQty=0，demo-fapi 实测恒如此，
		// 同 D-059 实施修正② 的 OKX 教训；spot 市价单响应同步成交，无需轮询）→ 轮询订单状态
		// 拿真实成交，防生产记 0 qty 持仓。
		for range 5 {
			time.Sleep(100 * time.Millisecond)
			st, err := e.binanceOrderStatus(ctx, o.Symbol, string(orderID))
			if err != nil {
				return Fill{}, err
			}
			if st.Status == "CANCELED" || st.Status == "REJECTED" || st.Status == "EXPIRED" {
				// 未成交即被取消/拒绝 → 平仓失败，如实报错而非返回 qty=0 假装成交。
				return Fill{}, fmt.Errorf("binance order %s: status=%s executedQty=%s（未成交，订单被取消/拒绝）",
					orderID, st.Status, st.ExecutedQty)
			}
			if st.Status == "FILLED" || st.Status == "PARTIALLY_FILLED" {
				q, perr := strconv.ParseFloat(st.ExecutedQty, 64)
				p, aerr := strconv.ParseFloat(st.AvgPrice, 64)
				if perr == nil && aerr == nil && q > 0 && p > 0 {
					return Fill{OrderID: string(orderID), Price: p, Qty: q, Ts: time.Now()}, nil
				}
			}
		}
	}
	// 超时仍未拿到成交：如实返回已下订单 + 当前已知 qty（可 0），不伪造。
	return Fill{
		OrderID: string(orderID),
		Price:   avgPrice,
		Qty:     qty,
		Ts:      time.Now(),
	}, nil
}

// placeOKX 下单：POST /api/v5/trade/order（OK-ACCESS-* 四头，SIGN=base64(HMAC-SHA256(secret,
// ts+method+path+body))）。spot tdMode=cash；perp tdMode=cross（代码保留，本次不验证）。
// 模拟盘带 x-simulated-trading:1（OKX demo key 必开，D-054 同款）。
func (e *CEXExecutor) placeOKX(ctx context.Context, o Order) (Fill, error) {
	side := "buy"
	if o.Side == "SELL" {
		side = "sell"
	}
	tdMode := "cash" // spot
	instID := o.Symbol
	if e.venueType == "perp" {
		tdMode = "cross"
		if !strings.HasSuffix(instID, "-SWAP") {
			instID = instID + "-SWAP" // OKX perp instId 形如 BTC-USDT-SWAP
		}
	}
	sz := strconv.FormatFloat(o.Qty, 'f', -1, 64)
	var bodyStr string
	if e.venueType == "perp" {
		// swap/futures 必须带 posSide（D-059 实施修正② 实测 51000）。本账户 posMode=long_short_mode
		// （account/config 实测）。开仓传空 → buy=long / sell=short 派生；平仓由调用方显式传
		// PositionSide=原持仓侧（BUY 开 long → SELL 平 + posSide=long 才减 long，D-059 实测
		// SELL+posSide=short 会新开空仓）。net_mode 账户需改 posSide=net。
		posSide := o.PositionSide
		if posSide == "" {
			posSide = "long"
			if side == "sell" {
				posSide = "short"
			}
		}
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"posSide":%q}`,
			instID, tdMode, side, sz, posSide)
	} else {
		// spot 市价单：缺省买方向 sz=报价币金额（0.0001 当 0.0001 USDT → 51020 最小金额，实测）。
		// 显式 tgtCcy=base_ccy 固定 sz=基础币数量，与 exec.Order.Qty 契约一致。
		bodyStr = fmt.Sprintf(`{"instId":%q,"tdMode":%q,"side":%q,"ordType":"market","sz":%q,"tgtCcy":"base_ccy"}`,
			instID, tdMode, side, sz)
	}

	path := "/api/v5/trade/order"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "POST" + path + bodyStr
	sign := okxSign(e.apiSecret, prehash)

	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
		"Content-Type":         {"application/json"},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}

	body, err := e.post(ctx, e.baseURL+path, bodyStr, headers)
	if err != nil {
		return Fill{}, fmt.Errorf("okx order: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			OrdID     string `json:"ordId"`
			SCode     string `json:"sCode"`
			SMsg      string `json:"sMsg"`
			AvgPx     string `json:"avgPx"`
			AccFillSz string `json:"accFillSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return Fill{}, fmt.Errorf("okx order json: %w", err)
	}
	if resp.Code != "0" {
		// code=1「All operations failed」是包装错误，真实原因在 data[0].sCode/sMsg（D-059 实测）。
		detail := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			detail = fmt.Sprintf("%s (sCode=%s sMsg=%s)", resp.Msg, resp.Data[0].SCode, resp.Data[0].SMsg)
		}
		return Fill{}, fmt.Errorf("okx order: code=%s msg=%s", resp.Code, detail)
	}
	if len(resp.Data) == 0 {
		return Fill{}, fmt.Errorf("okx order: empty data")
	}
	d := resp.Data[0]
	if d.SCode != "" && d.SCode != "0" {
		return Fill{}, fmt.Errorf("okx order: sCode=%s sMsg=%s", d.SCode, d.SMsg)
	}
	qty, _ := strconv.ParseFloat(d.AccFillSz, 64)
	avgPx, _ := strconv.ParseFloat(d.AvgPx, 64)
	if qty <= 0 || avgPx <= 0 {
		// 市价单 POST 响应可能先于撮合返回（accFillSz=0/avgPx 空，D-059 实施修正② 实测：
		// 订单实际成交但响应无成交数据）→ 轮询订单状态拿真实成交，防生产记 0 qty 持仓。
		for range 5 {
			time.Sleep(100 * time.Millisecond)
			o, err := e.okxOrderStatus(ctx, instID, d.OrdID)
			if err != nil {
				return Fill{}, err
			}
			if o.State == "canceled" || o.State == "rejected" || o.State == "mmp_canceled" {
				// 订单未成交即被取消/拒绝 → 平仓失败，如实报错而非返回 qty=0 假装成交
				// （D-059 实测：spot SELL 曾因模拟盘撮合取消，旧逻辑静默返回 0 致测试假绿）。
				// cancelSourceReason 携带取消根因（如「滑超 best bid 5% 保护」），生产排障必读。
				reason := strings.TrimSpace(o.CancelSourceReason)
				if reason != "" {
					reason = ": " + reason
				}
				return Fill{}, fmt.Errorf("okx order %s: state=%s accFillSz=%s avgPx=%s（未成交，订单被取消/拒绝%s）",
					d.OrdID, o.State, o.AccFillSz, o.AvgPx, reason)
			}
			if o.AccFillSz != "" && o.AvgPx != "" {
				if q, err := strconv.ParseFloat(o.AccFillSz, 64); err == nil && q > 0 {
					qty = q
				}
				if p, err := strconv.ParseFloat(o.AvgPx, 64); err == nil && p > 0 {
					avgPx = p
				}
				if qty > 0 && avgPx > 0 {
					return Fill{OrderID: d.OrdID, Price: avgPx, Qty: qty, Ts: time.Now()}, nil
				}
			}
		}
	}
	// 超时仍未拿到成交：如实返回已下订单 + 当前已知 qty（可 0），不伪造。
	return Fill{OrderID: d.OrdID, Price: avgPx, Qty: qty, Ts: time.Now()}, nil
}

func (e *CEXExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	// CEX 平仓 = 反向市价单
	symbol := ref.Symbol
	// 推断 side：CEX 没有"close"概念，需要外部传入方向
	// 简化：CloseOrder 在 CEX 场景由执行管线直接调 PlaceOrder 反向
	// 此方法保留接口完整性，实际 CEX 平仓走 PlaceOrder
	_ = symbol
	return Fill{}, fmt.Errorf("cex: use PlaceOrder with opposite side for close")
}
