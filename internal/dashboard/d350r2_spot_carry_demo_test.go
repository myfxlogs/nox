// d350r2_spot_carry_demo_test.go —— D-350R2 T-R2①（unit，fake）：demo 在册三分支收口——
// 终态必 demo 行。红证：基线无 registerSpotCarryDemoRow（编译红）+变异删 mark 调用 fresh 分支红。
package dashboard

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

// fakeDemoPaperStore demoPaperStore 三方法 fake（insertErr/promoteErr 驱动分支）。
type fakeDemoPaperStore struct {
	insertErr   error
	promoteErr  error
	inserted    *pgstore.PaperSpotCarry
	promoteArgs [2]string
	promoted    bool
	markID      int64
	markNote    string
	marked      bool
}

func (f *fakeDemoPaperStore) InsertPaperSpotCarry(_ context.Context, r *pgstore.PaperSpotCarry) error {
	f.inserted = r
	if f.insertErr != nil {
		return f.insertErr
	}
	r.ID, r.Status = 101, "active" // 复刻 pgstore 行为：Insert 硬编码 status='active'
	return nil
}

func (f *fakeDemoPaperStore) PromotePaperToDemo(_ context.Context, venue, symbol string, r *pgstore.PaperSpotCarry) error {
	f.promoted = true
	f.promoteArgs = [2]string{venue, symbol}
	if f.promoteErr != nil {
		return f.promoteErr
	}
	r.ID, r.Status = 202, "demo"
	return nil
}

func (f *fakeDemoPaperStore) MarkPaperSpotCarryDemo(_ context.Context, id int64, note string) error {
	f.marked, f.markID, f.markNote = true, id, note
	return nil
}

// TestD350R2_RegisterFreshBranch fresh（无 active/demo 行）：Insert 成功后必须立即 Mark demo 化
// （终态 demo+note 落）——缺 Mark 则行以 active 纸面行存在（ListSpotCarryDemo 漏+纸面自动退出
// 误作用+实际 qty 污染纸面账）。
func TestD350R2_RegisterFreshBranch(t *testing.T) {
	f := &fakeDemoPaperStore{}
	pr := &pgstore.PaperSpotCarry{Venue: "binance", Symbol: "GUSDT", Side: "short_perp", QtyUSD: 1000}
	if err := registerSpotCarryDemoRow(context.Background(), f, "binance", "GUSDT", "spot=o1@1.5 perp=o2@1.6", pr); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if f.inserted != pr {
		t.Fatal("Insert 未收 pr")
	}
	if !f.marked || f.markID != 101 || f.markNote != "spot=o1@1.5 perp=o2@1.6" {
		t.Errorf("fresh 分支 demo 化缺失: marked=%v id=%d note=%q（红=行态 active 纸面行）", f.marked, f.markID, f.markNote)
	}
	if f.promoted {
		t.Error("fresh 分支不应走 promote")
	}
}

// TestD350R2_RegisterActiveBranch active 行在：Insert 撞 ErrPaperExists → Promote（现行为保留）。
func TestD350R2_RegisterActiveBranch(t *testing.T) {
	f := &fakeDemoPaperStore{insertErr: pgstore.ErrPaperExists}
	pr := &pgstore.PaperSpotCarry{Venue: "binance", Symbol: "GUSDT", Side: "long_perp", QtyUSD: 1000}
	if err := registerSpotCarryDemoRow(context.Background(), f, "binance", "GUSDT", "note", pr); err != nil {
		t.Fatalf("active: %v", err)
	}
	if !f.promoted || f.promoteArgs != [2]string{"binance", "GUSDT"} {
		t.Errorf("promote 未调: promoted=%v args=%v", f.promoted, f.promoteArgs)
	}
	if f.marked {
		t.Error("active 分支不应 mark（promote 已落 demo 行）")
	}
}

// TestD350R2_RegisterDemoBranch demo 行在：Promote 报 ErrPaperPromoteNoActive →
// errDemoRowExists 透传（RPC 映射 already_exists，现行为保留）。
func TestD350R2_RegisterDemoBranch(t *testing.T) {
	f := &fakeDemoPaperStore{insertErr: pgstore.ErrPaperExists, promoteErr: pgstore.ErrPaperPromoteNoActive}
	err := registerSpotCarryDemoRow(context.Background(), f, "binance", "GUSDT", "note",
		&pgstore.PaperSpotCarry{Venue: "binance", Symbol: "GUSDT", Side: "short_perp"})
	if err != errDemoRowExists {
		t.Errorf("demo 分支 err = %v, want errDemoRowExists（透传）", err)
	}
}
