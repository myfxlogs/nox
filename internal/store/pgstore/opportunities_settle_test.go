// FundingSettleAvgs 单测（D-318 T1）：窗口过滤+均值+期数；无数据 symbol 无键。
package pgstore

import (
	"context"
	"math"
	"testing"
	"time"

	"nox/internal/data/fact"
)

func settleAvgsTestStore(t *testing.T) *Store {
	t.Helper()
	st := sharedTestStore(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("TRUNCATE facts: %v", err)
	}
	return st
}

func TestFundingSettleAvgs_WindowAndCount(t *testing.T) {
	st := settleAvgsTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rows := []fact.Fact{
		{Kind: "funding_history", Venue: "vtest", Symbol: "BTCTEST", Value: 2, Unit: fact.UnitPctAnnualized, Ts: now.Add(-1 * time.Hour), Src: "test"},
		{Kind: "funding_history", Venue: "vtest", Symbol: "BTCTEST", Value: 4, Unit: fact.UnitPctAnnualized, Ts: now.Add(-2 * time.Hour), Src: "test"},
		{Kind: "funding_history", Venue: "vtest", Symbol: "BTCTEST", Value: 6, Unit: fact.UnitPctAnnualized, Ts: now.Add(-3 * time.Hour), Src: "test"},
		// 窗外（>7d）→ 不参与均值/计数
		{Kind: "funding_history", Venue: "vtest", Symbol: "BTCTEST", Value: 100, Unit: fact.UnitPctAnnualized, Ts: now.Add(-8 * 24 * time.Hour), Src: "test"},
	}
	if err := st.InsertFacts(ctx, rows); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	avgs, err := st.FundingSettleAvgs(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("FundingSettleAvgs: %v", err)
	}
	sa, ok := avgs["vtest"]["BTCTEST"]
	if !ok {
		t.Fatal("T1: want key vtest/BTCTEST, got none")
	}
	if math.Abs(sa.Avg-4.0) > 1e-9 {
		t.Errorf("T1: avg want 4.0 (窗内 2/4/6 均值), got %v", sa.Avg)
	}
	if sa.N != 3 {
		t.Errorf("T1: N want 3 (窗外 100 不计), got %d", sa.N)
	}
	if _, ok := avgs["vtest"]["NOPESYM"]; ok {
		t.Errorf("T1: missing symbol want no key, got one")
	}
}
