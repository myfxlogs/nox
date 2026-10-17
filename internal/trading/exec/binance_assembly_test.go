package exec

// binance_assembly_test.go —— binance 装配链锚（D-549 三所补齐）：
// earn 活期赎回→spot 重读→MAIN_UMFUTURE 划转序列+额=min(缺口,可动源) 断言。
import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBinanceAssembleMargin_Sequence(t *testing.T) {
	var seq []string
	spotFree := "300"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/account":
			seq = append(seq, "spotRead")
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"` + spotFree + `"}]}`))
		case r.URL.Path == "/sapi/v1/simple-earn/flexible/position":
			seq = append(seq, "earnPos")
			_, _ = w.Write([]byte(`{"rows":[{"productId":"USDTFLX","totalAmount":"500"}]}`))
		case r.URL.Path == "/sapi/v1/simple-earn/flexible/redeem" && r.Method == http.MethodPost:
			seq = append(seq, "redeem:"+r.FormValue("amount"))
			spotFree = "800" // 赎回落 spot（实读才认）
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.URL.Path == "/sapi/v1/asset/transfer" && r.Method == http.MethodPost:
			seq = append(seq, "xfer:"+r.FormValue("type")+":"+r.FormValue("amount"))
			_, _ = w.Write([]byte(`{"tranId":1}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL+"/fapi", false)
	got, err := e.binanceAssembleMargin(context.Background(), 1000)
	if err != nil || got != 800 {
		t.Fatalf("got=%v err=%v, want 800（min(缺口1000, spot重读800)）", got, err)
	}
	want := []string{"spotRead", "earnPos", "redeem:500.00000000", "spotRead", "xfer:MAIN_UMFUTURE:800.00000000"}
	if len(seq) != len(want) {
		t.Fatalf("seq=%v", seq)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("seq[%d]=%v want %v", i, seq[i], want[i])
		}
	}
}

func TestBinanceAssembleMargin_FailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"100"}]}`))
		case "/sapi/v1/simple-earn/flexible/position":
			w.WriteHeader(500) // earn 可赎额读败→fail-closed 不赎不划
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL+"/fapi", false)
	if _, err := e.binanceAssembleMargin(context.Background(), 500); err == nil {
		t.Fatal("earn 读败应 error（fail-closed）")
	}
}
