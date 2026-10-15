// dci_sigma.go —— D-526①：标的日收益波动率 σ_daily 实算。
// 源=venue 日 K REST：binance GET {base}/fapi/v1/klines?interval=1d、
// okx GET {base}/api/v5/market/history-candles?bar=1D。
// D-004 豁免举证：σ 为统计参考输入非判定实时数据（ticks/index_price 本地留存
// ~5d 撑不起波动率窗口——10-15 决策席取证），venue WS K 线频道存在但按统计
// 用途逐拍拉日 K 合规。读败/样本不足 → err（fail-closed，调用方不产提案）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// SigmaDaily 日收益对数差样本标准差；days=窗口天数（app_config 可调），
// limit=days+1 根日 K（首根作基准价）。
func SigmaDaily(ctx context.Context, client *http.Client, baseURL, venue, symbol string, days int) (float64, error) {
	if days < 3 {
		return 0, fmt.Errorf("sigma window %d<3 样本不足", days)
	}
	closes, err := dailyCloses(ctx, client, baseURL, venue, symbol, days+1)
	if err != nil {
		return 0, err
	}
	if len(closes) < days {
		return 0, fmt.Errorf("%s 日 K 样本 %d<%d", venue, len(closes), days)
	}
	var rs []float64
	for i := 1; i < len(closes); i++ {
		if closes[i-1] <= 0 || closes[i] <= 0 {
			continue
		}
		rs = append(rs, math.Log(closes[i]/closes[i-1]))
	}
	if len(rs) < days-1 {
		return 0, fmt.Errorf("%s 有效收益样本 %d<%d", venue, len(rs), days-1)
	}
	var mu float64
	for _, r := range rs {
		mu += r
	}
	mu /= float64(len(rs))
	var sd float64
	for _, r := range rs {
		sd += (r - mu) * (r - mu)
	}
	return math.Sqrt(sd / float64(len(rs)-1)), nil
}

// dailyCloses venue 日 K 收盘价序列（旧→新序归一；okx 原生新→旧倒排翻转）。
func dailyCloses(ctx context.Context, client *http.Client, baseURL, venue, symbol string, limit int) ([]float64, error) {
	switch venue {
	case "binance":
		q := url.Values{}
		q.Set("symbol", symbol) // BTCUSDT
		q.Set("interval", "1d")
		q.Set("limit", strconv.Itoa(limit))
		u := strings.TrimRight(baseURL, "/") + "/fapi/v1/klines?" + q.Encode()
		arr, err := getJSONArray(ctx, client, u)
		if err != nil {
			return nil, fmt.Errorf("binance klines: %w", err)
		}
		var rows [][]any
		for _, a := range arr {
			if rr, ok := a.([]any); ok {
				rows = append(rows, rr)
			}
		}
		out := make([]float64, 0, len(rows))
		for _, r := range rows {
			if len(r) < 5 {
				continue
			}
			c, _ := strconv.ParseFloat(fmt.Sprint(r[4]), 64)
			out = append(out, c)
		}
		return out, nil
	case "okx":
		q := url.Values{}
		q.Set("instId", symbol) // BTC-USDT
		q.Set("bar", "1D")
		q.Set("limit", strconv.Itoa(limit))
		u := strings.TrimRight(baseURL, "/") + "/api/v5/market/history-candles?" + q.Encode()
		m, err := getJSON(ctx, client, u)
		if err != nil {
			return nil, fmt.Errorf("okx candles: %w", err)
		}
		var resp struct {
			Data [][]string `json:"data"`
		}
		if b, _ := json.Marshal(m); json.Unmarshal(b, &resp) != nil {
			return nil, fmt.Errorf("okx candles json: 结构不符")
		}
		out := make([]float64, 0, len(resp.Data))
		for i := len(resp.Data) - 1; i >= 0; i-- { // 新→旧翻旧→新
			r := resp.Data[i]
			if len(r) < 5 {
				continue
			}
			c, _ := strconv.ParseFloat(r[4], 64)
			out = append(out, c)
		}
		return out, nil
	}
	return nil, fmt.Errorf("sigma 源未接 venue=%s", venue)
}
