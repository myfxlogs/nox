// derive_test.go —— D-552 S2：键域回归锚（D-496 拆列后裸列 vs 生成键列）。
// 病灶=loadDerivationInputs SELECT 裸列 venue_buy/sell → IsMTKey("3001157")=false
// → mt×cex 组合全判「同所」拒采 → monitor 簿停写 49h。三锚防再回退：
// T1 纯函数契约（裸列必败/键列必成）；T2 真仓 enabled 行全派生+真同所保留；
// T3 DB 层红→绿（查询列名语义=本票回归源，SQL 回退即红）。
package main

import (
	"context"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// d552RealCombos 真仓 xasset_allow_combos enabled 行投影（2026-09-28 生产实拉，
// venue_key 生成列口径=修后 SQL 应供形态；10 行=id1-14 enabled 全集）。
func d552RealCombos() []allowComboRow {
	return []allowComboRow{
		{"CLUSDT", "gate:perp", "WTI", "277842155:mt5"},         // id1：nonDerivable 静默
		{"XAUUSD", "277842155:mt5", "XAUUSDT", "gate:perp"},     // id2
		{"XAUUSD", "277842155:mt5", "XAUUSDT", "binance:perp"},  // id3
		{"XAUUSD", "277842155:mt5", "PAXGUSDT", "binance:perp"}, // id4
		{"BTCUSDT", "3001157:mt5", "BTCUSDT", "okx:perp"},       // id10（真仓五行①）
		{"XAUUSD", "3001157:mt5", "XAUUSDT", "okx:perp"},        // id11（真仓五行②）
		{"XAUUSD", "3001157:mt5", "XAUUSDT", "gate:perp"},       // id12（真仓五行③）
		{"XPTUSD", "3001157:mt5", "XPTUSDT", "gate:perp"},       // id13（真仓五行④）
		{"XPDUSD", "3001157:mt5", "XPDUSDT", "gate:perp"},       // id14（真仓五行⑤）
		{"SKLUSDT", "binance:spot", "SKLUSDT", "binance:perp"},  // 真同所（B-008 正确保留）
	}
}

// TestD552_BareColumnInputFailsKeyedInputDerives T1 纯函数契约：D-496 后裸列
// （venue_buy="3001157"）IsMTKey=false → mt×cex 误判同所派生必败；键列输入必成。
// 本测试双态同跑=SQL 供给契约钉死（谁回退谁负责让 T3 红）。
func TestD552_BareColumnInputFailsKeyedInputDerives(t *testing.T) {
	pairs, uncovered := deriveMonitorPairs(
		[]allowComboRow{{"XAUUSD", "3001157", "XAUUSDT", "gate"}}, d472Universe(), nil, nil, nil)
	for _, p := range pairs {
		if p.cexVenue == "gate:perp" && p.pair == "XAUUSDT-XAU" {
			t.Fatalf("裸列输入不得派生病灶对 gate:perp XAUUSDT-XAU（legacy 三行恒在属正常）, got %+v", pairs)
		}
	}
	bareFlagged := false
	for _, u := range uncovered {
		if strings.Contains(u, "同所") && strings.Contains(u, "XAUUSD") {
			bareFlagged = true
		}
	}
	if !bareFlagged {
		t.Fatalf("裸列输入应落「同所」对账名单（病灶实锤形态）, got %v", uncovered)
	}

	keyed, unc2 := deriveMonitorPairs(
		[]allowComboRow{{"XAUUSD", "3001157:mt5", "XAUUSDT", "gate:perp"}}, d472Universe(), nil, nil, nil)
	var p *monitorPair
	for i := range keyed {
		if keyed[i].cexVenue == "gate:perp" && keyed[i].pair == "XAUUSDT-XAU" {
			p = &keyed[i]
		}
	}
	if p == nil {
		t.Fatalf("键列输入应派生病灶对 gate:perp XAUUSDT-XAU, got %+v", keyed)
	}
	if p.sym != "XAU_USDT" || p.exSym != "XAUUSD" {
		t.Fatalf("键列派生对错: %+v", *p)
	}
	if len(unc2) != 0 {
		t.Fatalf("键列输入不得进 uncovered, got %v", unc2)
	}
}

// TestD552_RealFiveRowsDerive T2 真仓 enabled 行投影（键列口径）：3001157 五组合
// 全部派生（B-008 warn 五行归零）；SKLUSDT 真同所保留；CLUSDT nonDerivable 静默不警告。
func TestD552_RealFiveRowsDerive(t *testing.T) {
	pairs, uncovered := deriveMonitorPairs(d552RealCombos(), d472Universe(), nil, nil, nil)
	got := map[[3]string]bool{}
	for _, p := range pairs {
		got[[3]string{p.cexVenue, p.pair, p.exSym}] = true
	}
	// 真仓五行（cron warn 实锤的五组合）三元组：
	wantFive := [][3]string{
		{"okx:perp", "BTCUSDT-BTC", "BTCUSDT"},
		{"okx:perp", "XAUUSDT-XAU", "XAUUSD"},
		{"gate:perp", "XAUUSDT-XAU", "XAUUSD"},
		{"gate:perp", "XPTUSDT-XPT", "XPTUSD"},
		{"gate:perp", "XPDUSDT-XPD", "XPDUSD"},
	}
	for _, w := range wantFive {
		if !got[w] {
			t.Errorf("真仓组合应派生: %v（缺失=簿继续停写）", w)
		}
	}
	// 277842155 三行同口径派生（id2/3/4，gate/binance 腿）：
	for _, w := range [][3]string{
		{"binance:perp", "PAXGUSDT-XAU", "XAUUSD"},
	} {
		if !got[w] {
			t.Errorf("277842155 组合应派生: %v", w)
		}
	}
	// uncovered：仅 SKLUSDT 真同所一行（CLUSDT 静默、其余全派生）。
	if len(uncovered) != 1 || !strings.Contains(uncovered[0], "同所") || !strings.Contains(uncovered[0], "SKLUSDT") {
		t.Fatalf("uncovered 应仅余 SKLUSDT 真同所行, got %v", uncovered)
	}
}

// TestD552_LoadDerivationInputsDeliversKeyedColumns T3 DB 层红→绿锚（本票回归源）：
// loadDerivationInputs 必须回读 venue_key_* 生成列（D-496 键域权威口径）——
// SELECT 裸列 venue_buy/sell 的历史形态在此直接红（返 "3001157"/"gate"）。
func TestD552_LoadDerivationInputsDeliversKeyedColumns(t *testing.T) {
	s, err := pgstore.New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO xasset_allow_combos
		(canonical_buy, venue_buy, venue_type_buy, canonical_sell, venue_sell, venue_type_sell,
		 max_long_qty, max_short_qty, max_gross_usd, enabled)
		VALUES ('XAUUSD', '3001157', 'mt5', 'XAUUSDT', 'gate', 'perp', 1, 1, 100, true)`); err != nil {
		t.Fatalf("insert combo: %v", err)
	}
	combos, _, _, _, _, err := loadDerivationInputs(ctx, s)
	if err != nil {
		t.Fatalf("loadDerivationInputs: %v", err)
	}
	if len(combos) != 1 {
		t.Fatalf("combos = %d, want 1", len(combos))
	}
	if combos[0].venueBuy != "3001157:mt5" || combos[0].venueSell != "gate:perp" {
		t.Fatalf("派生入参必须为 venue_key 生成列（键域口径）, got venueBuy=%q venueSell=%q —— D-552 裸列回归复现",
			combos[0].venueBuy, combos[0].venueSell)
	}
}

// TestFuturesExcludedFromMonitorUniverse 交割行剔监控簿锚（xasset-monitor 交割符号
// 回归票）：FUTURES 行不进派生入参（ticks 零行→critical 洪泛病灶）；PERP 行照旧收。
func TestFuturesExcludedFromMonitorUniverse(t *testing.T) {
	s, err := pgstore.New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`DELETE FROM fin_cex_universe WHERE symbol LIKE 'XMON%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_cex_universe (venue, symbol, base, status, contract_type) VALUES
		('okx','XMON-USD_UM-270326','XMON','TRADING','FUTURES'),
		('okx','XMON-USDT-SWAP','XMON','TRADING','PERP'),
		('binance','XMONPERPQ','XMON','TRADING',NULL)`); err != nil {
		t.Fatal(err)
	}
	_, uni, _, _, _, err := loadDerivationInputs(ctx, s)
	if err != nil {
		t.Fatalf("loadDerivationInputs: %v", err)
	}
	var fut, perp, nullCt bool
	for _, r := range uni {
		switch r.symbol {
		case "XMON-USD_UM-270326":
			fut = true
		case "XMON-USDT-SWAP":
			perp = true
		case "XMONPERPQ":
			nullCt = true
		}
	}
	if fut {
		t.Fatal("FUTURES 行混入监控簿派生——ticks 零行 critical 洪泛源复现")
	}
	if !perp || !nullCt {
		t.Fatalf("perp/存量 NULL 行应照旧收: perp=%v nullCt=%v", perp, nullCt)
	}
}
