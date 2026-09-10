// D-271 S3 测试：账户扫描。
package cexscan

import (
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// fakeSnapshotter mock AccountSnapshotter。
type fakeSnapshotter struct {
	infos []*v1.AccountInfo
}

func (f *fakeSnapshotter) Snapshot() []*v1.AccountInfo {
	return f.infos
}

// TestScanAccounts 映射 AccountInfo → AccountSnapshot。
func TestScanAccounts(t *testing.T) {
	s := &fakeSnapshotter{
		infos: []*v1.AccountInfo{
			{Venue: "binance", VenueType: "spot", Balance: 1000, Equity: 1100, FreeMargin: 500},
			{Venue: "okx", VenueType: "perp", Balance: 2000, Equity: 2100, FreeMargin: 800},
		},
	}
	snaps := ScanAccounts(s)
	if len(snaps) != 2 {
		t.Fatalf("got %d snaps, want 2", len(snaps))
	}
	if snaps[0].Venue != "binance" || snaps[0].Balance != 1000 {
		t.Errorf("snap[0] = %+v, want binance/1000", snaps[0])
	}
	if snaps[1].Venue != "okx" || snaps[1].Equity != 2100 {
		t.Errorf("snap[1] = %+v, want okx/2100", snaps[1])
	}
}

// TestScanAccounts_NilInfo nil 条目跳过。
func TestScanAccounts_NilInfo(t *testing.T) {
	s := &fakeSnapshotter{
		infos: []*v1.AccountInfo{nil, {Venue: "binance", VenueType: "spot", Balance: 100}},
	}
	snaps := ScanAccounts(s)
	if len(snaps) != 1 {
		t.Fatalf("got %d snaps, want 1 (nil skipped)", len(snaps))
	}
}

// TestScanAccounts_Empty 空快照返回空切片。
func TestScanAccounts_Empty(t *testing.T) {
	s := &fakeSnapshotter{}
	snaps := ScanAccounts(s)
	if len(snaps) != 0 {
		t.Fatalf("got %d snaps, want 0", len(snaps))
	}
}
