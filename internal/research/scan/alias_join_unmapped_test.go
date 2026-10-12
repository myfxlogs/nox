// d464_alias_test.go D-464 S1（T1）：别名表接入归一层——MT 侧 map miss 别名直配 +
// 映射 canonical 过别名 + CEX 侧同过 + 循环剔除。
package scan

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// T1a：MT 侧 raw symbol 无映射但别名命中（GOLD→XAU）→ universe 行产出。
func TestD464_AliasJoinUnmapped(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "gate", Symbol: "XAUUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "3001157", Symbol: "GOLD", Value: -2, Ts: time.Now()},
		},
		mappings: nil, // GOLD 无任何映射——别名直配是唯一通道
		aliases:  map[string]string{"GOLD": "XAU"},
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Canonical != "XAU" || res.Rows[0].MtVenue != "3001157" {
		t.Fatalf("want XAU row via alias, got %+v skipped=%d", res.Rows, res.Skipped)
	}
}

// T1b：映射 canonical 也过别名（map 写 GOLD→canonical GOLD，别名翻 XAU）+CEX 侧别名。
func TestD464_AliasMappedAndCex(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "gate", Symbol: "GOLDUSDT", Value: 1.0, Ts: time.Now()}, // CEX 侧 GOLDUSDT→GOLD→alias→XAU
			{Kind: "swap_long", Venue: "3001157", Symbol: "XAUUSDm", Value: -1, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "GOLD"},
		},
		aliases: map[string]string{"GOLD": "XAU"},
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Canonical != "XAU" {
		t.Fatalf("want XAU row (both sides aliased), got %+v", res.Rows)
	}
}

// T1c：循环别名 A→B→A 剔除不致死；正常别名不受影响。
func TestD464_AliasCycle(t *testing.T) {
	clean, dropped := cleanAliases(map[string]string{
		"A": "B", "B": "A", // 循环
		"GOLD": "XAU",
	})
	if len(dropped) != 2 || clean["GOLD"] != "XAU" {
		t.Fatalf("dropped=%v clean=%v", dropped, clean)
	}
}
