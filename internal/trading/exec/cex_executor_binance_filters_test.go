package exec

// cex_executor_binance_filters_test.go —— D-587 坑1 验收锚：/v1/symbolConfig
// 官方返回 JSON 数组 [{...}]（生产实证 "cannot unmarshal array into Go value of type
// map"），解码面必须与契约同形。
import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBinanceMaxNotional_ArrayResponse(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/symbolConfig" {
			_, _ = io.WriteString(w, body)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)

	body = `[{"symbol":"TRXUSDT","maxNotionalValue":"250000"}]`
	got, err := e.binanceMaxNotional(context.Background(), "TRXUSDT")
	if err != nil {
		t.Fatalf("数组响应应解码成功: %v", err)
	}
	if got != 250000 {
		t.Fatalf("maxNotionalValue=%v, want 250000", got)
	}

	body = `[]`
	got, err = e.binanceMaxNotional(context.Background(), "TRXUSDT")
	if err != nil {
		t.Fatalf("空数组不应报错: %v", err)
	}
	if got != 0 {
		t.Fatalf("空数组应返 0（未采不拦）, got %v", got)
	}
}
