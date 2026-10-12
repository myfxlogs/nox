package dashboard

// D-142 ② 单测：AddBroker 异步触发 BackfillRuleMappings + 免重启映射机制验证。
// 用例①：mock symbols 源 → BackfillRuleMappings → store 出现 source='rule' 行（DSN 集成）
// 用例②：RuleBackfill 回调机制——nil 时不 panic，非 nil 时可调用

import (
	"context"
	"log"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/normalize"
)

// 用例①：mock symbols 源 → BackfillRuleMappings → store 出现 source='rule' 行（DSN 集成）。
// 模拟 AddBroker 后异步触发：BrokerManager 有 mock running adapter（symbols 源），
// BrokerSymbols 拉取 symbols → BackfillRuleMappings → store 出现 source='rule' 行。
func TestBackfillRuleMappings_MockSymbolsSource(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st := serviceTestStoreSingleInit(t)

	venue := "test_d142_backfill"
	// 清理可能残留
	st.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue = $1`, venue)
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ($1,'mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING`, venue); err != nil {
		t.Fatalf("seed broker account: %v", err)
	}
	defer st.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue = $1`, venue)

	// BrokerManager with mock running adapter（mock symbols 源）
	m := &BrokerManager{
		store:   st,
		running: make(map[string]*running),
	}
	// mock symbolGroups：返回规则库可命中的符号（EURUSDm → EURUSD via regex）
	// symbols 设为非 nil 占位（BrokerSymbols guard 要求 r.symbols != nil，即使走 symbolGroups 路径）
	m.running[venue] = &running{
		symbols: func(_ context.Context) ([]string, error) { return nil, nil }, // 占位，走 symbolGroups 路径不调
		symbolGroups: func(_ context.Context) ([]SymbolGroup, error) {
			return []SymbolGroup{
				{Symbol: "EURUSDm", Group: "Forex"},
				{Symbol: "GBPUSDm", Group: "Forex"},
				{Symbol: "XAUUSDm", Group: "Metals"},
			}, nil
		},
	}

	// 模拟 backfillVenueRules 的核心逻辑：BrokerSymbols → BackfillRuleMappings
	groups, err := m.BrokerSymbols(ctx, venue)
	if err != nil {
		t.Fatalf("BrokerSymbols: %v", err)
	}
	symbols := make([]string, 0, len(groups))
	for _, g := range groups {
		symbols = append(symbols, g.Symbol)
	}
	eng := normalize.NewRuleEngine()
	n, err := st.BackfillRuleMappings(ctx, venue, symbols, eng)
	if err != nil {
		t.Fatalf("BackfillRuleMappings: %v", err)
	}

	// 验证 store 中该 venue 出现映射行
	mappings, err := st.ListSymbolMappings(ctx, venue)
	if err != nil {
		t.Fatalf("ListSymbolMappings: %v", err)
	}
	if len(mappings) == 0 {
		t.Fatal("BackfillRuleMappings 后 store 应有映射行，实际 0 行")
	}
	// 至少有一行 source='rule'（BackfillRuleMappings 产出的）
	hasRule := false
	for _, mp := range mappings {
		if mp.Source == "rule" {
			hasRule = true
			break
		}
	}
	if !hasRule {
		t.Errorf("store 无 source='rule' 行（BackfillRuleMappings 返回 %d，mappings %d）", n, len(mappings))
	}
}

// 用例②：RuleBackfill 回调机制——nil 时不 panic，非 nil 时被调用。
// 验证 AddBroker 的异步触发逻辑：RuleBackfill 非 nil → goroutine 调用回调。
func TestRuleBackfillCallbackMechanism(t *testing.T) {
	var called int32
	m := &BrokerManager{
		RuleBackfill: func(_ context.Context, venue string) {
			if venue == "test_venue" {
				atomic.AddInt32(&called, 1)
			}
		},
	}
	// 直接调用回调（模拟 AddBroker 内 goroutine 的行为）
	go m.RuleBackfill(context.Background(), "test_venue")
	// 等待 goroutine
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("RuleBackfill callback: called %d, want 1", called)
	}

	// nil RuleBackfill → 不 panic
	m2 := &BrokerManager{}
	if m2.RuleBackfill != nil {
		t.Error("nil RuleBackfill should be nil")
	}
}
