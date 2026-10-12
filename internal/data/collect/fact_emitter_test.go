// fact_emitter_test.go —— D-509 S1：FactEmitter 去重+心跳节流（真库 + 单测双档）。
package collect

import (
	"context"
	"sync"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// fakeSink 记录每次调用（批内 facts 拷贝）。
type fakeSink struct {
	mu    sync.Mutex
	calls [][]fact.Fact
}

func (f *fakeSink) sink(_ context.Context, fs []fact.Fact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]fact.Fact{}, fs...))
	return nil
}

func TestFactEmitter_DedupAndHeartbeat(t *testing.T) {
	fk := &fakeSink{}
	now := time.Now()
	cur := now
	e := NewFactEmitter(fk.sink)
	e.now = func() time.Time { return cur }
	ctx := context.Background()
	f := func(v float64) []fact.Fact {
		return []fact.Fact{{Kind: fact.KindFunding, Venue: "binance", Symbol: "BTCUSDT", Value: v}}
	}

	if err := e.Emit(ctx, f(1.5)); err != nil { // 首发：过
		t.Fatal(err)
	}
	if err := e.Emit(ctx, f(1.5)); err != nil { // 同值 30s：丢
		t.Fatal(err)
	}
	cur = now.Add(30 * time.Second)
	if err := e.Emit(ctx, f(1.5)); err != nil {
		t.Fatal(err)
	}
	cur = now.Add(130 * time.Second)
	if err := e.Emit(ctx, f(1.5)); err != nil { // 心跳到期：过
		t.Fatal(err)
	}
	if err := e.Emit(ctx, f(2.0)); err != nil { // 值变：即发
		t.Fatal(err)
	}
	if len(fk.calls) != 3 {
		t.Fatalf("calls=%d want 3（首发+心跳+值变）", len(fk.calls))
	}
}

// TestFactEmitter_RealSink 真库链路：同值节流+值变入库（NOX_TEST_PG_DSN 门控）。
func TestFactEmitter_RealSink(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM facts WHERE kind='funding' AND venue='d509emit'`); err != nil {
		t.Fatal(err)
	}

	e := NewFactEmitter(st.InsertFacts)
	sym := "D509USDT"
	emit := func(v float64) {
		t.Helper()
		if err := e.Emit(ctx, []fact.Fact{{Kind: fact.KindFunding, Venue: "d509emit", Symbol: sym,
			Value: v, Unit: fact.UnitPctAnnualized, Ts: time.Now(), Src: "test"}}); err != nil {
			t.Fatal(err)
		}
	}
	emit(1.5)
	emit(1.5) // 同值节流：不入库
	emit(2.5) // 值变：入库
	var cnt int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM facts WHERE kind='funding' AND venue='d509emit' AND symbol=$1`, sym).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 2 {
		t.Fatalf("rows=%d want 2", cnt)
	}
	// 心跳分支：时钟前移 130s 后同值重发入库。
	e.now = func() time.Time { return time.Now().Add(130 * time.Second) }
	emit(2.5)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM facts WHERE kind='funding' AND venue='d509emit' AND symbol=$1`, sym).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 3 {
		t.Fatalf("rows=%d want 3（心跳补发）", cnt)
	}
}
