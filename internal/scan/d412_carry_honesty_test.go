// d412_carry_honesty_test.go —— D-412 S5：扫描器诚实性四修六锚（数值锚与真实生产案例对拍）。
// ① INJ 红锚（先红后绿：修前 net=+75.9 排名前列且不触发 suspect；修后 net=−225.8 且
//
//	suspectNote 置零——脏源隔离双路径）；② 全叉积多行并存（D-410 保底槽三重键回归）；
//
// ③ flapping 账户 swap_free=false 且成本按 7d 均值；④ stale funding 注入 note=stale；
// ⑤ 金属行兼容（swap=0 修前修后不变）；⑥ swap 列年化口径 ×3.65 读方回归。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

func d412Scanner(st Store) *CarryUniverseScanner { return NewCarryUniverseScanner(st) }

// TestD412_F1_INJ_RedAnchor 锚①：INJUSDT@1314615 swap_long=−64.888 bps_daily × funding=11.0
// ——修前（单位混算+符号放大）net=11−(−64.888)=+75.9 排名前列且 |64.9|<100 不触发 suspect；
// 修后（F1 年化+成本直加）honest net=11+(−236.84)=−225.8 且 |−236.8|>100 → suspect 置零
// （脏源隔离双路径：数值诚实化+排名剔除同锚断言）。
func TestD412_F1_INJ_RedAnchor(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "INJUSDT", Value: 11.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "1314615", Symbol: "INJUSD", Value: -64.888, Ts: time.Now()}, // 生产原值
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "1314615", BrokerSymbol: "INJUSD", Canonical: "INJ"},
		},
	}
	res, err := d412Scanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d, want 1: %+v", len(res.Rows), res.Rows)
	}
	row := res.Rows[0]
	// 双路径断言（R4）：honest net=−225.8 被 suspect 置零——行可见、net=0、note 含年化 swap 值
	if row.NetAnnual != 0 {
		t.Errorf("INJ net=%v, want 0（suspect 置零剔除排名；honest 算术=11−236.84=−225.8）", row.NetAnnual)
	}
	if row.Note == "" || !containsSub(row.Note, "swap 236.8") {
		t.Errorf("INJ note=%q, want suspect: swap 236.8%%（|−236.8|>100 触发）", row.Note)
	}
	if row.SwapFree {
		t.Error("INJ swap_free 应 false（脏源非免息）")
	}
}

// TestD412_F2_FullCrossProduct 锚②：XAU 同 canonical 多 MT venue×多 CEX venue——
// 全叉积行并存（3001157 在跑授权线恒在监控视野；任意白名单三元组可命中，D-410 保底槽回归）。
func TestD412_F2_FullCrossProduct(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "XAUUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "funding", Venue: "gate", Symbol: "XAUUSDT", Value: 6.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "XAUUSD", Value: 0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "39050818", Symbol: "XAUUSD", Value: -1.0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XAUUSD", Canonical: "XAUUSD"},
			{Venue: "39050818", BrokerSymbol: "XAUUSD", Canonical: "XAUUSD"},
		},
	}
	res, err := d412Scanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	type triple struct{ mt, cex string }
	got := map[triple]bool{}
	for _, r := range res.Rows {
		if r.Canonical == "XAU" {
			got[triple{r.MtVenue, r.CexVenue}] = true
		}
	}
	for _, want := range []triple{
		{"3001157", "binance"}, {"3001157", "gate"},
		{"39050818", "binance"}, {"39050818", "gate"},
	} {
		if !got[want] {
			t.Errorf("全叉积缺三元组 %v（D-410 保底槽三重键回归）: %v", want, got)
		}
	}
}

