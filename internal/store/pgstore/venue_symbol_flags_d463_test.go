// venue_symbol_flags_d463_test.go D-463 T1：fin_venue_symbol_flags CRUD 真库测。
package pgstore

import (
	"context"
	"testing"
)

func TestVenueSymbolFlags_D463(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_venue_symbol_flags"); err != nil {
		t.Fatal(err)
	}
	kb := "条目/xpd-funding-semantics.md"
	f := &VenueSymbolFlag{Venue: "gate", Symbol: "XPD", Level: "warn", Reason: "funding 字段语义坑待查", KbRef: &kb}
	if err := s.InsertFlag(ctx, f); err != nil {
		t.Fatal(err)
	}
	if f.ID == 0 {
		t.Fatal("insert 未回填 id")
	}
	b := &VenueSymbolFlag{Venue: "gate", Symbol: "XPT", Level: "blocked", Reason: "blocked 级行"}
	if err := s.InsertFlag(ctx, b); err != nil {
		t.Fatal(err)
	}
	flags, err := s.ListActiveFlags(ctx)
	if err != nil || len(flags) != 2 {
		t.Fatalf("flags=%v err=%v", flags, err)
	}
	if flags[FlagKey("gate", "XPD")].Level != "warn" || flags[FlagKey("gate", "XPD")].KbRef == nil {
		t.Fatalf("flag row=%+v", flags[FlagKey("gate", "XPD")])
	}
	// level CHECK 约束：非法级拒插。
	if err := s.InsertFlag(ctx, &VenueSymbolFlag{Venue: "x", Symbol: "y", Level: "info", Reason: "r"}); err == nil {
		t.Fatal("非法 level 应被 CHECK 拒")
	}
	// resolve 软删：resolved 行不再返回。
	if err := s.ResolveFlag(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	flags, _ = s.ListActiveFlags(ctx)
	if len(flags) != 1 {
		t.Fatalf("resolved 后 flags=%v", flags)
	}
	// 幂等/守卫：重复 resolve 报错；不存在 id 报错。
	if err := s.ResolveFlag(ctx, b.ID); err == nil {
		t.Fatal("重复 resolve 应报错")
	}
	if err := s.ResolveFlag(ctx, 999999); err == nil {
		t.Fatal("不存在 id resolve 应报错")
	}
	// flag_warn 列：InsertCarryGraduation 落列 + List/Get 回读（paper_id FK→先插纸面行）。
	if _, err := s.pool.Exec(ctx, "TRUNCATE carry_graduations, paper_spot_carry CASCADE"); err != nil {
		t.Fatal(err)
	}
	p := &PaperSpotCarry{Venue: "gate", Symbol: "XPD", Side: "short_perp", EntryFundingAnn: 5, QtyUSD: 100}
	if err := s.InsertPaperSpotCarry(ctx, p); err != nil {
		t.Fatal(err)
	}
	warn := "⚠funding 语义待查"
	g := &CarryGraduation{PaperID: p.ID, Venue: "gate", Symbol: "XPD", Structure: "spot_carry",
		AgeDays: 6, AccruedUSD: 3, FlagWarn: &warn}
	if _, err := s.InsertCarryGraduation(ctx, g); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCarryGraduation(ctx, g.ID)
	if err != nil || got.FlagWarn == nil || *got.FlagWarn != warn {
		t.Fatalf("flag_warn=%v err=%v", got.FlagWarn, err)
	}
}
