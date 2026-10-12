package exchange

import "testing"

// !bookTicker 聚合流帧带 {"stream","data"} 包裹层——parseBookTicker
// 下钻 data 后与裸帧同投影；裸帧（spot/逐符号形制）回归不变。
func TestD535_AggWrappedFrame(t *testing.T) {
	wrapped := `{"stream":"!bookTicker","data":{"e":"bookTicker","u":1,"s":"BTCUSDT","b":"65000.5","B":"2.5","a":"65001.0","A":"1.2","T":1700000000000,"E":1700000000001}}`
	f, ok := parseBookTicker([]byte(wrapped))
	if !ok || f.S != "BTCUSDT" || f.B != "65000.5" || f.A != "65001.0" || f.T != 1700000000000 {
		t.Fatalf("包裹帧解析: %+v ok=%v", f, ok)
	}
	bare := `{"e":"bookTicker","u":1,"s":"ETHUSDT","b":"3000.1","B":"1","a":"3000.2","A":"2","T":1700000000000}`
	f2, ok := parseBookTicker([]byte(bare))
	if !ok || f2.S != "ETHUSDT" || f2.B != "3000.1" {
		t.Fatalf("裸帧回归: %+v ok=%v", f2, ok)
	}
	// stream 包裹但 data 缺关键字段 → 不入 QuoteBus
	if _, ok := parseBookTicker([]byte(`{"stream":"!bookTicker","data":{"x":1}}`)); ok {
		t.Fatal("空 data 应判否")
	}
}
