package exec

import (
	"strings"
	"testing"
)

// TestD508_GateFilledDirtyPrice：gate 订单 fill_price 脏字段 → error 上抛（旧语义静默归零）。
func TestD508_GateFilledDirtyPrice(t *testing.T) {
	if _, _, err := gateFilled(gateOrderResponse{ID: 7, Size: 10, Left: 0, FillPrice: "not-a-number"}); err == nil {
		t.Fatal("脏 fill_price 应 error 上抛")
	} else if !strings.Contains(err.Error(), "fill_price") {
		t.Fatalf("error 应带字段名定位: %v", err)
	}
	qty, price, err := gateFilled(gateOrderResponse{ID: 7, Size: 10, Left: 4, FillPrice: "123.5"})
	if err != nil || qty != 6 || price != 123.5 {
		t.Fatalf("正常解析回归: qty=%v price=%v err=%v", qty, price, err)
	}
}

// TestD508_BinanceAvgPriceDirtyFill：fills 数组 price/qty 脏字段 → error 上抛。
func TestD508_BinanceAvgPriceDirtyFill(t *testing.T) {
	body := map[string]any{"fills": []any{map[string]any{"price": "oops", "qty": "1"}}}
	if _, err := binanceOrderAvgPrice(body, 1); err == nil {
		t.Fatal("脏 fill price 应 error 上抛")
	}
	body = map[string]any{"fills": []any{map[string]any{"price": "100", "qty": "bad"}}}
	if _, err := binanceOrderAvgPrice(body, 1); err == nil {
		t.Fatal("脏 fill qty 应 error 上抛")
	}
}

// TestD508_JsonNumDirty：保证金预检数值帮手——脏 string/未知类型 error 上抛，nil=可选字段缺省 0。
func TestD508_JsonNumDirty(t *testing.T) {
	if _, err := jsonNum("garbage"); err == nil {
		t.Fatal("脏 string 应 error 上抛")
	}
	if _, err := jsonNum(struct{}{}); err == nil {
		t.Fatal("未知类型应 error 上抛")
	}
	if v, err := jsonNum(nil); err != nil || v != 0 {
		t.Fatalf("nil 可选字段缺省语义: v=%v err=%v", v, err)
	}
	if v, err := jsonNum("1.5"); err != nil || v != 1.5 {
		t.Fatalf("正常解析回归: v=%v err=%v", v, err)
	}
}
