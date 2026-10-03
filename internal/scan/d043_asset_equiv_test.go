// D-043 锚：carry universe join 主键改 asset_class——SILVER×XAGUSDT/BRENT×BZUSDT
// 跨 canonical 同类即 join（long/short 双行，swap-free net=±funding）；aliases
// 退化为无覆盖兜底（XCU→COPPER 回归不变）；class 键行 paper 腿解析同步。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d043Equiv 生产 asset_equiv 实证行（SILVER/BRENT/GOLD 子集）。
func d043Equiv() []pgstore.AssetEquivEntry {
	return []pgstore.AssetEquivEntry{
		{AssetClass: "SILVER", Canonical: "SILVER", VenueType: "mt5", Venue: "3001157"},
		{AssetClass: "SILVER", Canonical: "XAGUSDT", VenueType: "perp", Venue: "okx"},
		{AssetClass: "SILVER", Canonical: "XAGUSDT", VenueType: "perp", Venue: "gate"},
		{AssetClass: "BRENT", Canonical: "BRENT", VenueType: "mt5", Venue: "3001157"},
		{AssetClass: "BRENT", Canonical: "BZUSDT", VenueType: "perp", Venue: "gate"},
		{AssetClass: "GOLD", Canonical: "XAUUSD", VenueType: "mt5", Venue: "3001157"},
		{AssetClass: "GOLD", Canonical: "XAUUSDT", VenueType: "perp", Venue: "okx"},
		{AssetClass: "GOLD", Canonical: "PAXGUSDT", VenueType: "perp", Venue: "okx"},
	}
}

// TestD043_ClassJoin SILVER×XAGUSDT(okx)、BRENT×BZUSDT(gate) class 键 join——
// canonical 字符串相异（SILVER≠XAG、BRENT≠BZ）同类即出 long/short 双行。
func TestD043_ClassJoin(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "okx", Symbol: "XAGUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "funding", Venue: "gate", Symbol: "BZUSDT", Value: 4.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "XAGUSDm", Value: 0, Ts: time.Now()},
			{Kind: "swap_short", Venue: "3001157", Symbol: "XAGUSDm", Value: 0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "BRENTm", Value: 0, Ts: time.Now()},
			{Kind: "swap_short", Venue: "3001157", Symbol: "BRENTm", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XAGUSDm", Canonical: "SILVER", IsPrimary: true},
			{Venue: "3001157", BrokerSymbol: "BRENTm", Canonical: "BRENT", IsPrimary: true},
		},
		equiv: d043Equiv(),
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	type want struct {
		canonical, mt, cex, structure string
		net                           float64
	}
	wants := []want{
		{"SILVER", "3001157", "okx", "long_cfd_short_perp", 5.0},
		{"SILVER", "3001157", "okx", "short_cfd_long_perp", -5.0},
		{"BRENT", "3001157", "gate", "long_cfd_short_perp", 4.0},
		{"BRENT", "3001157", "gate", "short_cfd_long_perp", -4.0},
	}
	got := map[string]UniverseRow{}
	for _, r := range res.Rows {
		got[r.Canonical+"|"+r.MtVenue+"|"+r.CexVenue+"|"+r.Structure] = r
	}
	for _, w := range wants {
		r, ok := got[w.canonical+"|"+w.mt+"|"+w.cex+"|"+w.structure]
		if !ok {
			t.Fatalf("缺行 %v: rows=%+v", w, res.Rows)
		}
		if r.NetAnnual < w.net-0.01 || r.NetAnnual > w.net+0.01 {
			t.Errorf("%s net want ≈%.2f, got %.4f", w.canonical+"/"+w.structure, w.net, r.NetAnnual)
		}
	}
	if len(res.Rows) != 4 {
		t.Errorf("rows=%d want 4（重复行应去重）: %+v", len(res.Rows), res.Rows)
	}
}

