// order_event_snapshot_test.go —— D-383 B1：order-event → 该 venue 定向落库快照。
// 锚：src=mt_event+fetched_at=事件时刻（B1）、5s/venue 收合（B1）、per-venue 键隔离（B4）、
// realKeys 失败 fail-closed（B1）、写失败返回 error 不留毒态（B1 不杀循环的存储面）、
// 并发风暴 race 面（B4）、调度路径 src 默认 scheduler（迁移 0121 语义）。
package cexscan

import (
	"context"
	"sync"
	"testing"
	"time"

	"arb/internal/config"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

func d383Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE fin_venue_account_snapshot`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	// 证真键集来自真库（BrokerRealAccountKeys 读 broker_accounts）——播种两 real 账户。
	if _, err := s.Pool().Exec(context.Background(), `
		TRUNCATE broker_accounts CASCADE;
		INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real) VALUES
			('3001157', 'mt5', 'h', 1, 'p', 's', 'connected', true),
			('15032313', 'mt5', 'h', 2, 'p', 's', 'connected', true)`); err != nil {
		t.Fatalf("seed broker_accounts: %v", err)
	}
	return s
}

func d383Scanner(store *pgstore.Store, infos []*v1.AccountInfo) *Scanner {
	return NewScanner(&config.Config{}, store, &fakeSnapshotter{infos: infos}, nil)
}

func countSnapshots(t *testing.T, s *pgstore.Store, src string) (int, time.Time) {
	t.Helper()
	var n int
	var fetched time.Time
	err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*), COALESCE(max(fetched_at), '1970-01-01') FROM fin_venue_account_snapshot WHERE src = $1`,
		src).Scan(&n, &fetched)
	if err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	return n, fetched
}

