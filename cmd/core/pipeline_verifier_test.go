// pipeline_verifier_test.go —— D-108 P1 R1：pipelineVerifier 单测（先红后绿）。
// 覆盖：adapter 映射（venue 命中转 4 字段）/ venue 缺失 error / querier 超时或 err 透传 / set 刷新后新 querier 生效。
package main

import (
	"context"
	"errors"
	"nox/internal/data/venue"
	"sync"
	"testing"
	"time"
)

// fakeQuerier 测试用 venue.Querier。
type fakeQuerier struct {
	venueKey string
	orders   []venue.Order
	err      error
	delay    time.Duration
}

func (q *fakeQuerier) VenueKey() string { return q.venueKey }
func (q *fakeQuerier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	if q.delay > 0 {
		select {
		case <-time.After(q.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return q.orders, q.err
}

// TestD108R1_VenueHit 映射 4 字段（Ticket/Symbol/Side/Volume）。
func TestD108R1_VenueHit(t *testing.T) {
	pv := newPipelineVerifier()
	pv.set([]venue.Querier{
		&fakeQuerier{venueKey: "vA:mt5", orders: []venue.Order{
			{Ticket: "T100", Symbol: "AUDJPY", Side: "BUY", Volume: 1.0},
			{Ticket: "T200", Symbol: "EURUSD", Side: "SELL", Volume: 0.5},
		}},
	})
	orders, err := pv.QueryOpenedOrders(context.Background(), "vA:mt5")
	if err != nil {
		t.Fatalf("venue hit: unexpected err: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("expected 2 orders, got %d", len(orders))
	}
	if orders[0].Ticket != "T100" || orders[0].Symbol != "AUDJPY" || orders[0].Side != "BUY" || orders[0].Volume != 1.0 {
		t.Errorf("order[0] mismatch: %+v", orders[0])
	}
	if orders[1].Ticket != "T200" || orders[1].Symbol != "EURUSD" || orders[1].Side != "SELL" || orders[1].Volume != 0.5 {
		t.Errorf("order[1] mismatch: %+v", orders[1])
	}
}

// TestD108R1_VenueMissing venue 缺失 → error。
func TestD108R1_VenueMissing(t *testing.T) {
	pv := newPipelineVerifier()
	pv.set([]venue.Querier{
		&fakeQuerier{venueKey: "vA:mt5", orders: nil},
	})
	_, err := pv.QueryOpenedOrders(context.Background(), "vX:mt5")
	if err == nil {
		t.Fatal("venue missing: expected error, got nil")
	}
}

// TestD108R1_QuerierError querier 返回 err → 透传。
func TestD108R1_QuerierError(t *testing.T) {
	pv := newPipelineVerifier()
	pv.set([]venue.Querier{
		&fakeQuerier{venueKey: "vA:mt5", err: errors.New("grpc unavailable")},
	})
	_, err := pv.QueryOpenedOrders(context.Background(), "vA:mt5")
	if err == nil {
		t.Fatal("querier error: expected error, got nil")
	}
}

// TestD108R1_QuerierTimeout querier 超时 → per-call 10s 超时截断（用短 ctx 验证透传）。
func TestD108R1_QuerierTimeout(t *testing.T) {
	pv := newPipelineVerifier()
	pv.set([]venue.Querier{
		&fakeQuerier{venueKey: "vA:mt5", delay: 5 * time.Second},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := pv.QueryOpenedOrders(ctx, "vA:mt5")
	if err == nil {
		t.Fatal("querier timeout: expected error, got nil")
	}
}

// TestD108R1_SetRefresh set 刷新后新 querier 生效（旧 venue 消失，新 venue 可查）。
func TestD108R1_SetRefresh(t *testing.T) {
	pv := newPipelineVerifier()
	pv.set([]venue.Querier{
		&fakeQuerier{venueKey: "vA:mt5", orders: []venue.Order{{Ticket: "T1"}}},
	})
	// 初始 vA 可查
	orders, err := pv.QueryOpenedOrders(context.Background(), "vA:mt5")
	if err != nil || len(orders) != 1 {
		t.Fatalf("initial vA: expected 1 order, got %d err=%v", len(orders), err)
	}
	// 刷新：vA 移除，vB 加入
	pv.set([]venue.Querier{
		&fakeQuerier{venueKey: "vB:mt5", orders: []venue.Order{{Ticket: "T2"}}},
	})
	// vA 应缺失
	_, err = pv.QueryOpenedOrders(context.Background(), "vA:mt5")
	if err == nil {
		t.Fatal("after refresh: vA should be missing, got nil err")
	}
	// vB 应可查
	orders, err = pv.QueryOpenedOrders(context.Background(), "vB:mt5")
	if err != nil || len(orders) != 1 || orders[0].Ticket != "T2" {
		t.Fatalf("after refresh: vB expected T2, got %d err=%v", len(orders), err)
	}
}

// TestD108R1_ConcurrentSetQuery 并发 set × Query → 不 panic（RWMutex 保护）。
func TestD108R1_ConcurrentSetQuery(t *testing.T) {
	pv := newPipelineVerifier()
	var wg sync.WaitGroup
	// 并发 set
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			pv.set([]venue.Querier{
				&fakeQuerier{venueKey: "vA:mt5", orders: []venue.Order{{Ticket: "T"}}},
			})
		}
	}()
	// 并发 query
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			pv.QueryOpenedOrders(context.Background(), "vA:mt5")
		}
	}()
	wg.Wait()
}