// TestD043_SwapFreeClassKeyed swap_free 账户 class 键行 net=±funding——
// 非零 swap 均值被 swap_free 旗强制归零（D-028 ④ 覆写在 class 键行同样生效）。
func TestD043_SwapFreeClassKeyed(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "okx", Symbol: "XAGUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "XAGUSDm", Value: -20, Ts: time.Now()},
			{Kind: "swap_short", Venue: "3001157", Symbol: "XAGUSDm", Value: 20, Ts: time.Now()},
		},
		windowMeans: map[string]float64{
			"swap_long|3001157|XAGUSDm":  -20, // −73%/yr，无旗时 net=5−73=−68
			"swap_short|3001157|XAGUSDm": 20,  // +73%/yr，无旗时 net=73−5=68
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XAGUSDm", Canonical: "SILVER", IsPrimary: true},
		},
		equiv:    d043Equiv(),
		swapFree: map[string]bool{"3001157": true},
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	nets := map[string]float64{}
	for _, r := range res.Rows {
		if r.Canonical == "SILVER" {
			nets[r.Structure] = r.NetAnnual
		}
	}
	if nets["long_cfd_short_perp"] < 4.99 || nets["long_cfd_short_perp"] > 5.01 {
		t.Errorf("swap-free long net want ≈+funding=5, got %.4f", nets["long_cfd_short_perp"])
	}
	if nets["short_cfd_long_perp"] < -5.01 || nets["short_cfd_long_perp"] > -4.99 {
		t.Errorf("swap-free short net want ≈−funding=−5, got %.4f", nets["short_cfd_long_perp"])
	}
}

// TestD043_XCUAliasFallback asset_equiv 无 COPPER 覆盖时 aliases 兜底——
// XCU→COPPER 回归不变（class 键不产生时 legacy+alias 路径完整保留）。
func TestD043_XCUAliasFallback(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "COPPERUSDT", Value: 3.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "XCUUSDm", Value: 0, Ts: time.Now()},
			{Kind: "swap_short", Venue: "3001157", Symbol: "XCUUSDm", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XCUUSDm", Canonical: "XCUUSD", IsPrimary: true},
		},
		aliases: map[string]string{"XCU": "COPPER"},
		// equiv 故意不给 COPPER 覆盖（SILVER 行证明视图在而无该资产）。
		equiv: []pgstore.AssetEquivEntry{
			{AssetClass: "SILVER", Canonical: "SILVER", VenueType: "mt5", Venue: "3001157"},
		},
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	nets := map[string]float64{}
	for _, r := range res.Rows {
		if r.Canonical == "COPPER" {
			nets[r.Structure] = r.NetAnnual
		}
	}
	if len(nets) != 2 || nets["long_cfd_short_perp"] != 3.0 || nets["short_cfd_long_perp"] != -3.0 {
		t.Fatalf("XCU→COPPER alias 兜底应出双行 net=±3: %+v (rows=%+v)", nets, res.Rows)
	}
}

