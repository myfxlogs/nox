// gate_univpoll_funding_test.go —— D-665 返修⑤锚：REST 拍行 funding 三字段
// 经 emitFacts 发射（非执行面 perp 断供复供出生实证）。
package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
)

func TestGateUnivPoll_FundingEmit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/spot/tickers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/v4/futures/usdt/tickers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"contract":"XAG_USDT","lowest_ask":"1.2","highest_bid":"1.1","funding_rate":"0.0001","funding_next_apply":1790400000,"funding_interval":28800},{"contract":"NOFUND_USDT","lowest_ask":"1","highest_bid":"0.9"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var got []fact.Fact
	g := &GateWebSocket{cfg: Config{Client: srv.Client(), GateBaseURL: srv.URL}}
	g.emitFacts = func(ctx context.Context, fs []fact.Fact) error {
		got = append(got, fs...)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	g.runGateTickerPoll(ctx, func(bus.Quote) {})
	if len(got) != 2 {
		t.Fatalf("应两 facts（funding+next_settle；NOFUND 缺字段不造）: %v", got)
	}
	if got[0].Kind != fact.KindFunding || got[0].Symbol != "XAGUSDT" || got[0].Src != "gate_rest_futures" {
		t.Fatalf("funding fact 字段不对: %+v", got[0])
	}
	if got[1].Kind != fact.KindFundingNextSettle || got[1].Value != 1790400000 {
		t.Fatalf("next_settle fact 不对: %+v", got[1])
	}
}
