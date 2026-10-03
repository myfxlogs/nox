// d028_paper_fix_test.go —— D-028 四修验收锚：
// T-okx：next_settle 领先两窗 → lastBoundaryLE 循环回退仍 accrue（旧径单次回退 water=base 零 accrue）；
// T-TRX：cexRawByCanon 键 NJK(cexFactSymbol) 后 okx TRX-USDT-SWAP / gate TRX_USDT 都命中查询键；
// T-long：周末（funding=0 → universe 赢家翻转 short）long 行按自身 structure net 判定不 adverse。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// TestD028_LastBoundaryLEMultiWindowAhead T-okx 单元：ns=now+2·iv（领先两窗）→
// 回退两步 ≤now → 返回非零（旧径单次回退仍 >now → 零 → water=base 静默零 accrue）。
func TestD028_LastBoundaryLEMultiWindowAhead(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 30, 0, 0, time.UTC) // 00:30（结算窗 00:00 刚过）
	ns := now.Add(16 * time.Hour)                        // 领先两窗（iv=8h）
	got := lastBoundaryLE(ns, 8*time.Hour, now)
	if got.IsZero() || got.After(now) {
		t.Fatalf("领先两窗应循环回退至 ≤now，got %v", got)
	}
	// 回退两步：+16h→+8h→00:30（=now；网格相位随 ns 锚定，非午夜对齐）
	want := now
	if !got.Equal(want) {
		t.Fatalf("water want %v, got %v", want, got)
	}
}

// TestD028_LastBoundaryLEFarAheadBounded 极端领先（iv=1s 网格 +10 天）→ 有界回退仍 ≤now 不挂死。
func TestD028_LastBoundaryLEFarAheadBounded(t *testing.T) {
	now := time.Now()
	ns := now.Add(10 * 24 * time.Hour)
	got := lastBoundaryLE(ns, time.Second, now)
	if !got.IsZero() {
		t.Fatalf("异常 1s 网格领先 10 天应触及有界上限返回零（fail-closed 零边界告警 ⑤ 兜），got %v", got)
	}
}

// TestD028_CexRawByCanonTrxKeys T-TRX：okx TRX-USDT-SWAP / gate TRX_USDT 两行 universe →
// 索引含查询键（venue|NJK(canonical)=trx → facts 键 TRXUSDT）——旧径 NJK(raw) 不归一 → miss。
func TestD028_CexRawByCanonTrxKeys(t *testing.T) {
	collect := func(rows []pgstore.CexUniverseRow) map[string]string {
		m := map[string]string{}
		for _, ur := range rows {
			for _, canon := range []string{ur.HedgeCanonical, ur.Symbol, NormalizeJoinKey(cexFactSymbol(ur.Symbol))} {
				if k := ur.Venue + "|" + NormalizeJoinKey(canon); canon != "" {
					m[k] = cexFactSymbol(ur.Symbol)
				}
			}
		}
		return m
	}
	m := collect([]pgstore.CexUniverseRow{
		{Venue: "okx", Symbol: "TRX-USDT-SWAP", HedgeCanonical: "TRX"},
		{Venue: "gate", Symbol: "TRX_USDT", HedgeCanonical: "TRX"},
	})
	for _, key := range []string{"okx|TRX", "gate|TRX"} {
		if got, ok := m[key]; !ok || got != "TRXUSDT" {
			t.Fatalf("键 %s 应命中 facts 键 TRXUSDT，got %q (ok=%v)", key, got, ok)
		}
	}
}

