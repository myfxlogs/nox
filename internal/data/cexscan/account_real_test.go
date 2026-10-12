// account_real_test.go —— D-360 S5：real 判定纯函数锚（cex=!Simulated / cfd=is_real 键集 /
// 未知 type 丢）+ ScanAccounts 透传 AssetType/Simulated 两键（映射曾丢弃实证）。
package cexscan

import (
	"testing"

	v1 "arb/internal/gen/dashboardv1"
)

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