// TestD412_F3_FlappingWindowMean 锚③：39050818 XAU 0/−1.4133 交替（204/254 行）——
// 7d 均值 −0.7847 bps_daily → 成本 −2.86%/yr 且 swap_free=false（点值判定=抛硬币的修正）。
func TestD412_F3_FlappingWindowMean(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "XAUUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "39050818", Symbol: "XAUUSD", Value: -1.4133, Ts: time.Now()}, // 最新点值非零
		},
		windowMeans: map[string]float64{"swap_long|39050818|XAUUSD": -0.7847},
		mappings: []pgstore.SymbolMapping{
			{Venue: "39050818", BrokerSymbol: "XAUUSD", Canonical: "XAUUSD"},
		},
	}
	res, err := d412Scanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d: %+v", len(res.Rows), res.Rows)
	}
	row := res.Rows[0]
	if row.SwapFree {
		t.Error("flapping 账户均值年化 −2.86%：swap_free 应 false（点值 0 判定=抛硬币已废）")
	}
	if row.SwapLongPct == nil || *row.SwapLongPct < -2.87 || *row.SwapLongPct > -2.85 {
		t.Errorf("swap 成本应取 7d 均值年化 ≈−2.86，got %v", row.SwapLongPct)
	}
	if row.NetAnnual < 2.13 || row.NetAnnual > 2.15 {
		t.Errorf("net=5−2.86 ≈2.14，got %v", row.NetAnnual)
	}
}

// TestD412_F4_StaleFundingGate 锚④：funding fact 龄 >24h → 行 net=0+note=stale 剔除排名。
func TestD412_F4_StaleFundingGate(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 6.65, Ts: time.Now().Add(-25 * time.Hour)},
			{Kind: "swap_long", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "BTCUSD", Canonical: "BTC"},
		},
	}
	res, err := d412Scanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d: %+v", len(res.Rows), res.Rows)
	}
	row := res.Rows[0]
	if row.NetAnnual != 0 || row.Note != "stale" {
		t.Errorf("stale funding 应 net=0+note=stale，got net=%v note=%q", row.NetAnnual, row.Note)
	}
	if row.SwapFree {
		t.Error("stale 行 swap_free 应 false（读口不诚实态）")
	}
}

// TestD412_F4_SwapStaleGate swap 侧龄期闸（>48h）同型断言。
func TestD412_F4_SwapStaleGate(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 6.65, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now().Add(-49 * time.Hour)},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "BTCUSD", Canonical: "BTC"},
		},
	}
	res, err := d412Scanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Note != "stale" {
		t.Fatalf("swap 龄期超闸应 note=stale: %+v", res.Rows)
	}
}

// TestD412_F1_MetalsUnchanged 锚⑤：金属行 swap=0——修前修后 net 不变（0 在任何单位下是 0
// =兼容性证明；funding 路径零改动）。
func TestD412_F1_MetalsUnchanged(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "XAUUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "XAUUSD", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XAUUSD", Canonical: "XAUUSD"},
		},
	}
	res, err := d412Scanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d", len(res.Rows))
	}
	row := res.Rows[0]
	if row.NetAnnual < 4.99 || row.NetAnnual > 5.01 {
		t.Errorf("金属 swap=0 行 net 应恰=funding 5.0（单位免疫），got %v", row.NetAnnual)
	}
	if !row.SwapFree {
		t.Error("swap=0 仍应 swap_free（均值年化 0<1）")
	}
}

// TestD412_F1_SwapColumnAnnualized 锚⑥：swap 透出列年化口径（读方可见 ×3.65——R3 口径
// 统一声明锚：bps_daily −10 → swap_long_pct −36.5）。
func TestD412_F1_SwapColumnAnnualized(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "WTIUSDT", Value: 30.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "WTIUSD", Value: -10, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "WTIUSD", Canonical: "WTI"},
		},
	}
	res, err := d412Scanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	row := res.Rows[0]
	if row.SwapLongPct == nil {
		t.Fatal("SwapLongPct 应透出（年化口径）")
	}
	if *row.SwapLongPct < -36.6 || *row.SwapLongPct > -36.4 {
		t.Errorf("SwapLongPct want −36.5（−10bps_daily×3.65 年化统一），got %v", *row.SwapLongPct)
	}
	if row.NetAnnual < -6.7 || row.NetAnnual > -6.5 {
		t.Errorf("net=30−36.5=−6.5（年化统一同口径），got %v", row.NetAnnual)
	}
}
