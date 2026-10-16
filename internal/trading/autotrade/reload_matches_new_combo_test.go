// d403_reload_test.go —— D-403 S6/S3：白名单热重载（插行→reload→Match 命中，免重启）+ race ×3
// （handleOpportunity 读侧 atomic Load vs approve 写侧 Store 的并发竞态锚，SSOT 强制）。
package autotrade

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

func newReloadEngine(t *testing.T) *Engine {
	t.Helper()
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// D-584 venue 退役守卫：reload 前 RetireCombosMissingVenue 摘牌双簿消失组合——
	// 测试 venue 须先在 broker_accounts/exchange_configs 落簿否则组合被自动退役。
	for _, q := range []string{
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) SELECT '1279967','mt5','t',0,'t' WHERE NOT EXISTS (SELECT 1 FROM broker_accounts WHERE venue='1279967')`,
		`INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret) SELECT 'gate','perp','t','t' WHERE NOT EXISTS (SELECT 1 FROM exchange_configs WHERE venue='gate')`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("seed venue book: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`)
	})
	return New(gate.Config{Environment: "real"}, st, nil, nil, nil)
}

// TestD403_Reload_MatchesNewCombo 端到端：初始空名单 → 插行 → reload → Match 命中（验收锚 3）。
func TestD403_Reload_MatchesNewCombo(t *testing.T) {
	e := newReloadEngine(t)
	ctx := context.Background()
	// 初始空：不命中
	if exec.MatchXAssetComboAllow(e.xassetSpecs(), "BTCUSDT", "BTCUSD", "gate:perp", "1279967:mt5") != nil {
		t.Fatal("初始空名单不应命中")
	}
	insertCombo(t, e, &pgstore.XAssetComboInsert{
		CanonicalBuy: "BTCUSDT", CanonicalSell: "BTCUSD", VenueBuy: "gate:perp", VenueSell: "1279967:mt5",
		MaxLongQty: 1000, MaxShortQty: 0.01, MaxGrossUSD: 5000, Simulated: false, Note: "D-403毕业 paper#42",
	})
	if err := e.ReloadXAssetAllowCombos(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	spec := exec.MatchXAssetComboAllow(e.xassetSpecs(), "BTCUSDT", "BTCUSD", "gate:perp", "1279967:mt5")
	if spec == nil {
		t.Fatal("reload 后应命中新 combo（免重启）")
	}
	if spec.MaxLongQty != 1000 || spec.MaxShortQty != 0.01 {
		t.Errorf("spec caps: %+v", spec)
	}
	// simulated=false 才入 real 名单；demo 加载不得命中
	if exec.MatchXAssetComboAllow(func() []exec.XAssetComboAllowSpec {
		rows, err := e.store.LoadXAssetAllowCombos(ctx, "demo")
		if err != nil {
			t.Fatal(err)
		}
		specs, _ := exec.XAssetSpecsFromRows(rows)
		return specs
	}(), "BTCUSDT", "BTCUSD", "gate:perp", "1279967:mt5") != nil {
		t.Error("simulated=false 行不应进 demo 名单")
	}
}

// TestD403_Reload_Race race 锚（go test -race 下运行）：并发 reload 写 vs specs 读+Match，
// 零 DATA RACE 即过（SSOT race ×3 由机检外层 -count 循环保证）。
func TestD403_Reload_Race(t *testing.T) {
	e := newReloadEngine(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		insertCombo(t, e, &pgstore.XAssetComboInsert{
			CanonicalBuy: "XAUUSDT", CanonicalSell: fmt.Sprintf("BTCUSD%02d", i), VenueBuy: "gate:perp", VenueSell: "1279967:mt5",
			MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 10, Simulated: false,
		})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // 读侧：模拟 handleOpportunity 每机会 Match
		defer wg.Done()
		for j := 0; j < 200; j++ {
			_ = exec.MatchXAssetComboAllow(e.xassetSpecs(), "XAUUSDT", "BTCUSD", "gate:perp", "1279967:mt5")
		}
	}()
	go func() { // 写侧：模拟 approve 后 reload（全表重读+atomic 整体替换）
		defer wg.Done()
		for j := 0; j < 20; j++ {
			if err := e.ReloadXAssetAllowCombos(ctx); err != nil {
				t.Errorf("reload: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// insertCombo 测试插白名单行（引擎同包直用 e.store）。
func insertCombo(t *testing.T, e *Engine, r *pgstore.XAssetComboInsert) {
	t.Helper()
	if _, err := e.store.InsertXAssetAllowCombo(context.Background(), r); err != nil {
		t.Fatalf("insert combo: %v", err)
	}
}
