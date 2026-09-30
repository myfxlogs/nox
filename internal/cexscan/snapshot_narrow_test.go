// snapshot_narrow_test.go —— D-004 P1#1 收窄：有私有流写口的键退出周期快照，无流键保留。
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

// TestSnapshotRows_Narrow full=false 只落无流键：okx:funding；
// 有流键（okx spot/perp→unified、binance:perp、gate:perp）与 cfd/MT 键（3d-P1#1b
// 余额帧事件写口 src=mt_frame）退出周期写。
func TestSnapshotRows_Narrow(t *testing.T) {
	realKeys := map[string]bool{"3001157": true}
	rows := snapshotRowsFor(snapSet(), realKeys, false)
	got := keys(len(rows), func(i int) (string, string) { return rows[i].Venue, rows[i].VenueType })
	want := map[string]bool{"okx/funding": true}
	if len(got) != len(want) {
		t.Fatalf("narrow rows = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s in %v", k, got)
		}
	}
}

// TestSnapshotRows_Full full=true 全量水合：unified（okx spot/perp 归一去重）+
// binance:perp + gate:perp + okx:funding + cfd。
func TestSnapshotRows_Full(t *testing.T) {
	realKeys := map[string]bool{"3001157": true}
	rows := snapshotRowsFor(snapSet(), realKeys, true)
	got := keys(len(rows), func(i int) (string, string) { return rows[i].Venue, rows[i].VenueType })
	want := map[string]bool{
		"okx/unified": true, "binance/perp": true, "gate/perp": true,
		"okx/funding": true, "3001157/mt5": true,
	}
	if len(got) != len(want) {
		t.Fatalf("full rows = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s in %v", k, got)
		}
	}
}
