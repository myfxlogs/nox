// d594_unit_unknown_test.go D-594 S4：未标定疑似商品 → unit_unknown 分类亮灯。
package symdig

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

func f64(v float64) *float64 { return &v }

// 敏感 canonical（存在非 1 标定行）的未标定行 → unit_unknown；已标定行 → 原 paired/
// unpaired 判定不受影响；非敏感品种未标定行 → 不受影响。
func TestD594UnitUnknown(t *testing.T) {
	now := time.Now().UTC()
	st := &fakeStore{
		facts: []fact.Fact{
			{Kind: fact.KindTradeMode, Venue: "vA", Symbol: "COPPERx", Value: fact.TradeModeFull, Ts: now},
			{Kind: fact.KindTradeMode, Venue: "vB", Symbol: "COPPER", Value: fact.TradeModeFull, Ts: now},
			{Kind: fact.KindTradeMode, Venue: "vA", Symbol: "EURUSDx", Value: fact.TradeModeFull, Ts: now},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "vA", BrokerSymbol: "COPPERx", Canonical: "XCUUSD", ContractSize: f64(5)}, // 未标定敏感行
			{Venue: "vB", BrokerSymbol: "COPPER", Canonical: "XCUUSD", ContractSize: f64(1), PriceScale: f64(1.0 / 2204.62), QtyScale: f64(2204.62)},
			{Venue: "vA", BrokerSymbol: "EURUSDx", Canonical: "EURUSD", ContractSize: f64(100000)}, // 非敏感未标定
		},
		aliases:  map[string]string{},
		uni:      map[string]bool{"vB|XCU": true},
		prevKeys: map[string]bool{},
	}
	sum, err := classify(context.Background(), st)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if k, _ := klassOf(sum.Rows, "vA", "COPPERx"); k != KlassUnitUnknown {
		t.Errorf("COPPERx klass=%q, want unit_unknown", k)
	}
	if k, _ := klassOf(sum.Rows, "vB", "COPPER"); k != KlassPaired {
		t.Errorf("calibrated COPPER klass=%q, want paired", k)
	}
	if k, r := klassOf(sum.Rows, "vA", "EURUSDx"); k == KlassUnitUnknown {
		t.Errorf("non-sensitive unrated must not be unit_unknown (reason=%q)", r)
	}
}
