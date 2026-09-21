// cex_io_test.go —— D-365 S5：进出账采集器锚（fake server 双端点映射+水位增量+sim 过滤）。
package exchange

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// fakeIOSink 落库/水位 fake。
type fakeIOSink struct {
	inserted []pgstore.VenueIO
	wm       map[string]time.Time // "configID:direction" → 水位
}

func (f *fakeIOSink) InsertVenueIO(_ context.Context, rows []pgstore.VenueIO) (int64, error) {
	f.inserted = append(f.inserted, rows...)
	return int64(len(rows)), nil
}
func (f *fakeIOSink) LatestVenueIOTs(_ context.Context, configID int64, direction string) (time.Time, bool, error) {
	t, ok := f.wm[itoa(int(configID))+":"+direction]
	return t, ok, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// TestCexIOCollector_Binance D-365 S5：fake hisrec/withdraw 双端点 → 行映射
// （id→external_id、insertTime/applyTime→ts、transactionFee→fee、status 原样）。
func TestCexIOCollector_Binance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sapi/v1/capital/deposit/hisrec":
			fmt.Fprint(w, `[{"id":"5232145709440380673","amount":"22000","coin":"USDT","network":"TRX","status":1,"txId":"0xdep","insertTime":1789818586000}]`)
		case "/sapi/v1/capital/withdraw/history":
			fmt.Fprint(w, `[{"id":"999","amount":"379.0191","transactionFee":"1.5","coin":"TRX","status":6,"txId":"0xwd","applyTime":1789904986000}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sink := &fakeIOSink{}
	col := NewCexIOCollector(sink, func() []CexIOAccount {
		return []CexIOAccount{{ID: 20, Venue: "binance", APIKey: "k", APISecret: "s", BaseURL: srv.URL + "/api"}}
	})
	n, err := col.RunOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("RunOnce = %d (err=%v), want 2", n, err)
	}
	var dep, wd *pgstore.VenueIO
	for i := range sink.inserted {
		if sink.inserted[i].Direction == "deposit" {
			dep = &sink.inserted[i]
		} else {
			wd = &sink.inserted[i]
		}
	}
	if dep == nil || dep.ExternalID != "5232145709440380673" || dep.Amount != 22000 || dep.Status != "1" {
		t.Errorf("deposit row = %+v", dep)
	}
	if dep != nil && dep.Ts.UnixMilli() != 1789818586000 {
		t.Errorf("deposit ts ms = %v", dep.Ts.UnixMilli())
	}
	if wd == nil || wd.ExternalID != "999" || wd.Fee == nil || *wd.Fee != 1.5 || wd.Status != "6" {
		t.Errorf("withdraw row = %+v", wd)
	}
}

// TestCexIOCollector_OkxWatermarkFilter D-363 同源 okx 直连：行 ts 旧于水位被客户端滤除；
// wdId 为提现 external_id（核证点④）。
func TestCexIOCollector_OkxWatermarkFilter(t *testing.T) {
	wm := time.UnixMilli(1789600000000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/asset/deposit-history":
			fmt.Fprint(w, `{"code":"0","data":[{"depId":"431879636","amt":"400","ccy":"USDT","state":"2","ts":"1789604266000","txId":"0xa"}]}`)
		case "/api/v5/asset/withdrawal-history":
			fmt.Fprint(w, `{"code":"0","data":[{"wdId":"430840820","amt":"3500","ccy":"USDT","fee":"0.0006","state":"2","ts":"1789600000000","txId":"0xb"},{"wdId":"430840821","amt":"1","ccy":"USDT","fee":"0.0006","state":"2","ts":"1789609999000","txId":"0xc"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sink := &fakeIOSink{wm: map[string]time.Time{"30:withdraw": wm}}
	col := NewCexIOCollector(sink, func() []CexIOAccount {
		return []CexIOAccount{{ID: 30, Venue: "okx", APIKey: "k", APISecret: "s", Passphrase: "p", BaseURL: srv.URL}}
	})
	n, err := col.RunOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("RunOnce = %d (err=%v), want 2（deposit 全收+withdraw 仅新行）", n, err)
	}
	for _, r := range sink.inserted {
		if r.Direction == "withdraw" && r.ExternalID == "430840820" {
			t.Error("旧于水位的 withdraw 行应被客户端过滤（430840820 ts=水位）")
		}
		if r.Direction == "withdraw" && r.ExternalID == "430840821" && (r.Fee == nil || *r.Fee != 0.0006) {
			t.Errorf("withdraw fee = %v, want 0.0006", r.Fee)
		}
	}
}

// TestFilterIOAccounts D-365 S5：sim 配置与无凭据行不进采集列表（real-only）；okx 直连基址注入。
func TestFilterIOAccounts(t *testing.T) {
	cfgs := []pgstore.ExchangeConfig{
		{ID: 1, Venue: "binance", VenueType: "spot", APIKey: "k", APISecret: "s", Simulated: false},
		{ID: 2, Venue: "binance", VenueType: "perp", APIKey: "k2", APISecret: "s2", Simulated: true},
		{ID: 3, Venue: "gate", VenueType: "perp", APIKey: "k3", APISecret: "s3", Simulated: false},
		{ID: 4, Venue: "okx", VenueType: "spot", APIKey: "", APISecret: "", Simulated: false},
		{ID: 5, Venue: "okx", VenueType: "spot", APIKey: "k5", APISecret: "s5", Simulated: false},
	}
	accs := FilterIOAccounts(cfgs)
	if len(accs) != 2 {
		t.Fatalf("accs = %d, want 2（sim/gate/无凭据全滤）: %+v", len(accs), accs)
	}
	if accs[1].Venue == "okx" && accs[1].BaseURL != "https://www.okx.com" {
		t.Errorf("okx 基址应直连 www.okx.com（核证点④）, got %q", accs[1].BaseURL)
	}
}
