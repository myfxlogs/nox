// account_monitor_snapshot.go —— D-132：REST-only 账户面审计留痕搭车写口
// （applyAcctResult 成功路径调用；自 account_monitor.go 拆出控行数）。
package dashboard

import (
	"context"
	"log"
	"sort"
	"time"

	"nox/internal/data/venue"
	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

// applyAcctResult REST 结果落态：err→restOnline=false（在线翻转则脏化，离线保留最近
// 成功快照 D-052）；ok→restOnline=true+acct 落态+dirty。cooldown Record 在锁内。
// D-136 R1 自 account_monitor.go 迁入（REST 落态+快照留痕写径同文件）。
func (m *AccountMonitor) applyAcctResult(ctx context.Context, vk string, acct *reconcile.AccountInfo, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldown.Record(vk, err)
	st := m.states[vk]
	if err != nil {
		st.restOnline = false
		wasOnline := st.online
		st.online = m.onlineLocked(vk, st)
		if !wasOnline {
			// 从未在线：也记录一次（CEX 首次配置失败曾静默无日志，难排障）
			log.Printf("[account-mon] %s first-poll error: %v", vk, err)
			return
		}
		m.states[vk] = st
		log.Printf("[account-mon] %s offline: %v", vk, err)
		if !st.online {
			m.dirtyLocked(vk)
		}
		return
	}
	changed := !acctSame(st.acct, acct)
	st.restOnline = true
	snapPrev := st.snapBase // D-136：快照去重基准=上次成功写行（非 acct——它会被 ledger patch 污染）
	cp := *acct             // 防共享指针原地改写：querier 侧复用/突变对象不回流本态
	st.acct = &cp
	st.asOf = time.Now()
	st.online = m.onlineLocked(vk, st)
	m.states[vk] = st
	m.dirtyLocked(vk)
	m.mu.Unlock()
	wrote := m.writeRestSnapshot(ctx, vk, snapPrev, acct) // D-132/D-136：REST-only 面入库列值变→审计留痕
	if changed {
		m.notifyFunds() // D-568：REST 值变→funds 通知（值不变不触）
	}
	m.mu.Lock()
	if wrote {
		if stNow, ok := m.states[vk]; ok { // 键可能已被 prune（配置删除）——不在则不复活
			stNow.snapBase = &cp // &cp 是本函数存的不可变副本（patch 径换新对象不动旧对象）
			m.states[vk] = stNow
		}
	}
}

// SetSnapshotWriter D-132：注入快照写口（main 装配 st.InsertVenueAccountSnapshots；
// nil=REST-only 面留痕跳过，REST 语义不动）。
func (m *AccountMonitor) SetSnapshotWriter(fn func(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error) {
	m.snapWriter = fn
}

// writeRestSnapshot D-132：REST-only CEX 面（HasPrivateStream=false：okx funding/earn
// 等无 WS 余额频道键，D-006 已举证）poll 成功且值变 → fin_venue_account_snapshot
// 搭车落库（src=rest_poll）。WS 覆盖键不写（私有流事件径已有，禁双写）、sim/非 cex 不写。
// D-136：去重口径按「入库列」比较（snapSame=balance/equity/free_margin/currency/
// buffer_multiple，MT acctFrameState 三值先例的忠实化），且基准是调用方传入的
// snapBase=上次成功写行副本——不可用 st.acct（可变展示态，OnLedgerAcct 会把
// capitalview REST 校准回写的 equity-fallback available patch 进 FreeMargin，
// 生产实证 earn 入库列全同却每轮照写）。事件驱动搭车（kick→poll→列值变→写）
// 非定时器，不违 D-004；nil 写口/写败只 log、基准不前进（同值下轮重试）。
func (m *AccountMonitor) writeRestSnapshot(ctx context.Context, vk string, prev, acct *reconcile.AccountInfo) bool {
	if m.snapWriter == nil || acct == nil {
		return false
	}
	if acct.AssetType != "cex" || acct.Simulated ||
		venue.HasPrivateStream(acct.Venue, acct.VenueType, acct.Simulated) {
		return false
	}
	if snapSame(prev, acct) {
		return false
	}
	rows := snapshotRows(prev, acct)
	if err := m.snapWriter(ctx, rows); err != nil {
		log.Printf("[account-mon] %s rest_poll snapshot write: %v", vk, err)
		return false
	}
	return true
}

// snapshotRows 组装写行：默认单行；earn 面多币持仓（binance earn 分币枚举，D-285 零换算）
// 时每币种一行（读端 DISTINCT ON 含 currency），并入 prev 币种并集——消失的币种
// 补 0 行防陈旧残值永久滞留。
func snapshotRows(prev, acct *reconcile.AccountInfo) []pgstore.VenueAccountSnapshot {
	vt := venue.NormalizeVenueType(acct.AssetType, acct.Venue, acct.VenueType)
	mk := func(ccy string, eq float64) pgstore.VenueAccountSnapshot {
		row := pgstore.VenueAccountSnapshot{
			Venue: acct.Venue, VenueType: vt,
			BalanceRaw: eq, EquityRaw: eq, FreeMarginRaw: 0, // 分币行零重复保证金概念
			Currency: ccy, FetchedAt: time.Now(), Src: "rest_poll",
		}
		if acct.BufferMultiple > 0 {
			b := acct.BufferMultiple
			row.BufferMultiple = &b
		}
		return row
	}
	if acct.VenueType != "earn" || len(acct.Holdings) == 0 {
		return []pgstore.VenueAccountSnapshot{{
			Venue: acct.Venue, VenueType: vt,
			BalanceRaw: acct.Balance, EquityRaw: acct.Equity, FreeMarginRaw: acct.FreeMargin,
			Currency: acct.Currency, FetchedAt: time.Now(), Src: "rest_poll",
			BufferMultiple: bufPtr(acct),
		}}
	}
	per := map[string]float64{}
	for _, h := range acct.Holdings {
		per[h.Ccy] += h.Qty
	}
	if prev != nil {
		for _, h := range prev.Holdings {
			if _, ok := per[h.Ccy]; !ok {
				per[h.Ccy] = 0 // 消失币种补零行（陈旧残值防滞留）
			}
		}
	}
	ccys := make([]string, 0, len(per))
	for c := range per {
		ccys = append(ccys, c)
	}
	sort.Strings(ccys)
	rows := make([]pgstore.VenueAccountSnapshot, 0, len(ccys))
	for _, c := range ccys {
		rows = append(rows, mk(c, per[c]))
	}
	return rows
}

func bufPtr(a *reconcile.AccountInfo) *float64 {
	if a.BufferMultiple <= 0 {
		return nil
	}
	b := a.BufferMultiple
	return &b
}

// buffer_multiple），非入库字段（Holdings 浮动子项等）不参与；prev=nil=首采必写。
func snapSame(a, b *reconcile.AccountInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Balance != b.Balance || a.Equity != b.Equity || a.FreeMargin != b.FreeMargin ||
		a.Currency != b.Currency || a.BufferMultiple != b.BufferMultiple {
		return false
	}
	// earn 分币径 holdings 照旧不比（D-136 噪声防线）：币种消失而聚合额不变=该
	// 币种≈0 不值得写行；真换仓时聚合额变会触写，snapshotRows 零填行兜消失币种。
	return true
}