// TestD383B1_OrderEventSnapshotRow B1：事件 → src=mt_event 行（fetched_at=事件时刻）+
// 5s 收合 + per-venue 隔离 + 调度路径行 src=scheduler。
func TestD383B1_OrderEventSnapshotRow(t *testing.T) {
	s := d383Store(t)
	ctx := context.Background()
	infos := []*v1.AccountInfo{
		{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Balance: 1000, Equity: 1000, Currency: "USD"},
		{Venue: "15032313", VenueType: "mt5", AssetType: "cfd", Balance: 2000, Equity: 2000, Currency: "USD"},
	}
	sc := d383Scanner(s, infos)
	realKeys := map[string]bool{"3001157": true, "15032313": true} // 两 venue 均 real

	t0 := time.Now().UTC()
	if err := sc.OnOrderEvent(ctx, "3001157", t0); err != nil {
		t.Fatalf("on order event: %v", err)
	}
	n, fetched := countSnapshots(t, s, "mt_event")
	if n != 1 {
		t.Fatalf("mt_event rows = %d, want 1（红=事件落库缺失）", n)
	}
	if fetched.Sub(t0) > 2*time.Second || fetched.Sub(t0) < -2*time.Second {
		t.Errorf("fetched_at = %v, want ≈事件时刻 %v", fetched, t0)
	}
	var venue, vt string
	if err := s.Pool().QueryRow(ctx,
		`SELECT venue, venue_type FROM fin_venue_account_snapshot WHERE src = 'mt_event'`).Scan(&venue, &vt); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if venue != "3001157" || vt != "mt5" {
		t.Errorf("row = %s/%s, want 3001157/mt5（定向落库+复用行口径归一）", venue, vt)
	}

	// 5s 收合：同 venue 间隔内事件静默跳过（红=收合缺失）
	if err := sc.OnOrderEvent(ctx, "3001157", t0.Add(time.Second)); err != nil {
		t.Fatalf("on order event 2: %v", err)
	}
	if n, _ = countSnapshots(t, s, "mt_event"); n != 1 {
		t.Fatalf("mt_event rows = %d, want 1（同 venue 5s 内应收合）", n)
	}

	// per-venue 隔离：venue B 首发不受 venue A lastWrite 影响
	if err := sc.OnOrderEvent(ctx, "15032313", t0.Add(2*time.Second)); err != nil {
		t.Fatalf("on order event venue B: %v", err)
	}
	if n, _ = countSnapshots(t, s, "mt_event"); n != 2 {
		t.Fatalf("mt_event rows = %d, want 2（键级隔离）", n)
	}

	// 间隔过后同 venue 再发 → 新行（间隔≥5s 生效）
	if err := sc.OnOrderEvent(ctx, "3001157", t0.Add(6*time.Second)); err != nil {
		t.Fatalf("on order event 3: %v", err)
	}
	if n, _ = countSnapshots(t, s, "mt_event"); n != 3 {
		t.Fatalf("mt_event rows = %d, want 3（5s 间隔后放行）", n)
	}

	// 证真失败 venue：realKeys 无 99999999 → 其行被 D-360 口径滤除（复用 buildVenueSnapshotRows）
	if _, err := s.Pool().Exec(ctx, `DELETE FROM fin_venue_account_snapshot WHERE src='mt_event'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if err := sc.OnOrderEvent(ctx, "99999999", t0.Add(7*time.Second)); err != nil {
		t.Fatalf("on order event unreal: %v", err)
	}
	if n, _ = countSnapshots(t, s, "mt_event"); n != 0 {
		t.Fatalf("mt_event rows = %d, want 0（非 real venue 定向落库应被证真口径滤除）", n)
	}

	// 调度路径回归：空 Src → 落库 src=scheduler（migration 0121 列默认+写侧回退同值）
	rows := buildVenueSnapshotRows([]AccountSnapshot{{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Ts: t0}}, realKeys)
	if err := s.InsertVenueAccountSnapshots(ctx, rows); err != nil {
		t.Fatalf("scheduler insert: %v", err)
	}
	if n, _ = countSnapshots(t, s, "scheduler"); n != 1 {
		t.Fatalf("scheduler rows = %d, want 1（调度路径默认归 scheduler）", n)
	}
}

// TestD383B1_FailClosedAndWriteFail B1：realKeys 查询失败 fail-closed 不落；insert 失败
// 返回 error（事件循环记日志不杀循环的存储面）且计数态一致可续。
func TestD383B1_FailClosedAndWriteFail(t *testing.T) {
	ctx := context.Background()
	am := &fakeSnapshotter{infos: []*v1.AccountInfo{
		{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Balance: 1, Currency: "USD"},
	}}
	ts := time.Now().UTC()

	// realKeys 查询失败 → fail-closed 返回 error，零落行
	if err := writeVenueEventSnapshot(ctx, &fakeOrderStore{keysErr: true}, am, "3001157", ts); err == nil {
		t.Fatal("keys 失败应返回 error（fail-closed）")
	}
	// insert 失败 → 返回 error（不 panic、不留半态）
	if err := writeVenueEventSnapshot(ctx, &fakeOrderStore{insertErr: true}, am, "3001157", ts); err == nil {
		t.Fatal("insert 失败应返回 error（不杀循环由调用方记日志承接）")
	}
}

// TestD383B4_StormCollapseRace B4：并发事件风暴（race 灵敏面）——同 venue 间隔内并发触发
// 收合为单行、整体无死锁；quote 流不被阻塞由「事件循环内有界工作量」承接（5s 收合前置短路）。
func TestD383B4_StormCollapseRace(t *testing.T) {
	s := d383Store(t)
	ctx := context.Background()
	sc := d383Scanner(s, []*v1.AccountInfo{
		{Venue: "3001157", VenueType: "mt5", AssetType: "cfd", Balance: 1, Currency: "USD"},
	})
	t0 := time.Now().UTC()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_ = sc.OnOrderEvent(ctx, "3001157", t0.Add(time.Duration(i)*time.Millisecond)) // 全落在 5s 窗内
			}
		}(g)
	}
	wg.Wait()
	if n, _ := countSnapshots(t, s, "mt_event"); n != 1 {
		t.Fatalf("storm rows = %d, want 1（风暴收合，红=并发窗口竞态放行）", n)
	}
}

// fakeOrderStore orderEventStore mock（fail-closed/写失败锚用）。
type fakeOrderStore struct {
	keysErr   bool
	insertErr bool
}

func (f *fakeOrderStore) BrokerRealAccountKeys(context.Context) (map[string]bool, error) {
	if f.keysErr {
		return nil, context.DeadlineExceeded
	}
	return map[string]bool{"3001157": true}, nil
}

func (f *fakeOrderStore) InsertVenueAccountSnapshots(_ context.Context, _ []pgstore.VenueAccountSnapshot) error {
	if f.insertErr {
		return context.DeadlineExceeded
	}
	return nil
}
