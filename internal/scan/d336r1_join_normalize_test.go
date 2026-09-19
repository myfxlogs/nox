package scan

// D-336R1 T：join 键双侧规范化——MT map canonical=XAUUSD（XAUUSD247m→XAUUSD）vs
// CEX cexCanonical=XAU（去 USDT）双形分裂永不相等→黄金/BTC/PAXG 全 join 失败
// （部署后 A8：4 行全指数对）。规范化后 XAUUSD/XAUUSDT 同键 XAU，行出 canonical
// 用规范化键。红=现无 normalize → XAU 双面产 0 行；SPX500_ 回归锚（两形同怪
// 误撞成功形态，规范化后仍 join 且键正化）。
import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// TestD336R1_XAUDualShapeJoin XAU 双面（MT XAUUSD map canonical=XAUUSD swap_long=0
// + CEX XAUUSDT funding=12.3）→ 出规范化 XAU 行 net≈12.3 swap_free（红=现 skipped）。
func TestD336R1_XAUDualShapeJoin(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "XAUUSDT", Value: 12.3, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "XAUUSD", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "XAUUSD", Canonical: "XAUUSD"}, // 生产双形：map 侧 USD 形
		},
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d, want 1（红=双形分裂 join 失败）: %+v", len(res.Rows), res.Rows)
	}
	r := res.Rows[0]
	if r.Canonical != "XAU" {
		t.Errorf("行出 canonical 应为规范化键 XAU, got %s", r.Canonical)
	}
	if !r.SwapFree || r.NetAnnual < 12.2 || r.NetAnnual > 12.4 {
		t.Errorf("XAU net want ≈12.3 swap_free, got %+v", r)
	}
}

// TestD336R1_SPXJoinRegression SPX500_ 回归锚：CEX SPX500_USDT→SPX500_、MT map
// canonical=SPX500_——现双形同怪误撞成功；规范化后仍 join、键正化为 SPX500。
func TestD336R1_SPXJoinRegression(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "okx", Symbol: "SPX500_USDT", Value: -4.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "SPX500_", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "SPX500_", Canonical: "SPX500_"},
		},
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d, want 1（SPX 回归：规范化不得打断现 join）: %+v", len(res.Rows), res.Rows)
	}
	if res.Rows[0].Canonical != "SPX500" {
		t.Errorf("SPX 行出 canonical 应为规范化键 SPX500, got %s", res.Rows[0].Canonical)
	}
}

// TestD336R1_NormalizeJoinKey 单元锚：提示词示例全钉。
func TestD336R1_NormalizeJoinKey(t *testing.T) {
	for in, want := range map[string]string{
		"XAUUSD":   "XAU",
		"XAUUSDT":  "XAU",
		"SPX500_":  "SPX500",
		"BTCUSDT":  "BTC",
		"BTC":      "BTC",
		"XAU":      "XAU",
		"USDT":     "USDT", // 去后为空不去
		"ETHUSD":   "ETH",
		"SPX500__": "SPX500_", // 只去一个尾部 _
	} {
		if got := normalizeJoinKey(in); got != want {
			t.Errorf("normalizeJoinKey(%q)=%q, want %q", in, got, want)
		}
	}
}
