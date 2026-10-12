// broker_manager_d383_test.go —— D-383 A6/A8+A2.3 告警：primarySplit 纯函数、
// symbolPlan 订阅收窄（branch1 显式 primary/branch2 auto-map）、Resubscribe 同规则滤、
// 歧义组 alerts 告警 30min 冷却。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/platform/testutil/storetest"
	"arb/internal/store/pgstore"
)

func d383BMStore(t *testing.T) *pgstore.Store {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_SERVICE_DSN")
	if _, err := st.Pool().Exec(context.Background(),
		`TRUNCATE broker_symbol_map`); err != nil {
		t.Fatalf("TRUNCATE broker_symbol_map: %v", err)
	}
	return st
}

func d383SeedMap(t *testing.T, st *pgstore.Store, rows []struct {
	venue, sym, canon string
	primary           bool
}) {
	t.Helper()
	for _, r := range rows {
		if _, err := st.Pool().Exec(context.Background(),
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ($1,'mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING`, r.venue); err != nil {
			t.Fatalf("seed broker %s: %v", r.venue, err)
		}
		if _, err := st.Pool().Exec(context.Background(),
			`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary) VALUES ($1,$2,$3,$4)`,
			r.venue, r.sym, r.canon, r.primary); err != nil {
			t.Fatalf("seed %s.%s: %v", r.venue, r.sym, err)
		}
	}
}

// TestD383_PrimarySplit 纯函数：显式 primary/隐式 primary（单行组）/blocked（多行非 primary）/
// deadGroups（多行零 primary）/空 canonical 行恒合格。
func TestD383_PrimarySplit(t *testing.T) {
	mappings := []pgstore.SymbolMapping{
		{Venue: "v", BrokerSymbol: "A", Canonical: "CAN", IsPrimary: true},
		{Venue: "v", BrokerSymbol: "B", Canonical: "CAN"},
		{Venue: "v", BrokerSymbol: "C", Canonical: "SOLO"},
		{Venue: "v", BrokerSymbol: "META", Canonical: ""},
	}
	ok, blocked, dead := primarySplit(mappings)
	got := map[string]bool{}
	for _, m := range ok {
		got[m.BrokerSymbol] = true
	}
	if !got["A"] || !got["C"] || !got["META"] || got["B"] {
		t.Fatalf("ok 集 = %v, want A/C/META 合格、B 抑制（隐式 primary=单行组 C）", got)
	}
	if !blocked["B"] || len(blocked) != 1 {
		t.Fatalf("blocked = %v, want 仅 B", blocked)
	}
	if len(dead) != 0 {
		t.Fatalf("dead = %v, want 空（组 CAN 有 primary）", dead)
	}
	_, _, dead = primarySplit([]pgstore.SymbolMapping{
		{Venue: "v", BrokerSymbol: "A", Canonical: "CAN"},
		{Venue: "v", BrokerSymbol: "B", Canonical: "CAN"},
	})
	if len(dead) != 1 || !dead["v|CAN"] {
		t.Fatalf("dead = %v, want {v|CAN}（多行零 primary 歧义组）", dead)
	}
}

// TestD383A6_SymbolPlanSubscriptionNarrowed A6a/A8：双行组（XAUUSDm primary 固化态）——
// 订阅集只收合格行，blocked 含 variant，全量 sm 保留（native fact 源）；单行组隐式 primary
// 不误伤（A8 前半，Upsert 默认 false 行照常可订阅）。
func TestD383A6_SymbolPlanSubscriptionNarrowed(t *testing.T) {
	st := d383BMStore(t)
	d383SeedMap(t, st, []struct {
		venue, sym, canon string
		primary           bool
	}{
		{"v", "XAUUSDm", "XAUUSD", true},
		{"v", "XAUUSD247m", "XAUUSD", false}, // rule 复活变体（247m 场景）
		{"v", "EURUSD", "EURUSD", false},     // 新行默认 false，单行组=隐式 primary（A8 不误伤）
	})
	m := NewBrokerManager(st, nil, nil, time.Minute)
	groups := []SymbolGroup{{Symbol: "XAUUSDm"}, {Symbol: "XAUUSD247m"}, {Symbol: "EURUSD"}}
	sm, keys, blocked, _, _, err := m.symbolPlan(context.Background(), "v", func(context.Context) ([]SymbolGroup, error) {
		return groups, nil
	})
	if err != nil {
		t.Fatalf("symbolPlan: %v", err)
	}
	keySet := map[string]bool{}
	for _, k := range keys {
		keySet[k] = true
	}
	if keySet["XAUUSD247m"] || !keySet["XAUUSDm"] || !keySet["EURUSD"] {
		t.Fatalf("订阅集 = %v, want XAUUSDm+EURUSD、无 XAUUSD247m（红=A6 订阅收窄缺失）", keySet)
	}
	if !blocked["XAUUSD247m"] || blocked["EURUSD"] || len(blocked) != 1 {
		t.Fatalf("blocked = %v, want 仅 XAUUSD247m（A2.3 发射抑制集）", blocked)
	}
	if sm["XAUUSD247m"] != "XAUUSD" {
		t.Fatalf("全量 sm 应保留 variant 行（native fact 源）: %v", sm)
	}
}

// TestD383A6_ResubscribeFiltered A6b：运行时补订过同规则滤——variant 被拒、
// 合格行放行；顺序保持。
func TestD383A6_ResubscribeFiltered(t *testing.T) {
	st := d383BMStore(t)
	d383SeedMap(t, st, []struct {
		venue, sym, canon string
		primary           bool
	}{
		{"v", "XAUUSDm", "XAUUSD", true},
		{"v", "XAUUSD247m", "XAUUSD", false},
	})
	m := NewBrokerManager(st, nil, nil, time.Minute)
	var captured []string
	m.mu.Lock()
	m.running["v"] = &running{resubscribe: func(syms []string) { captured = syms }}
	m.mu.Unlock()

	m.Resubscribe("v", []string{"XAUUSDm", "XAUUSD247m", "EURUSD"})
	if len(captured) != 1 || captured[0] != "XAUUSDm" {
		t.Fatalf("补订 = %v, want [XAUUSDm]（红=A6b variant 未被滤）", captured)
	}
}

// TestD383A6_AutoMapMultiRowDeadGroup A6c：auto-map 同 canonical 匹配多符号 →
// 多行零 primary 组不订阅（fail-dead）+ alerts 告警；单行组照常订阅（A8）。
func TestD383A6_AutoMapMultiRowDeadGroup(t *testing.T) {
	st := d383BMStore(t)
	d383SeedMap(t, st, []struct {
		venue, sym, canon string
		primary           bool
	}{
		{"other", "XAUUSDm", "XAUUSD", true}, // 既有 canonical 索引源（aliases）
		{"other", "XAUUSD247m", "XAUUSD", false},
		{"other", "EURUSD", "EURUSD", true},
	})
	if _, err := st.Pool().Exec(context.Background(),
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('fresh','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING`); err != nil {
		t.Fatalf("seed fresh account: %v", err)
	}
	m := NewBrokerManager(st, nil, nil, time.Minute)
	var alerts []string
	m.AlertSink = func(level, msg string) { alerts = append(alerts, level+"|"+msg) }
	groups := []SymbolGroup{{Symbol: "XAUUSDm"}, {Symbol: "XAUUSD247m"}, {Symbol: "EURUSD"}}
	_, syms, blocked, _, _, err := m.symbolPlan(context.Background(), "fresh", func(context.Context) ([]SymbolGroup, error) {
		return groups, nil
	})
	if err != nil {
		t.Fatalf("symbolPlan: %v", err)
	}
	symSet := map[string]bool{}
	for _, s := range syms {
		symSet[s] = true
	}
	if symSet["XAUUSDm"] || symSet["XAUUSD247m"] {
		t.Fatalf("多行零 primary 组进了订阅集: %v（红=fail-dead 缺失/默默猜 primary）", symSet)
	}
	if !symSet["EURUSD"] {
		t.Fatalf("单行组 EURUSD 应照常订阅（A8 不误伤）: %v", symSet)
	}
	if !blocked["XAUUSDm"] || !blocked["XAUUSD247m"] {
		t.Fatalf("blocked = %v, want 双行皆抑制（canonical 停供）", blocked)
	}
	found := false
	for _, a := range alerts {
		if strings.Contains(a, "fresh/XAUUSD") {
			found = true
		}
	}
	if !found {
		t.Fatalf("alerts 应含 fresh/XAUUSD 歧义组告警: %v", alerts)
	}
}

// TestD383A4_AmbiguousAlertCooldown A2.3/A4：歧义组告警落 alerts 表（AlertSink 链），
// (venue,canonical) 键 30min 冷却——重复触发单次报。
func TestD383A4_AmbiguousAlertCooldown(t *testing.T) {
	st := d383BMStore(t)
	m := NewBrokerManager(st, nil, nil, time.Minute)
	var alerts []string
	m.AlertSink = func(level, msg string) { alerts = append(alerts, level+"|"+msg) }

	m.alertAmbiguousGroups(map[string]bool{"v|XAUUSD": true})
	m.alertAmbiguousGroups(map[string]bool{"v|XAUUSD": true}) // 冷却窗口内重复触发
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d 行, want 1（红=冷却缺失逐轮重报）", len(alerts))
	}
	if !strings.Contains(alerts[0], "v/XAUUSD") || !strings.HasPrefix(alerts[0], "warn|") {
		t.Fatalf("告警 = %q, want warn 级含 v/XAUUSD", alerts[0])
	}
	// AlertSink nil（测试直构造）安全不 panic
	m2 := NewBrokerManager(st, nil, nil, time.Minute)
	m2.alertAmbiguousGroups(map[string]bool{"v|XAUUSD": true})
}

// TestD383A4_ResolveLegsPrimaryGate A4 执行面：MT 腿解析 primary-only——
// 歧义态 fail-closed 拒单（宁拒单不撞车，497）；报价槽符号 ≠ 合格行 → 陈旧/变体报价拒绝；
// 合格行放行不误伤；CEX 腿不触发闸。
func TestD383A4_ResolveLegsPrimaryGate(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.0, Ask: 1.0001, Time: now, VenueSymbol: "EURUSDm"})
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "s", VenueType: "mt5", Bid: 1.0002, Ask: 1.0003, Time: now, VenueSymbol: "EURUSDm"})
	newSvc := func(resolver func(venueKey, canonical string) (string, error)) *Service {
		return &Service{
			qbus:            b,
			legContractSize: func(_, _ string) (float64, bool) { return 100, true },
			primarySymbol:   resolver,
		}
	}
	found := &pgstore.Opportunity{Canonical: "EURUSD", VenueBuy: "b:mt5", VenueSell: "s:mt5"}

	// 歧义态（卖腿 venue 多行零 primary）→ fail-closed
	s := newSvc(func(venueKey, _ string) (string, error) {
		if venueKey == "s:mt5" {
			return "", fmt.Errorf("symbol resolution ambiguous (s, EURUSD)")
		}
		return "EURUSDm", nil
	})
	if _, err := s.resolveLegs(found, 0.1); err == nil || !strings.Contains(err.Error(), "symbol resolution ambiguous") {
		t.Fatalf("err = %v, want 歧义 fail-closed（红=A2.4 缺失）", err)
	}

	// 报价槽符号 ≠ 合格行（陈旧/变体报价）→ 拒
	s = newSvc(func(venueKey, _ string) (string, error) {
		if venueKey == "b:mt5" {
			return "OTHERm", nil
		}
		return "EURUSDm", nil
	})
	if _, err := s.resolveLegs(found, 0.1); err == nil || !strings.Contains(err.Error(), "陈旧或变体") {
		t.Fatalf("err = %v, want 变体报价拒绝", err)
	}

	// 双腿合格 → 放行不误伤
	s = newSvc(func(_, _ string) (string, error) { return "EURUSDm", nil })
	legs, err := s.resolveLegs(found, 0.1)
	if err != nil || len(legs) != 2 {
		t.Fatalf("legs = %v/%v, want 2 腿放行（合格行不误伤）", legs, err)
	}

	// CEX 腿不触发闸（resolver 对 CEX venue 一调即炸）
	cexFound := &pgstore.Opportunity{Canonical: "XAUUSDT", VenueBuy: "binance:perp", VenueSell: "okx:perp"}
	b2 := bus.New()
	b2.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp", Bid: 4000, Ask: 4000.5, Time: now, VenueSymbol: "XAUUSDT"})
	b2.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 4000.2, Ask: 4000.7, Time: now, VenueSymbol: "XAU-USDT-SWAP"})
	s2 := &Service{
		qbus:            b2,
		legContractSize: func(_, _ string) (float64, bool) { return 1, true },
		cexContractSize: func(_, _ string) (float64, bool) { return 1, true },
		primarySymbol: func(_, _ string) (string, error) {
			t.Fatal("CEX 腿不应触发 primarySymbol 闸")
			return "", nil
		},
	}
	_, _ = s2.resolveLegs(cexFound, 0.1) // 仅证闸未对 CEX 腿触发（t.Fatal 即失败）；step sizing 结果与本锚无关
}
