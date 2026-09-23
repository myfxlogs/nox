// carry_universe_tradable_test.go —— D-382 S2：carry 宇宙可执行性过滤锚 A1-A7（先红后绿）。
// 语义公理（业主）：可执行候选一个不丢（venue 回退）；不可执行绝不告警但不静默消失（标记行）。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d382Store 构造标准双 MT venue 场景：TRX canonical，funding binance +9%；
// swap_long mtA −10（贵）/mtB −5（优）；extra 追加 trade_mode 等事实。
func d382Store(extra ...fact.Fact) *fakeScanStore {
	now := time.Now()
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "TRXUSDT", Value: 9.0, Ts: now},
			{Kind: "swap_long", Venue: "mtA", Symbol: "TRXUSD", Value: -10, Ts: now},
			{Kind: "swap_long", Venue: "mtB", Symbol: "TRXUSD", Value: -5, Ts: now},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "mtA", BrokerSymbol: "TRXUSD", Canonical: "TRX"},
			{Venue: "mtB", BrokerSymbol: "TRXUSD", Canonical: "TRX"},
		},
		universe: map[string]pgstore.CexUniverseRow{},
	}
	st.facts = append(st.facts, extra...)
	return st
}

func d382TM(venue string, mode int64, age time.Duration) fact.Fact {
	return fact.Fact{Kind: "trade_mode", Venue: venue, Symbol: "TRXUSD", Value: float64(mode), Ts: time.Now().Add(-age)}
}

func d382Run(t *testing.T, st *fakeScanStore) []UniverseRow {
	t.Helper()
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res.Rows
}

func d382Find(rows []UniverseRow, canonical string) *UniverseRow {
	for i := range rows {
		if rows[i].Canonical == canonical {
			return &rows[i]
		}
	}
	return nil
}

// A1：venue mtA mode=0（禁）+ mtB mode=4（全）同 canonical → 行存活且 MtVenue=mtB（回退不逃脱）。
func TestD382_A1_VenueFallback(t *testing.T) {
	st := d382Store(d382TM("mtA", 0, 0), d382TM("mtB", 4, 0))
	rows := d382Run(t, st)
	r := d382Find(rows, "TRX")
	if r == nil {
		t.Fatalf("A1: TRX 行应存活（venue 回退）, got %+v", rows)
	}
	if r.MtVenue != "mtB" {
		t.Fatalf("A1: MtVenue = %q, want mtB（禁用 venue 回退到可交易次优）", r.MtVenue)
	}
}

// A2：全部 venue 禁交易 → 标记行存在（net=0、Note=mt_trade_disabled、MtVenue=(all-disabled)）。
func TestD382_A2_AllDisabledMarkerRow(t *testing.T) {
	st := d382Store(d382TM("mtA", 0, 0), d382TM("mtB", 0, 0))
	rows := d382Run(t, st)
	r := d382Find(rows, "TRX")
	if r == nil {
		t.Fatalf("A2: 全禁应产标记行（不静默消失）, got %+v", rows)
	}
	if r.NetAnnual != 0 || r.Note != "mt_trade_disabled" || r.MtVenue != "(all-disabled)" {
		t.Fatalf("A2: 标记行字段 = net %v/note %q/venue %q", r.NetAnnual, r.Note, r.MtVenue)
	}
}

// A3：无 trade_mode fact → 行正常保留（fail-open，venue 拒单是最终权威）。
func TestD382_A3_NoTradeMode_FailOpen(t *testing.T) {
	rows := d382Run(t, d382Store()) // 无 trade_mode facts
	r := d382Find(rows, "TRX")
	if r == nil || r.MtVenue != "mtB" || r.NetAnnual != -9.25 {
		t.Fatalf("A3: fail-open 行应正常存活（net=-9.25 mtB（D-412 F1 年化：swap −5bps_daily=−18.25%%/yr））, got %+v", r)
	}
}

// A4：A2 场景行过 EmitThresholdAlerts → 零告警（net=0 天然不触，死行可见不响）。
func TestD382_A4_MarkerRow_NoAlert(t *testing.T) {
	rows := d382Run(t, d382Store(d382TM("mtA", 0, 0), d382TM("mtB", 0, 0)))
	called := 0
	EmitThresholdAlerts(context.Background(), nil, rows, func(_ context.Context, _ *int64, _, _ string) error {
		called++
		return nil
	})
	if called != 0 {
		t.Fatalf("A4: 标记行不应触发告警, got %d 条", called)
	}
}

// A5：买向禁（mode=2 ShortOnly 禁买）卖向通 → 仅 short_cfd_long_perp 行存活。
func TestD382_A5_DirectionalFilter(t *testing.T) {
	now := time.Now()
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "TRXUSDT", Value: -5.0, Ts: now}, // 负 funding → short_cfd 结构
			{Kind: "swap_long", Venue: "mtA", Symbol: "TRXUSD", Value: -10, Ts: now},
			{Kind: "swap_short", Venue: "mtA", Symbol: "TRXUSD", Value: 8, Ts: now},
			d382TM("mtA", 2, 0), // 只可开空：swap_long（买向）被拦、swap_short（卖向）放行
		},
		mappings: []pgstore.SymbolMapping{{Venue: "mtA", BrokerSymbol: "TRXUSD", Canonical: "TRX"}},
		universe: map[string]pgstore.CexUniverseRow{},
	}
	rows := d382Run(t, st)
	long, short := false, false
	for _, r := range rows {
		if r.Canonical != "TRX" {
			continue
		}
		if r.Structure == "long_cfd_short_perp" {
			long = true
		}
		if r.Structure == "short_cfd_long_perp" {
			short = true
		}
	}
	if long || !short {
		t.Fatalf("A5: want 仅 short_cfd_long_perp 存活, got long=%v short=%v rows=%+v", long, short, rows)
	}
}

// A6：mode=3（CloseOnly 只可平仓）→ 同禁用处理（允许集语义，拦开新仓）→ 标记行。
func TestD382_A6_CloseOnly_Blocked(t *testing.T) {
	rows := d382Run(t, d382Store(d382TM("mtA", 3, 0), d382TM("mtB", 3, 0)))
	r := d382Find(rows, "TRX")
	if r == nil || r.Note != "mt_trade_disabled" || r.NetAnnual != 0 {
		t.Fatalf("A6: CloseOnly 应同禁用产标记行, got %+v", r)
	}
}

// A7：trade_mode fact 超 TTL（48h 前 mode=0）→ 视同未知放行，行正常存活（新鲜度闸锚）。
func TestD382_A7_StaleTradeMode_Pass(t *testing.T) {
	rows := d382Run(t, d382Store(d382TM("mtA", 0, 48*time.Hour), d382TM("mtB", 0, 48*time.Hour)))
	r := d382Find(rows, "TRX")
	if r == nil || r.MtVenue != "mtB" || r.NetAnnual != -9.25 {
		t.Fatalf("A7: 过期 trade_mode 应视同未知放行（fail-open）, got %+v", r)
	}
}
