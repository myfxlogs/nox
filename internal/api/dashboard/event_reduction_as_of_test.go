// d094_bpm_stream_test.go —— D-094 锚：事件归约 asOf 透传、样本行 Σ 排除、boot 单次
// 初始拉取后零轮询（D-004 唯一例外面）。断流保留/venue_online 由 D-052 锚
// （OfflineKeepsSnapshot）覆盖不重复。
package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/venue"

	v1 "nox/internal/gen/dashboardv1"
)

// TestD094_EventReductionAsOf 事件归约锚：帧 patch 两帧同仓（pnl 变化）→ Snapshot
// 行值=末帧、AsOf=帧到达时戳（proto 透传非零）。
func TestD094_EventReductionAsOf(t *testing.T) {
	ctx := context.Background()
	oldDelay := bpFlushDelay
	bpFlushDelay = 5 * time.Millisecond
	t.Cleanup(func() { bpFlushDelay = oldDelay })

	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []venue.Order{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "TRXUSDT", Side: "SELL", Volume: 0.01, Profit: 1},
	}}
	m, _, _ := newMonTest(t, q)
	m.Refresh(ctx) // boot：建仓 ticket=1（已知票号，后续帧 patch 生效）

	// 两帧 patch：pnl 2 → 3；asOf=帧到达时刻。
	m.OnMT5Frame("testvenue:mt5", []venue.Order{{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "TRXUSDT", Side: "SELL", Volume: 0.01, Profit: 2}})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := m.Snapshot(); len(got) == 1 && got[0].Pnl == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("第一帧未归约: %+v", m.Snapshot())
		}
		time.Sleep(2 * time.Millisecond)
	}
	m.OnMT5Frame("testvenue:mt5", []venue.Order{{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "TRXUSDT", Side: "SELL", Volume: 0.01, Profit: 3}})
	deadline = time.Now().Add(2 * time.Second)
	var pb *v1.BrokerPosition
	for {
		got := m.Snapshot()
		if len(got) == 1 && got[0].Pnl == 3 {
			pb = got[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("末帧未归约（Snapshot 行值应=末帧 pnl=3）: %+v", got)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if pb.AsOf == nil || time.Since(pb.AsOf.AsTime()) > 5*time.Second {
		t.Fatalf("AsOf 应=帧到达时戳（非零且新鲜）: %v", pb.AsOf)
	}
}

// TestD094_SampleRowExcludedFromSums 样本排除锚：孤儿行（position_id=0）IsSample=true，
// brokerSums 排除其 pnl/swap/commission；行本身仍在列表。
func TestD094_SampleRowExcludedFromSums(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "3001157:mt5", Ticket: "1", Pnl: 0.31, Swap: 0.01, Commission: 0},
		{VenueKey: "binance:spot", Ticket: "bin-0.002", Pnl: 4.48, Swap: 0, Commission: 0}, // 孤儿（收息样本）
	}
	annotateWithIndex(bps, map[string]int64{"3001157:mt5\x001": 509}, map[string]string{"3001157:mt5\x001": "o1"})
	if bps[0].IsSample {
		t.Fatal("关联系统仓的行不应标样本")
	}
	if !bps[1].IsSample {
		t.Fatal("孤儿行（position_id=0）应标样本")
	}
	ps, ns := brokerSums(bps)
	if ps == nil || *ps != 0.31 || ns == nil || *ns != 0.32 {
		t.Fatalf("Σ 应排除样本行（pnlSum=0.31 netSum=0.32）: %v %v", ps, ns)
	}
	if len(bps) != 2 {
		t.Fatalf("样本行仍应在列表内: %d", len(bps))
	}
}

// TestD094_BootSinglePullNoPoll boot 初始拉取锚：Run 冷启后权威回读恰一次，
// 后续事件等待期零轮询调用（D-004：初始同步非定时刷新）。
func TestD094_BootSinglePullNoPoll(t *testing.T) {
	q := &fakeVenueQuerier{key: "testvenue:mt5", orders: []venue.Order{
		{Venue: "testvenue", VenueKey: "testvenue:mt5", Ticket: "1", Symbol: "TRXUSDT", Side: "SELL", Volume: 0.01, Profit: 1},
	}}
	m, _, _ := newMonTest(t, q)

	ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	time.Sleep(400 * time.Millisecond) // 跨多个既有轮询周期量级——若残留周期刷新必 >1
	if calls := q.calls.Load(); calls != 1 {
		t.Fatalf("boot 应只回读一次（后续零轮询）, calls=%d", calls)
	}
	cancel()
	<-done
}