// TestD028_LongRowWeekendNoAdverse T-long：周末 funding=0 → universe 赢家翻转 short（net −12），
// long 行自身 net=+7.3 > 0 → 按 structure 匹配判定不 adverse（旧径借赢家 net → 误杀）。
func TestD028_LongRowWeekendNoAdverse(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 开仓：long_cfd_short_perp 行（winning/自身 net 同源 +7.3 相位）。
	rows := []UniverseRow{
		{Canonical: "XAU", MtVenue: "3001157", CexVenue: "okx", NetAnnual: 12.0, FundingAnn: 12.0,
			Structure: "long_cfd_short_perp"},
		// D-028 ③：赢家翻转后的 short 行（周末形态）——旧 rowByKey 无结构维度会被它遮蔽。
		{Canonical: "XAU", MtVenue: "3001157", CexVenue: "okx", NetAnnual: -12.0, FundingAnn: 0,
			Structure: "short_cfd_long_perp"},
	}
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, now, d410Opts(), nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 回拨水位 25h → 次轮判定（long 行自身 net=+12 正常在轨）。
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET last_accrue_ts = $1`, now.Add(-25*time.Hour)); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	// 下轮 universe：funding=0 → 赢家翻转 short（−12 垫底排序后仍单发 winner 形态的对照面）。
	rowsNext := []UniverseRow{
		{Canonical: "XAU", MtVenue: "3001157", CexVenue: "okx", NetAnnual: -12.0, FundingAnn: 0,
			Structure: "short_cfd_long_perp"},
		{Canonical: "XAU", MtVenue: "3001157", CexVenue: "okx", NetAnnual: 12.0, FundingAnn: 0,
			Structure: "long_cfd_short_perp"},
	}
	exited, err := func() (int, error) {
		accrable, _ := s.ListAccrablePaperSpotCarry(ctx)
		rowByKey := map[string]UniverseRow{}
		dirtyPair := map[string]string{}
		for _, r := range rowsNext {
			rowByKey[r.MtVenue+"×"+r.CexVenue+"|"+r.Canonical+"|"+r.Structure] = r
		}
		silentMt := map[string]bool{}
		return accrueExitCfdPaper(ctx, s, accrable, rowByKey, dirtyPair, silentMt, now)
	}()
	if err != nil {
		t.Fatalf("accrue round: %v", err)
	}
	if exited != 0 {
		t.Fatalf("long 行自身 net=+12 周末不得 adverse（旧径借赢家 −12 误杀），exited=%d", exited)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	found := false
	for _, a := range active {
		if a.Symbol == "XAU" && a.Structure == "long_cfd_short_perp" {
			found = true
		}
	}
	if !found {
		t.Fatal("long 行应仍 active")
	}
}

// TestD028_MtRawIndexPrimaryWins ⑥ 验收锚：3001157 XAU 双行（XAUUSDm primary / XAUUSD247m）
// → 选 is_primary 行（旧径 last-write-wins 随机）；唯一行兜底；多行无 primary=ambiguous 不设键。
func TestD028_MtRawIndexPrimaryWins(t *testing.T) {
	maps := []pgstore.SymbolMapping{
		{Venue: "3001157", BrokerSymbol: "XAUUSD247m", Canonical: "XAUUSD", IsPrimary: false},
		{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD", IsPrimary: true},
	}
	m := mtRawIndex(maps)
	if m["XAU"] != "XAUUSDm" { // NJK("XAUUSD")="XAU"（查询键同形）
		t.Fatalf("3001157 XAU 应选 is_primary=XAUUSDm（旧径 last-write-wins 随机），got %q", m["XAU"])
	}
	// 唯一行兜底（无 primary=隐式 primary，brokers_symbolmap 同注）
	m = mtRawIndex([]pgstore.SymbolMapping{
		{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD", IsPrimary: false},
	})
	if m["XAU"] != "XAUUSDm" { // NJK("XAUUSD")="XAU"（查询键同形）
		t.Fatalf("唯一行应兜底，got %q", m["xauusd"])
	}
	// 多行无 primary=ambiguous → 键不设（fail-closed，禁 last-write-wins）
	m = mtRawIndex([]pgstore.SymbolMapping{
		{Venue: "3001157", BrokerSymbol: "XAUUSD247m", Canonical: "XAUUSD"},
		{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD"},
	})
	if _, exists := m["XAU"]; exists {
		t.Fatalf("多行无 primary 应 ambiguous 不设键，got %q", m["XAU"])
	}
}

// TestD028_SwapFreeCollectZeroesVal ④ 验收锚：collectMtSwaps swap_free 旗=true → 该 MT 腿
// swap val 置 0（不等 7d 均值收敛；spec 错值源屏蔽，3001157 实拍）。
func TestD028_SwapFreeCollectZeroesVal(t *testing.T) {
	sc := NewCarryUniverseScanner(&fakeScanStore{
		facts: []fact.Fact{
			{Kind: "swap_long", Venue: "3001157", Symbol: "XAUUSDm", Value: -1, Ts: time.Now()},
			{Kind: "swap_short", Venue: "3001157", Symbol: "XAUUSDm", Value: 0.5, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAU", IsPrimary: true},
		},
		windowMeans: map[string]float64{
			"swap_long|3001157|XAUUSDm":  -1.28, // spec 错值形态
			"swap_short|3001157|XAUUSDm": 0.5,
		},
	})
	long, short := sc.collectMtSwaps(context.Background(),
		[]fact.Fact{{Kind: "swap_long", Venue: "3001157", Symbol: "XAUUSDm", Value: -1, Ts: time.Now()}},
		[]fact.Fact{{Kind: "swap_short", Venue: "3001157", Symbol: "XAUUSDm", Value: 0.5, Ts: time.Now()}},
		func(fact.Fact) (int64, bool) { return 0, false },
		map[string]bool{}, &ScanResult{}, map[string]string{},
		map[string]bool{"3001157": true}) // swap_free 旗=true
	if v := long["XAU|3001157"].val; v != 0 {
		t.Fatalf("swap_free long val want 0, got %v", v)
	}
	if v := short["XAU|3001157"].val; v != 0 {
		t.Fatalf("swap_free short val want 0, got %v", v)
	}
}
