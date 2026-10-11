// dci_test.go —— D-183：三所 DCI 解析锚（strike/tenor/apy 映射+BTC/ETH 过滤+隔离）。
package cexscan

import (
	"context"
	"strconv"
	"testing"
	"time"

	"arb/internal/httpx"
)

func TestD183_ParseBinanceDCI(t *testing.T) {
	body := []byte(`{"data":[
		{"id":"74151","exerciseCoin":"BTC","investCoin":"USDT","strikePrice":"70000","deliveryDate":"1760227200000","apy":"0.4567","optionType":"CALL","canPurchase":true},
		{"id":"74152","exerciseCoin":"DOGE","investCoin":"USDT","strikePrice":"0.2","deliveryDate":"1760227200000","apy":"0.9","optionType":"PUT"},
		{"id":"74153","exerciseCoin":"BTC","investCoin":"BTC","strikePrice":"80000","deliveryDate":"1760486400000","apy":"0.31","optionType":"PUT"}]}`)
	rows, err := parseBinanceDCI(body, time.UnixMilli(1760054400000)) // 交割前 2d/7d
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应只留 BTC 行（DOGE 滤出）, got %d", len(rows))
	}
	r := rows[0]
	if r.Venue != "binance" || r.Underlying != "BTC" || r.InvestCcy != "USDT" || r.OptType != "CALL" {
		t.Fatalf("字段映射错: %+v", r)
	}
	if r.Strike == nil || *r.Strike != 70000 {
		t.Fatalf("strike 错: %+v", r.Strike)
	}
	if r.TenorDays == nil || *r.TenorDays != 2 {
		t.Fatalf("tenor 错: %+v", r.TenorDays)
	}
	if r.APY != 0.4567 {
		t.Fatalf("apy 错: %v", r.APY)
	}
}

func TestD183_ParseOKXDCI(t *testing.T) {
	body := []byte(`{"code":"0","msg":"","data":[
		{"id":"x1","strike":"72000","apy":"0.52","expTime":"1760227200000","investCcy":"USDT","term":"2"},
		{"id":"x2","strikePx":"73000","apy":"0.61","term":"7","quoteCcy":"USDT"}]}`)
	rows, err := parseOKXDCI(body, "BTC", "C", time.UnixMilli(1760054400000))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("行数错: %d", len(rows))
	}
	if rows[0].OptType != "C" || rows[0].Underlying != "BTC" || *rows[0].Strike != 72000 || *rows[0].TenorDays != 2 {
		t.Fatalf("行0 映射错: %+v", rows[0])
	}
	if rows[1].DeliveryTs != nil || *rows[1].TenorDays != 7 || *rows[1].Strike != 73000 {
		t.Fatalf("行1 回退键映射错: %+v", rows[1])
	}
}

func TestD183_ParseGateDCI(t *testing.T) {
	now := time.Now().Unix()
	body := []byte(`[
		{"id":4248518,"instrument_name":"BTC 75000","type":"call","invest_currency":"USDT","exercise_currency":"BTC","exercise_price":75000,"delivery_time":` +
		strconv.FormatInt(now+3*86400, 10) + `,"status":"ONGOING","apy_display":"0.5211"},
		{"id":9,"instrument_name":"FLOKI","type":"put","invest_currency":"USDT","exercise_currency":"FLOKI","exercise_price":0.00002,"delivery_time":` +
		strconv.FormatInt(now+86400, 10) + `,"status":"ONGOING","apy_display":"1.06"},
		{"id":10,"instrument_name":"ETH 4000","type":"put","invest_currency":"ETH","exercise_currency":"ETH","exercise_price":4000,"delivery_time":0,"status":"ONGOING","apy_display":"0.33"}]`)
	rows, err := parseGateDCI(body, time.Unix(now, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应只留 BTC/ETH（FLOKI 滤出）, got %d", len(rows))
	}
	r := rows[0]
	if r.Venue != "gate" || r.OptType != "CALL" || *r.Strike != 75000 || *r.TenorDays != 3 || r.APY != 0.5211 {
		t.Fatalf("gate 行0 映射错: %+v", r)
	}
	if rows[1].DeliveryTs != nil {
		t.Fatalf("delivery_time=0 应留 NULL: %+v", rows[1].DeliveryTs)
	}
}

// TestD183_CollectIsolation 无凭据装配→三所各自报错但聚合不炸、返回空集（隔离锚）。
func TestD183_CollectIsolation(t *testing.T) {
	c := &EarnCollector{client: httpx.Client()}
	gateDualURL0 := gateDualURL
	gateDualURL = "http://127.0.0.1:1/none" // 强制 gate 失败
	defer func() { gateDualURL = gateDualURL0 }()
	out := c.CollectDCI(context.Background())
	if len(out) != 0 {
		t.Fatalf("全败应空集不 panic, got %d", len(out))
	}
}