// TestD043_AccrueClassKeyed class 键 paper 行双腿 raw 解析——
// GOLD 行：CEX 经 class 索引（XAU-USDT-SWAP→XAUUSDT），MT 经 class→canonical→
// 映射（GOLD→XAUUSD→XAUUSDm）→ 双腿命中 accrue 水位前进（fail-closed 未触发）。
func TestD043_AccrueClassKeyed(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	entry := now.Add(-25 * time.Hour) // 跨 UTC 日界+≥3 个 8h funding 界——水位必前进
	st := newFakeMtmPaperStore()
	st.equiv = d043Equiv()
	st.mappings = map[string][]pgstore.SymbolMapping{
		"3001157": {{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD", IsPrimary: true}},
	}
	st.universe = map[string]pgstore.CexUniverseRow{
		"okx|XAU-USDT-SWAP": {Venue: "okx", Symbol: "XAU-USDT-SWAP", HedgeCanonical: "XAUUSD"},
	}
	iv := int64(8 * 3600)
	grid := now.Unix() / iv * iv
	st.facts = map[string][]fact.Fact{
		"funding_next_settle|okx|XAUUSDT": {{Value: float64(grid + iv), Ts: now}},
		"funding_interval|okx|XAUUSDT":    {{Value: float64(iv), Ts: now}},
		"funding|okx|XAUUSDT":             {{Value: 5.0, Ts: entry}},
		"swap_long|3001157|XAUUSDm":       {{Value: -1.0, Ts: entry}},
	}
	st.rows[1] = &pgstore.PaperSpotCarry{
		ID: 1, VenueA: "3001157", VenueB: "okx", Symbol: "GOLD", Side: "short_perp",
		Structure: "long_cfd_short_perp", Status: "active", EntryTs: entry, QtyUSD: 1000,
	}
	row := UniverseRow{Canonical: "GOLD", MtVenue: "3001157", CexVenue: "okx",
		NetAnnual: 5, FundingAnn: 5, FundingInst: 5, Structure: "long_cfd_short_perp"}
	rowByKey := map[string]UniverseRow{"3001157×okx|GOLD|long_cfd_short_perp": row}
	accrable, _ := st.ListAccrablePaperSpotCarry(ctx)
	exited, err := accrueExitCfdPaper(ctx, st, accrable, rowByKey, map[string]string{},
		map[string]bool{}, newAssetClassView(st.equiv), now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 0 || len(st.accrues) != 1 {
		t.Fatalf("class 键行应正常 accrue: exited=%d accrues=%+v", exited, st.accrues)
	}
	got := st.rows[1]
	if got.LastAccrueTs == nil || !got.LastAccrueTs.After(entry) {
		t.Fatalf("双腿命中应推进水位 >entry: %+v", got.LastAccrueTs)
	}
}

// TestD043_CexRawIndexClassConflict 同类同 venue 多 perp canonical（GOLD=
// XAUUSDT+PAXGUSDT 同在 okx 有 universe 行）→ class 键歧义删除，accrue 诚实
// miss（水位不前=fail-closed，不错绑异腿 funding）。
func TestD043_CexRawIndexClassConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	entry := now.Add(-25 * time.Hour)
	st := newFakeMtmPaperStore()
	st.equiv = d043Equiv() // GOLD: okx 注册 XAUUSDT+PAXGUSDT 双腿
	st.mappings = map[string][]pgstore.SymbolMapping{
		"3001157": {{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD", IsPrimary: true}},
	}
	st.universe = map[string]pgstore.CexUniverseRow{
		"okx|XAU-USDT-SWAP":  {Venue: "okx", Symbol: "XAU-USDT-SWAP", HedgeCanonical: "XAUUSD"},
		"okx|PAXG-USDT-SWAP": {Venue: "okx", Symbol: "PAXG-USDT-SWAP", HedgeCanonical: "PAXGUSD"},
	}
	iv := int64(8 * 3600)
	grid := now.Unix() / iv * iv
	st.facts = map[string][]fact.Fact{
		"funding_next_settle|okx|XAUUSDT": {{Value: float64(grid + iv), Ts: now}},
		"funding_interval|okx|XAUUSDT":    {{Value: float64(iv), Ts: now}},
		"swap_long|3001157|XAUUSDm":       {{Value: -1.0, Ts: entry}},
	}
	st.rows[1] = &pgstore.PaperSpotCarry{
		ID: 1, VenueA: "3001157", VenueB: "okx", Symbol: "GOLD", Side: "short_perp",
		Structure: "long_cfd_short_perp", Status: "active", EntryTs: entry, QtyUSD: 1000,
	}
	row := UniverseRow{Canonical: "GOLD", MtVenue: "3001157", CexVenue: "okx",
		NetAnnual: 5, FundingAnn: 5, FundingInst: 5, Structure: "long_cfd_short_perp"}
	rowByKey := map[string]UniverseRow{"3001157×okx|GOLD|long_cfd_short_perp": row}
	accrable, _ := st.ListAccrablePaperSpotCarry(ctx)
	if _, err := accrueExitCfdPaper(ctx, st, accrable, rowByKey, map[string]string{},
		map[string]bool{}, newAssetClassView(st.equiv), now); err != nil {
		t.Fatal(err)
	}
	if got := st.rows[1].LastAccrueTs; got != nil && got.After(entry) {
		t.Fatalf("歧义 class 键应诚实 miss 不推进水位: %v", got)
	}
}

// TestD043_ResolveGraduationClass class 键 universe 行毕业解析——
// SILVER→mt canonical SILVER（NJK 命中）+cex canonical XAGUSDT（equiv 唯一腿）。
func TestD043_ResolveGraduationClass(t *testing.T) {
	entries := []SymbolMapEntry{{Canonical: "SILVER", IsPrimary: true}}
	pair, err := ResolveGraduationCanonicals("SILVER", "3001157", "okx", 1, entries, d043Equiv())
	if err != nil {
		t.Fatal(err)
	}
	if pair.MtCanonical != "SILVER" || pair.CexCanonical != "XAGUSDT" || pair.MtVenueKey != "3001157:mt5" {
		t.Errorf("class resolve: %+v", pair)
	}
	// 歧义类（GOLD@okx 三注册腿）fail-closed；venue 无注册腿 fail-closed 不臆造。
	if _, err := ResolveGraduationCanonicals("GOLD", "3001157", "okx", 1,
		[]SymbolMapEntry{{Canonical: "XAUUSD", IsPrimary: true}}, d043Equiv()); err == nil {
		t.Error("GOLD@okx 多 perp canonical 应 fail-closed")
	}
	if _, err := ResolveGraduationCanonicals("BRENT", "3001157", "okx", 1,
		[]SymbolMapEntry{{Canonical: "BRENT", IsPrimary: true}}, d043Equiv()); err == nil {
		t.Error("BRENT@okx 无注册 perp 腿应 fail-closed")
	}
}
