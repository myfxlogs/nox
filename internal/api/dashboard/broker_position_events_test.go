// broker_position_events_test.go D-583：flush 快照段 orders 深拷回归。
// 根因：flush 锁内浅拷 venues（orders map 引用共享）→ 锁外 :52 读 vs.orders，
// 同期 OnMT5Frame 锁内原地写同一张 map → concurrent map read and map write fatal。
package dashboard

import (
	"context"
	"nox/internal/data/venue"
	"testing"
	"time"
)

// seedMT5Venue 直塞 venueState 已知票号（OnMT5Frame 仅 patch 已知票，未知票走 kick 回读）。
func seedMT5Venue(m *BrokerPositionMonitor, vk, ticket string) {
	m.mu.Lock()
	m.venues[vk] = venueState{orders: map[string]venue.Order{
		ticket: {Ticket: ticket, Symbol: "TRXUSDT", Venue: "testvenue", VenueKey: vk, Side: "SELL", Volume: 0.01, OpenPrice: 0.332},
	}}
	m.mu.Unlock()
}

// TestFlushOrdersRace race 压测：A 循环 OnMT5Frame 锁内原地写 orders（生产帧率每秒十余次），
// B 循环 flush 锁外读快照（250ms 合批尾沿）——深拷缺失时 -race 必报 DATA RACE。
func TestFlushOrdersRace(t *testing.T) {
	vk := "testvenue:mt5"
	m, _, _ := newMonTest(t, &fakeVenueQuerier{key: vk})
	seedMT5Venue(m, vk, "111")
	oldDelay := bpFlushDelay
	bpFlushDelay = time.Hour // 压测期只由 B 协程直调 flush，尾沿定时器不并发点火
	defer func() { bpFlushDelay = oldDelay }()

	// 先脏化一轮：dirty 空=flush 快照段前置早退，压测咬合不了读写窗口。
	m.OnMT5Frame(vk, []venue.Order{{
		Ticket: "111", Symbol: "TRXUSDT", Venue: "testvenue", VenueKey: vk,
		Side: "SELL", Volume: 0.01, OpenPrice: 0.332, Profit: 1,
	}})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			m.OnMT5Frame(vk, []venue.Order{{
				Ticket: "111", Symbol: "TRXUSDT", Venue: "testvenue", VenueKey: vk,
				Side: "SELL", Volume: 0.01, OpenPrice: 0.332, Profit: float64(i),
			}})
		}
	}()
	for {
		select {
		case <-done:
			m.flush(context.Background()) // 收尾一轮：A 末次写入对 B 可见
			return
		default:
			m.flush(context.Background())
		}
	}
}

// TestFlushSnapshotDeepCopy 深拷内容双证：快照后写侧原地改/新增票号、快照侧改写，
// 两侧互不串面（orders 私有副本；canon/cs 整体替换语义引用直拷即可）。
func TestFlushSnapshotDeepCopy(t *testing.T) {
	vk := "testvenue:mt5"
	m, _, _ := newMonTest(t, &fakeVenueQuerier{key: vk})
	seedMT5Venue(m, vk, "111")

	m.mu.Lock()
	snap := m.snapshotVenuesLocked()
	m.mu.Unlock()

	// 写侧原地改已知票 + 新增票号（OnMT5Frame 同款 map 写路径）——快照不受影响。
	m.mu.Lock()
	m.venues[vk].orders["111"] = venue.Order{Ticket: "111", Symbol: "TRXUSDT", VenueKey: vk, Profit: 9.9}
	m.venues[vk].orders["222"] = venue.Order{Ticket: "222", Symbol: "TRXUSDT", VenueKey: vk, Profit: 2.2}
	m.mu.Unlock()

	snapVs := snap[vk]
	if got := snapVs.orders["111"].Profit; got != 0 {
		t.Errorf("snapshot order 111 want untouched Profit=0, got %v", got)
	}
	if _, ok := snapVs.orders["222"]; ok {
		t.Error("snapshot must not see ticket 222 added after snapshot")
	}

	// 反向：改快照副本不影响原 map（私有副本非共享引用）。
	snapVs.orders["111"] = venue.Order{Ticket: "111", Profit: -7.7}
	m.mu.Lock()
	live := m.venues[vk].orders["111"].Profit
	m.mu.Unlock()
	if live != 9.9 {
		t.Errorf("live order 111 want 9.9 after snapshot-side write, got %v", live)
	}
}
