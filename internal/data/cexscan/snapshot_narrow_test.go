// snapshot_narrow_test.go —— 3d-P1#1c：账户快照周期补写整体退役（原 D-004 P1#1 收窄用例
// 随之改形）；本文件锚「boot 水合=全量单写口」（快照表=历史记录，实时源=AccountMonitor）。
package cexscan

import "testing"

func snapSet() []AccountSnapshot {
	return []AccountSnapshot{
		{Venue: "okx", VenueType: "spot", AssetType: "cex", Balance: 100},
		{Venue: "okx", VenueType: "perp", AssetType: "cex", Balance: 100},
		{Venue: "binance", VenueType: "perp", AssetType: "cex", Balance: 200},
		{Venue: "gate", VenueType: "perp", AssetType: "cex", Balance: 300},
		{Venue: "okx", VenueType: "funding", AssetType: "cex", Balance: 50},
		{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Balance: 400},
	}
}

func keys(rowsLen int, at func(i int) (string, string)) map[string]bool {
	m := make(map[string]bool, rowsLen)
	for i := 0; i < rowsLen; i++ {
		v, vt := at(i)
		m[v+"/"+vt] = true
	}
	return m
}

// TestHydrateRows_BootOnly #1c：hydrate 唯一写口=全量水合（buildVenueSnapshotRows）——
// unified（okx spot/perp 归一去重）+ binance:perp + gate:perp + okx:funding + cfd；
// 无流键不再有周期写口（资金页打开 → AccountMonitor.Kick 按需回读）。
func TestHydrateRows_BootOnly(t *testing.T) {
	realKeys := map[string]bool{"3001157": true}
	rows := buildVenueSnapshotRows(snapSet(), realKeys)
	got := keys(len(rows), func(i int) (string, string) { return rows[i].Venue, rows[i].VenueType })
	want := map[string]bool{
		"okx/unified": true, "binance/perp": true, "gate/perp": true,
		"okx/funding": true, "3001157/mt5": true,
	}
	if len(got) != len(want) {
		t.Fatalf("hydrate rows = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s in %v", k, got)
		}
	}
}
