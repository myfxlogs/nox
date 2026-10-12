// d515_swaparb_reload_test.go —— D-515 S5 A6：SQL 行改 → ReloadSwapArbAllowCombos → 快照生效。
// 真实 _test 隔离库；engine 直构（store 注入即可，快照零值→cfg 回退路径一并验）。
package autotrade

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

func TestD515_SwapArbReloadHotRead(t *testing.T) {
	ctx := context.Background()
	st, err := pgstore.New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore new: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM swaparb_allow_combos`); err != nil {
		t.Fatal(err)
	}
	// engine 直构（快照 nil → cfg 回退语义先验）。
	e := &Engine{store: st, cfg: Config{
		SwapArbAllowCombos: nil,
	}}
	if got := e.swapArbAllowList(); len(got) != 0 {
		t.Fatalf("nil snapshot fallback: %v", got)
	}
	// 播种 allow 行 → reload → 快照命中。
	f1, f2 := 0.5, 0.5
	if err := st.InsertSwapArbAllowCombo(ctx, pgstore.SwapArbAllowRow{
		Kind: "allow", Canonical: "XAUUSD", VenueLong: "A:mt5", VenueShort: "B:mt5",
		MaxLongQty: &f1, MaxShortQty: &f2, Note: "a6",
	}); err != nil {
		t.Fatal(err)
	}
	allow, err := e.ReloadSwapArbAllowCombos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allow) != 1 || allow[0].Canonical != "XAUUSD" || allow[0].MaxLongQty != 0.5 {
		t.Fatalf("reload snapshot: %+v", allow)
	}
	// deny 行验证：插一条 deny → reload → 快照含 deny。
	if err := st.InsertSwapArbAllowCombo(ctx, pgstore.SwapArbAllowRow{Kind: "deny", Canonical: "BAD", VenueLong: "x", VenueShort: "y"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ReloadSwapArbAllowCombos(ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.swapArbDenyList()) != 1 || e.swapArbDenyList()[0].Canonical != "BAD" {
		t.Fatalf("deny snapshot: %+v", e.swapArbDenyList())
	}
	// 带外 SQL 改行（UPDATE enabled=false）→ 再 reload → allow 空（热读锚）。
	if _, err := st.Pool().Exec(ctx, `UPDATE swaparb_allow_combos SET enabled=false WHERE kind='allow'`); err != nil {
		t.Fatal(err)
	}
	allow2, err := e.ReloadSwapArbAllowCombos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allow2) != 0 || len(e.swapArbAllowList()) != 0 {
		t.Fatalf("post-disable snapshot should be empty: %v", allow2)
	}
}
