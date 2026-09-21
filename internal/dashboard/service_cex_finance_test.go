// service_cex_finance_test.go —— D-365-R1 S1/S3 锚：FilterIOAccounts 同 key 去重与
// okx 交易卡合并（纯函数，无 DB）。
package dashboard

import (
	"testing"

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
	// okx trading 首张=min id 42（perp），保原 VenueType 供 FullInventory 分派
	if out[0].ID != 20 || out[0].VenueType != "spot" {
		t.Errorf("out[0] = #%d %s, want #20 binance spot（id 升序）", out[0].ID, out[0].VenueType)
	}
	okxTrading := 0
	for _, c := range out {
		if c.Venue == "okx" && (c.VenueType == "spot" || c.VenueType == "perp") {
			okxTrading++
		}
	}
	if okxTrading != 1 {
		t.Errorf("okx 交易类应仅 1 张（unified 卡语义）, got %d", okxTrading)
	}
}
