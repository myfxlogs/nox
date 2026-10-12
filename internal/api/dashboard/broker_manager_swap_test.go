package dashboard

import (
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"nox/internal/data/adapter"
	"nox/internal/data/fact"
)

// TestSwapFacts_ColdStartReturnsFacts R-c1：venue 返回 (facts, ErrSwapColdStart) →
// facts 仍入聚合结果 + hasCold 信号生效（bound 内整轮重试返回 nil+err）。
func TestSwapFacts_ColdStartReturnsFacts(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	m := &BrokerManager{
		running: make(map[string]*running),
	}
	// venue A：返回 (facts, ErrSwapColdStart)——冷符号但其余 facts 非空
	coldFacts := []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vA", Symbol: "AUDJPY", Value: -3, Src: "mt5"},
	}
	m.running["vA"] = &running{
		swap: func(_ context.Context) ([]fact.Fact, error) {
			return coldFacts, adapter.ErrSwapColdStart
		},
	}
	// venue B：正常返回
	normalFacts := []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vB", Symbol: "ETHUSD", Value: 1, Src: "mt5"},
	}
	m.running["vB"] = &running{
		swap: func(_ context.Context) ([]fact.Fact, error) {
			return normalFacts, nil
		},
	}

	// 首次调用：hasCold=true + coldStartSince=zero → retryCold=true（bound 内）
	// → 返回 nil, err（整轮重试，重复采集无害）
	got, err := m.SwapFacts(context.Background())
	if got != nil {
		t.Fatalf("bound 内应返回 nil facts（整轮重试），got %d facts", len(got))
	}
	if err == nil {
		t.Fatal("bound 内应返回 err（cold start retry）")
	}
	if !strings.Contains(err.Error(), "cold start") {
		t.Fatalf("err 应含 cold start 信号，got %v", err)
	}

	// 模拟 bound 过期：手动回拨 coldStartSince 到 61s 前
	m.mu.Lock()
	m.coldStartSince = time.Now().Add(-61 * time.Second)
	m.mu.Unlock()

	// bound 后：hasCold=true + retryCold=false → facts 全部落地
	got, err = m.SwapFacts(context.Background())
	if err != nil {
		t.Fatalf("bound 后应无 err（接受部分），got %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("bound 后应聚合 3 facts（vA 2 + vB 1），got %d", len(got))
	}
}

// TestSwapFacts_ColdStartOnlyColdVenue R-c1：仅一个 venue 冷启动，bound 后冷 venue 的 facts 也落地（不丢弃）。
func TestSwapFacts_ColdStartOnlyColdVenue(t *testing.T) {
	log.SetOutput(os.NewFile(0, os.DevNull))
	defer log.SetOutput(os.Stderr)

	m := &BrokerManager{
		running:        make(map[string]*running),
		coldStartSince: time.Now().Add(-61 * time.Second), // 已过 bound
	}
	coldFacts := []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "X", Value: 1, Src: "mt5"},
	}
	m.running["vA"] = &running{
		swap: func(_ context.Context) ([]fact.Fact, error) {
			return coldFacts, adapter.ErrSwapColdStart
		},
	}

	got, err := m.SwapFacts(context.Background())
	if err != nil {
		t.Fatalf("bound 后应无 err，got %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("冷 venue facts 应落地 1 条，got %d", len(got))
	}
	if got[0].Venue != "vA" {
		t.Errorf("fact venue 应 vA，got %s", got[0].Venue)
	}
}
