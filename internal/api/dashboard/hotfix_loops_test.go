// hotfix_loops_test.go —— 热修 hotfix-loops：kick 防抖 + CEX querier 配置哈希缓存
// （OOM 19:01 实证：帧风暴 kick→poll 恶性循环 + 每 poll 全量重建 querier）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// hotfixExchStore 可变配置 fake（querier 缓存测试：改配置触发重建）。
type hotfixExchStore struct {
	cfgs []pgstore.ExchangeConfig
}

func (f *hotfixExchStore) ListExchangeConfigs(context.Context) ([]pgstore.ExchangeConfig, error) {
	return f.cfgs, nil
}

// TestStreamFrameKick_Debounced nil-acct 连续 20 帧 <100ms → kick 只计 1 次
// （frameKickAt[vk] 首记后不被覆盖 = 后续帧全部被防抖拦截）。
func TestStreamFrameKick_Debounced(t *testing.T) {
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := NewAccountMonitor(bm, nil)
	vk := "1001"
	now := time.Now()
	m.ObserveStreamAccount(vk, 1, 1, 0, 1, 0, 0, "", now)
	m.mu.RLock()
	first := m.frameKickAt[vk]
	m.mu.RUnlock()
	if first.IsZero() {
		t.Fatal("first frame: kick not scheduled")
	}
	for i := 0; i < 19; i++ {
		// 模拟 REST 回读持续失败：acct 始终 nil（帧来即回 nil-acct 分支）。
		m.mu.Lock()
		delete(m.states, vk)
		m.mu.Unlock()
		m.ObserveStreamAccount(vk, 1, 1, 0, 1, 0, 0, "", now)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.frameKickAt[vk].Equal(first) {
		t.Fatalf("frameKickAt overwritten: debounce failed（20 帧应只计 1 次 kick）")
	}
	if len(m.pendingKick) != 1 {
		t.Fatalf("pendingKick=%d, want 1", len(m.pendingKick))
	}
}

// TestStreamFrameKick_RekickAfterWindow 超防抖窗后可再 kick（节流不免疫复发）。
func TestStreamFrameKick_RekickAfterWindow(t *testing.T) {
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := NewAccountMonitor(bm, nil)
	m.mu.Lock()
	m.frameKickAt["k1"] = time.Now().Add(-acctFrameKickMinInterval)
	m.mu.Unlock()
	if !m.kickDebounced("k1", acctFrameKickMinInterval, m.frameKickAt) {
		t.Fatal("past window: kick should fire")
	}
	if m.kickDebounced("k1", acctFrameKickMinInterval, m.frameKickAt) {
		t.Fatal("within window: kick should be debounced")
	}
}

// TestQuerierCache_SameConfigReuses 配置未变：两次 queriers() 只重建一次（构造计数 1）。
func TestQuerierCache_SameConfigReuses(t *testing.T) {
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := NewAccountMonitor(bm, nil)
	m.SetExchangeStore(&hotfixExchStore{cfgs: []pgstore.ExchangeConfig{
		{ID: 1, Venue: "okx", VenueType: "perp", APIKey: "k", APISecret: "s"},
	}})
	ctx := context.Background()
	before := cexQuerierBuildTotal.Load()
	m.queriers(ctx)
	m.queriers(ctx)
	if d := cexQuerierBuildTotal.Load() - before; d != 1 {
		t.Fatalf("rebuilds=%d, want 1（配置未变应复用缓存）", d)
	}
}

// TestQuerierCache_ConfigChangeRebuilds 配置内容变化 → 下一次 queriers() 重建。
func TestQuerierCache_ConfigChangeRebuilds(t *testing.T) {
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := NewAccountMonitor(bm, nil)
	st := &hotfixExchStore{cfgs: []pgstore.ExchangeConfig{
		{ID: 1, Venue: "okx", VenueType: "perp", APIKey: "k", APISecret: "s"},
	}}
	m.SetExchangeStore(st)
	ctx := context.Background()
	before := cexQuerierBuildTotal.Load()
	m.queriers(ctx)
	st.cfgs = append(st.cfgs, pgstore.ExchangeConfig{ID: 2, Venue: "binance", VenueType: "spot", APIKey: "k", APISecret: "s"})
	m.queriers(ctx)
	if d := cexQuerierBuildTotal.Load() - before; d != 2 {
		t.Fatalf("rebuilds=%d, want 2（配置变更应重建）", d)
	}
}
