// order_event_snapshot.go D-383 B1：order-event → 该 venue 定向落库一行账户快照
// （src=mt_event，「成交即落库余额」——保证金告急/转入到账场景的持久层秒级新鲜度）。
// 行构造复用 buildVenueSnapshotRows 同口径（real 过滤+venue_type 归一+同键去重，
// 禁新起第三套口径）；同 venue 5s 最小落库间隔收合事件风暴（防下单风暴打爆 MT RPC/DB）；
// realKeys 查询失败 fail-closed 本轮不落；写失败返回 error 由事件循环记日志不杀循环。
package cexscan

import (
	"context"
	"fmt"
	"time"

	"arb/internal/store/pgstore"
)

// orderSnapshotMinInterval 同 venue 事件风暴收合：最小落库间隔（SSOT §5 B.1）。
const orderSnapshotMinInterval = 5 * time.Second

// orderEventStore D-383 B1：事件落库所需 store 面（*pgstore.Store 原生实现）。
type orderEventStore interface {
	BrokerRealAccountKeys(ctx context.Context) (map[string]bool, error)
	InsertVenueAccountSnapshots(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error
}

// OnOrderEvent D-383 B1：订单事件到达 → 该 venue 定向落库快照（src=mt_event，
// fetched_at=事件时刻）。同 venue orderSnapshotMinInterval 内静默跳过（收合）。
func (s *Scanner) OnOrderEvent(ctx context.Context, venue string, ts time.Time) error {
	s.orderSnapMu.Lock()
	if last, ok := s.orderSnapLast[venue]; ok && ts.Sub(last) < orderSnapshotMinInterval {
		s.orderSnapMu.Unlock()
		return nil
	}
	s.orderSnapLast[venue] = ts
	s.orderSnapMu.Unlock()
	return writeVenueEventSnapshot(ctx, s.store, s.accounts, venue, ts)
}

// writeVenueEventSnapshot 读 AccountMonitor 内存视图（5s ticker 自保鲜，SSOT §5 B.0）
// 按 venue 过滤，复用调度路径行口径落 src=mt_event 行。视图未含该 venue（未连接/未轮询到）
// no-op——事件只加速持久层，不新建数据源。
func writeVenueEventSnapshot(ctx context.Context, store orderEventStore, am AccountSnapshotter, venue string, ts time.Time) error {
	realKeys, err := store.BrokerRealAccountKeys(ctx)
	if err != nil {
		return fmt.Errorf("order snapshot: broker real keys: %w（fail-closed 本轮不落）", err)
	}
	var mine []AccountSnapshot
	for _, sn := range ScanAccounts(am) {
		if sn.Venue == venue {
			mine = append(mine, sn)
		}
	}
	rows := buildVenueSnapshotRows(mine, realKeys)
	for i := range rows {
		rows[i].Src = "mt_event"
		rows[i].FetchedAt = ts
	}
	if len(rows) == 0 {
		return nil
	}
	if err := store.InsertVenueAccountSnapshots(ctx, rows); err != nil {
		return fmt.Errorf("order snapshot: insert %s: %w", venue, err)
	}
	return nil
}
