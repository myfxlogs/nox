// account_monitor_snapshot.go —— D-132：REST-only 账户面审计留痕搭车写口
// （applyAcctResult 成功路径调用；自 account_monitor.go 拆出控行数）。
package dashboard

import (
	"context"
	"log"
	"time"

	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// SetSnapshotWriter D-132：注入快照写口（main 装配 st.InsertVenueAccountSnapshots；
// nil=REST-only 面留痕跳过，REST 语义不动）。
func (m *AccountMonitor) SetSnapshotWriter(fn func(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error) {
	m.snapWriter = fn
}

// writeRestSnapshot D-132：REST-only CEX 面（HasPrivateStream=false：okx funding/earn
// 等无 WS 余额频道键，D-006 已举证）poll 成功且值变 → fin_venue_account_snapshot
// 搭车落库（src=rest_poll）。WS 覆盖键不写（私有流事件径已有，禁双写）、sim/非 cex 不写。
// D-136：去重口径改按「入库列」比较（snapSame=balance/equity/free_margin/currency/
// buffer_multiple，MT acctFrameState 三值先例的忠实化）——此前误用 acctSame 全 proto
// 比对，earn Holdings 内浮动字段每轮变→入库列逐行全同却照写（生产实证噪声行 ~1/min）。
// 事件驱动搭车（kick→poll→列值变→写）非定时器，不违 D-004；nil 写口/写败只 log 不回馈 REST 态。
func (m *AccountMonitor) writeRestSnapshot(ctx context.Context, vk string, prev, acct *reconcile.AccountInfo) {
	if m.snapWriter == nil || acct == nil {
		return
	}
	if acct.AssetType != "cex" || acct.Simulated ||
		venue.HasPrivateStream(acct.Venue, acct.VenueType, acct.Simulated) {
		return
	}
	if snapSame(prev, acct) {
		return
	}
	row := pgstore.VenueAccountSnapshot{
		Venue: acct.Venue, VenueType: venue.NormalizeVenueType(acct.AssetType, acct.Venue, acct.VenueType),
		BalanceRaw: acct.Balance, EquityRaw: acct.Equity, FreeMarginRaw: acct.FreeMargin,
		Currency: acct.Currency, FetchedAt: time.Now(), Src: "rest_poll",
	}
	if acct.BufferMultiple > 0 {
		b := acct.BufferMultiple
		row.BufferMultiple = &b
	}
	if err := m.snapWriter(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
		log.Printf("[account-mon] %s rest_poll snapshot write: %v", vk, err)
	}
}

// snapSame D-136：快照去重比较——只比「入库列」（balance/equity/free_margin/currency/
// buffer_multiple），非入库字段（Holdings 浮动子项等）不参与；prev=nil=首采必写。
func snapSame(a, b *reconcile.AccountInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Balance == b.Balance && a.Equity == b.Equity && a.FreeMargin == b.FreeMargin &&
		a.Currency == b.Currency && a.BufferMultiple == b.BufferMultiple
}
