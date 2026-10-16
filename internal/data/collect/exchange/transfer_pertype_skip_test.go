package exchange

// transfer_pertype_skip_test.go —— io transfer 枚举雷病灶回归锚：
// 单型 400（isolated-margin 域外裁定前移出枚举，此处用通用型注入同款雷）
// 不 nuked 整批——其它型+earn 史照常落账。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCexIO_BinanceTransfer_PerTypeSkip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sapi/v1/asset/transfer":
			typ := r.URL.Query().Get("type")
			if typ == "MAIN_CMFUTURE" { // 注入单型雷：400 同款 isolated-margin 病灶
				w.WriteHeader(400)
				fmt.Fprint(w, `{"code":-11014,"msg":"Symbol is null or empty!"}`)
				return
			}
			fmt.Fprintf(w, `{"rows":[{"tranId":%d,"asset":"USDT","amount":"10","status":"CONFIRMED","timestamp":1789600000000}]}`, len(typ))
		case r.URL.Path == "/sapi/v1/simple-earn/flexible/history/subscriptionRecord":
			fmt.Fprint(w, `{"rows":[{"purchaseId":26055,"asset":"USDT","amount":"100","status":"SUCCESS","time":1789600000000}]}`)
		case r.URL.Path == "/sapi/v1/simple-earn/flexible/history/redemptionRecord":
			fmt.Fprint(w, `{"rows":[{"redeemId":26056,"asset":"USDT","amount":"50","status":"SUCCESS","time":1789600000000}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sink := &fakeIOSink{}
	col := NewCexIOCollector(sink, func() []CexIOAccount {
		return []CexIOAccount{{ID: 21, Venue: "binance", APIKey: "k", APISecret: "s", BaseURL: srv.URL + "/api"}}
	})
	n, err := col.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("单型 400 不应弃整批: %v", err)
	}
	if n == 0 {
		t.Fatal("行数=0——fail-fast 病灶复发")
	}
	var seRow, normal bool
	for i := range sink.inserted {
		e := sink.inserted[i]
		if e.ExternalID == "se-26055" || e.ExternalID == "se-26056" {
			seRow = true // earn 史段未灭
		}
		if e.Direction == "transfer" && e.ExternalID != "" && e.ExternalID != "se-26055" && e.ExternalID != "se-26056" {
			normal = true // 正常 transfer 型落账
		}
	}
	if !seRow || !normal {
		t.Fatalf("earn 史与正常型应双落: se=%v normal=%v rows=%v", seRow, normal, sink.inserted)
	}
}
