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
// 搭车落库（src=rest_poll）。WS 覆盖键不写（私有流事件径已有，禁双写）、值不变不写
// （acctSame 去重=MT acctFrameState 三值先例）、sim/非 cex 不写。事件驱动搭车
// （kick→poll→值变→写）非定时器，不违 D-004；nil 写口/写败只 log 不回馈 REST 态。
func (m *AccountMonitor) writeRestSnapshot(ctx context.Context, vk string, acct *reconcile.AccountInfo, changed bool) {
	if m.snapWriter == nil || !changed || acct == nil {
		return
	}
	if acct.AssetType != "cex" || acct.Simulated ||
		venue.HasPrivateStream(acct.Venue, acct.VenueType, acct.Simulated) {
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
