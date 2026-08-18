package exec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CEXExecutor 是 Binance/OKX REST 签名下单执行器（§13.2）。
// 每个市场一个实例（spot/perp 是不同实例）。
type CEXExecutor struct {
	venue     string
	venueType string // spot / perp
	apiKey    string
	apiSecret string
	baseURL   string // spot: api.binance.com; perp: fapi.binance.com
	client    *http.Client
}

// NewCEXExecutor 创建 CEX 执行器。
func NewCEXExecutor(venue, venueType, apiKey, apiSecret, baseURL string) *CEXExecutor {
	return &CEXExecutor{
		venue:     venue,
		venueType: venueType,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		baseURL:   baseURL,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (e *CEXExecutor) Key() string { return e.venue + ":" + e.venueType }

func (e *CEXExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
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

	body, err := e.signedRequest(ctx, "POST", endpoint, params)
	if err != nil {
		return Fill{}, fmt.Errorf("cex order: %w", err)
	}

	execType, _ := body["executedQty"].(string)
	price, _ := body["avgPrice"].(string)
	orderID, _ := body["orderId"].(json.Number)
	if orderID == "" {
		if id, ok := body["orderId"].(float64); ok {
			orderID = json.Number(strconv.FormatFloat(id, 'f', 0, 64))
		}
	}

	qty, _ := strconv.ParseFloat(execType, 64)
	avgPrice, _ := strconv.ParseFloat(price, 64)

	return Fill{
		OrderID: string(orderID),
		Price:   avgPrice,
		Qty:     qty,
		Ts:      time.Now(),
	}, nil
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

func (e *CEXExecutor) signedRequest(ctx context.Context, method, endpoint string, params url.Values) (map[string]any, error) {
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")

	signature := e.sign(params.Encode())
	params.Set("signature", signature)

	u := e.baseURL + endpoint + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", e.apiKey)

	resp, err := e.client.Do(req)
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
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return m, nil
}

func (e *CEXExecutor) sign(query string) string {
	mac := hmac.New(sha256.New, []byte(e.apiSecret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
