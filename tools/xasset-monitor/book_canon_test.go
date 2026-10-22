// book_canon_test.go —— S2 方言 canonical 病灶锚：组合 canonical=venue 方言形
// （XAG_USDT/NAS100_USDT/XAG-USDT-SWAP）产 pair 左段=归一形（XAGUSDT/...）
// ——与 ticks/消费侧联查键同域（verbatim 键=零行假象族回归防）。
package main

import "testing"

func TestDerivePair_DialectCanonNormalized(t *testing.T) {
	combos := []allowComboRow{
		{"NAS100_USDT", "gate:perp", "NAS100", "15032313:mt5"},
		{"XAG-USDT-SWAP", "okx:perp", "XAG", "3001157:mt5"},
	}
	uni := []universeRow{
		{"gate", "NAS100_USDT", "NAS100"},
		{"okx", "XAG-USDT-SWAP", "XAG"},
	}
	accts := []mtAcct{{venue: "15032313", vtype: "mt5"}, {venue: "3001157", vtype: "mt5"}}
	caps := map[string]map[string]bool{
		"15032313": {"NAS100": true, "XAG": true},
		"3001157":  {"XAG": true},
	}
	pairs, _ := deriveMonitorPairs(combos, uni, nil, accts, nil, caps)
	if !capVenueHas(pairs, "15032313:mt5", "NAS100USDT-NAS100") {
		t.Fatalf("gate 方言组合应产归一 pair NAS100USDT-NAS100, pairs=%v", pairs)
	}
	if !capVenueHas(pairs, "3001157:mt5", "XAGUSDT-XAG") {
		t.Fatalf("okx 方言组合应产归一 pair XAGUSDT-XAG, pairs=%v", pairs)
	}
}
