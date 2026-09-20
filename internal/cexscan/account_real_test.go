// account_real_test.go —— D-360 S5：real 判定纯函数锚（cex=!Simulated / cfd=is_real 键集 /
// 未知 type 丢）+ ScanAccounts 透传 AssetType/Simulated 两键（映射曾丢弃实证）。
package cexscan

import (
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// TestIsRealAccount D-360 S5 判定锚：五态全覆盖。
func TestIsRealAccount(t *testing.T) {
	realKeys := map[string]bool{"3001157": true, "1279967": true}
	cases := []struct {
		name      string
		assetType string
		simulated bool
		venue     string
		want      bool
	}{
		{"cex+sim → 丢（v2 原生键，gate/binance-perp sim 实证）", "cex", true, "gate", false},
		{"cex+real → 留", "cex", false, "okx", true},
		{"cfd+is_real → 留", "cfd", false, "3001157", true},
		{"cfd+demo → 丢", "cfd", false, "52993526", false},
		{"未知 type → 丢", "paper", false, "x", false},
		{"cfd 键集 nil → fail-closed 丢", "cfd", false, "3001157", false},
	}
	for _, c := range cases {
		keys := realKeys
		if c.name == "cfd 键集 nil → fail-closed 丢" {
			keys = nil
		}
		if got := IsRealAccount(c.assetType, c.simulated, c.venue, keys); got != c.want {
			t.Errorf("%s: IsRealAccount(%q,%v,%q) = %v, want %v", c.name, c.assetType, c.simulated, c.venue, got, c.want)
		}
	}
}

// TestScanAccounts_RealKeysPassthrough D-360 S1：AssetType/Simulated 透传（不再丢弃）。
func TestScanAccounts_RealKeysPassthrough(t *testing.T) {
	s := &fakeSnapshotter{
		infos: []*v1.AccountInfo{
			{Venue: "gate", VenueType: "perp", AssetType: "cex", Simulated: true},
			{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Simulated: false},
		},
	}
	snaps := ScanAccounts(s)
	if len(snaps) != 2 {
		t.Fatalf("got %d snaps, want 2", len(snaps))
	}
	if snaps[0].AssetType != "cex" || !snaps[0].Simulated {
		t.Errorf("snap[0] AssetType/Simulated = %q/%v, want cex/true", snaps[0].AssetType, snaps[0].Simulated)
	}
	if snaps[1].AssetType != "cfd" || snaps[1].Simulated {
		t.Errorf("snap[1] AssetType/Simulated = %q/%v, want cfd/false", snaps[1].AssetType, snaps[1].Simulated)
	}
}
