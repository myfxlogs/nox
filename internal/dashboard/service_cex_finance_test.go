// service_cex_finance_test.go —— D-365-R1 S1/S3 锚：FilterIOAccounts 同 key 去重与
// okx 交易卡合并（纯函数，无 DB）。
package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/collect/exchange"
	"arb/internal/store/pgstore"
)

// TestFilterIOAccounts_SameKeyDedup R1 S1 锚：binance 3+okx 4=7 配置同两把 key →
// 单账户输出×2（canonical=min id）；不同 key 另立。
func TestFilterIOAccounts_SameKeyDedup(t *testing.T) {
	cfgs := []pgstore.ExchangeConfig{
		{ID: 30, Venue: "binance", VenueType: "perp", APIKey: "key-bn", APISecret: "s"},
		{ID: 21, Venue: "binance", VenueType: "spot", APIKey: "key-bn", APISecret: "s"},
		{ID: 25, Venue: "binance", VenueType: "funding", APIKey: "key-bn", APISecret: "s"},
		{ID: 40, Venue: "okx", VenueType: "earn", APIKey: "key-ok", APISecret: "s", Passphrase: "p"},
		{ID: 41, Venue: "okx", VenueType: "funding", APIKey: "key-ok", APISecret: "s", Passphrase: "p"},
		{ID: 42, Venue: "okx", VenueType: "perp", APIKey: "key-ok", APISecret: "s", Passphrase: "p"},
		{ID: 43, Venue: "okx", VenueType: "spot", APIKey: "key-ok", APISecret: "s", Passphrase: "p"},
	}
	accs := exchange.FilterIOAccounts(cfgs)
	if len(accs) != 2 {
		t.Fatalf("accs = %d, want 2（binance 3→1、okx 4→1）: %+v", len(accs), accs)
	}
	// canonical=min(id)：binance 21、okx 40
	byVenue := map[string]int64{}
	for _, a := range accs {
		byVenue[a.Venue] = a.ID
	}
	if byVenue["binance"] != 21 || byVenue["okx"] != 40 {
		t.Errorf("canonical ids = %v, want binance 21 / okx 40（min id）", byVenue)
	}
}

// TestDedupOkxTradingConfigs R1 S3 锚：okx spot/perp 合并成一张 unified 卡（首张=min id
// 出块），funding/earn/binance spot/perp 各自独立；sim/无凭据滤除。
func TestDedupOkxTradingConfigs(t *testing.T) {
	cfgs := []pgstore.ExchangeConfig{
		{ID: 42, Venue: "okx", VenueType: "perp", APIKey: "k", APISecret: "s"},
		{ID: 20, Venue: "binance", VenueType: "spot", APIKey: "kb", APISecret: "sb"},
		{ID: 43, Venue: "okx", VenueType: "spot", APIKey: "k", APISecret: "s"},
		{ID: 44, Venue: "okx", VenueType: "funding", APIKey: "k", APISecret: "s"},
		{ID: 45, Venue: "okx", VenueType: "earn", APIKey: "k", APISecret: "s"},
		{ID: 46, Venue: "binance", VenueType: "funding", APIKey: "kb", APISecret: "sb", Simulated: true},
		{ID: 47, Venue: "okx", VenueType: "earn", APIKey: "", APISecret: ""},
	}
	out := dedupOkxTradingConfigs(cfgs)
	// okx trading 合并 1 + okx funding + okx earn + binance spot = 4（binance funding sim 滤/okx earn 无凭据滤）
	if len(out) != 4 {
		t.Fatalf("out = %d, want 4: %+v", len(out), out)
	}
	// okx trading 首张=min id 42（perp），R2 定死：改写 VenueType=unified（分派自然走
	// trading impl，卡键/渲染与 monitor 快照 okx/unified 同名一致）
	if out[0].ID != 20 || out[0].VenueType != "spot" {
		t.Errorf("out[0] = #%d %s, want #20 binance spot（id 升序）", out[0].ID, out[0].VenueType)
	}
	for _, c := range out {
		if c.Venue == "okx" && (c.VenueType == "spot" || c.VenueType == "perp") {
			t.Errorf("okx 交易配置 %d 应已改写 unified, got %q", c.ID, c.VenueType)
		}
	}
	okxUnified := 0
	for _, c := range out {
		if c.Venue == "okx" && c.VenueType == "unified" {
			okxUnified++
		}
	}
	if okxUnified != 1 {
		t.Errorf("okx unified 卡应恰 1 张, got %d", okxUnified)
	}
}

// TestGetCexFinance_DedupWiring R2 S3 service 级锚（非纯函数）：同凭据多配置真走 RPC——
// 断言 Accounts 块数=去重后数（防「纯函数绿但没接线」重演；TTL 缓存测试内清零）。
func TestGetCexFinance_DedupWiring(t *testing.T) {
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE exchange_configs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/account": // base_url 尾段含 /api + 端点 /v3/account → /api/v3/account（生产形状）
			fmt.Fprint(w, `{"balances":[{"asset":"USDT","free":"100","locked":"0"}]}`)
		case r.URL.Path == "/api/v5/account/balance":
			fmt.Fprint(w, `{"code":"0","data":[{"details":[{"ccy":"USDT","eq":"50","cashBal":"50","eqUsd":"50"}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, base_url, simulated)
		VALUES ('binance', 'spot', 'kb', 'sb', $1, false),
		       ('okx', 'spot', 'ko', 'so', $2, false),
		       ('okx', 'perp', 'ko', 'so', $2, false)`, srv.URL+"/api", srv.URL); err != nil {
		t.Fatalf("seed configs: %v", err)
	}

	svc := &Service{store: s, qbus: nil, freshTTL: time.Minute, liqMMR: 0.005}
	cexFinanceMu.Lock()
	cexFinanceCached = nil // TTL 缓存清零（防跨测试串味）
	cexFinanceMu.Unlock()

	resp, err := svc.GetCexFinance(ctx, nil)
	if err != nil {
		t.Fatalf("GetCexFinance: %v", err)
	}
	if len(resp.Msg.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2（binance:spot + okx:unified，同 key okx 合并）: %+v",
			len(resp.Msg.Accounts), resp.Msg.Accounts)
	}
	if resp.Msg.Accounts[1].Key != "okx:unified" || resp.Msg.Accounts[1].VenueType != "unified" {
		t.Errorf("okx 卡 = %s/%s, want okx/unified（与 monitor 快照同名）",
			resp.Msg.Accounts[1].Key, resp.Msg.Accounts[1].VenueType)
	}
}
