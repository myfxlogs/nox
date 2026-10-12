// account_event_snapshot_test.go —— 3d-P1#1b：MT 余额帧事件 → 快照行（src=mt_frame）。
// 锚：MT 事件即写 / 1s 合流 pending 补写终值 / 同值不写 / CEX 键跳过 / ctx 退出。
package cexscan

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arb/internal/data/bus"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// fakeAcctStore orderEventStore mock：计数落库行 + keys/insert 失败注入。
type fakeAcctStore struct {
	mu        sync.Mutex
	rows      []pgstore.VenueAccountSnapshot
	keysErr   bool
	insertErr bool
}

func (f *fakeAcctStore) BrokerRealAccountKeys(context.Context) (map[string]bool, error) {
	if f.keysErr {
		return nil, errors.New("keys down")
	}
	return map[string]bool{"3001157": true}, nil
}

func (f *fakeAcctStore) InsertVenueAccountSnapshots(_ context.Context, rows []pgstore.VenueAccountSnapshot) error {
	if f.insertErr {
		return errors.New("insert down")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, rows...)
	return nil
}

func (f *fakeAcctStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func acctEvent(vk, assetType string) *v1.AccountEvent {
	return &v1.AccountEvent{Account: &v1.AccountInfo{VenueKey: vk, AssetType: assetType}}
}

func mtAcctInfos(eq float64) []*v1.AccountInfo {
	return []*v1.AccountInfo{
		{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Balance: 1000, Equity: eq, FreeMargin: eq - 10, Currency: "USD"},
	}
}

func waitRows(t *testing.T, s *fakeAcctStore, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.count() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("rows=%d, want ≥%d（%v 超时）", s.count(), want, d)
}

// TestAcctFrameSnapshot_WriteAndDedup (a)(c)：MT 帧 → 1 行 src=mt_frame；同值帧不再写。
func TestAcctFrameSnapshot_WriteAndDedup(t *testing.T) {
	topic := bus.NewTopic[*v1.AccountEvent]()
	store := &fakeAcctStore{}
	am := &fakeSnapshotter{infos: mtAcctInfos(1000)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { StartAccountEventSnapshots(ctx, topic, store, am); close(done) }()
	time.Sleep(50 * time.Millisecond) // 等订阅就位（Topic 无回放，publish 先于 Subscribe 会丢帧）
	topic.Publish(acctEvent("3001157:mt5", "cfd"))
	waitRows(t, store, 1, 2*time.Second)

	store.mu.Lock()
	r := store.rows[0]
	store.mu.Unlock()
	if r.Src != "mt_frame" || r.Venue != "3001157" || r.VenueType != "mt5" {
		t.Errorf("row = %+v, want src=mt_frame 3001157/mt5", r)
	}
	// 同值帧（节流窗外，1.1s 后）→ 不再写。
	time.Sleep(acctFrameWriteInterval + 100*time.Millisecond)
	topic.Publish(acctEvent("3001157:mt5", "cfd"))
	time.Sleep(200 * time.Millisecond)
	if n := store.count(); n != 1 {
		t.Errorf("同值帧 rows=%d, want 1", n)
	}
	cancel()
	<-done
}

// TestAcctFrameSnapshot_Coalesce (b)：300ms 内 5 帧值不同 → 首帧即写 + 1s 后补写终值 = 2 行。
func TestAcctFrameSnapshot_Coalesce(t *testing.T) {
	topic := bus.NewTopic[*v1.AccountEvent]()
	store := &fakeAcctStore{}
	am := &fakeSnapshotter{infos: mtAcctInfos(1000)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go StartAccountEventSnapshots(ctx, topic, store, am)
	time.Sleep(50 * time.Millisecond) // 等订阅就位

	topic.Publish(acctEvent("3001157:mt5", "cfd"))
	waitRows(t, store, 1, 2*time.Second)
	// 300ms 内连发 4 帧、值逐帧变化 → 窗内 pending，终值定时器补写。
	for i := 1; i <= 4; i++ {
		am.setInfos(mtAcctInfos(1000 + float64(i)))
		topic.Publish(acctEvent("3001157:mt5", "cfd"))
	}
	waitRows(t, store, 2, 2*time.Second)
	if n := store.count(); n != 2 {
		t.Fatalf("5 帧合流 rows=%d, want 2（首写+终值补写）", n)
	}
	store.mu.Lock()
	last := store.rows[len(store.rows)-1]
	store.mu.Unlock()
	if last.EquityRaw != 1004 {
		t.Errorf("补写终值 equity=%v, want 1004（末帧值）", last.EquityRaw)
	}
}

// TestAcctFrameSnapshot_CexSkip (d)：CEX venueKey 帧 → 0 行。
func TestAcctFrameSnapshot_CexSkip(t *testing.T) {
	topic := bus.NewTopic[*v1.AccountEvent]()
	store := &fakeAcctStore{}
	am := &fakeSnapshotter{infos: mtAcctInfos(1000)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go StartAccountEventSnapshots(ctx, topic, store, am)
	time.Sleep(50 * time.Millisecond) // 等订阅就位
	topic.Publish(acctEvent("okx:perp", "cex"))
	topic.Publish(acctEvent("binance:spot", "cex"))
	topic.Publish(acctEvent("binance:funding", "cex")) // 部署实证：非 :perp/:spot 后缀键曾被当 MT 误写
	topic.Publish(acctEvent("okx:unified", "cex"))
	time.Sleep(150 * time.Millisecond)
	if n := store.count(); n != 0 {
		t.Errorf("CEX 键应跳过 rows=%d, want 0", n)
	}
}
