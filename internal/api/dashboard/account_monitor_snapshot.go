// account_monitor_snapshot.go —— D-132：REST-only 账户面审计留痕搭车写口
// （applyAcctResult 成功路径调用；自 account_monitor.go 拆出控行数）。
package dashboard

import (
	"context"
	"log"
	"sort"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/venue"
	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

// applyAcctResult REST 结果落态：err→restOnline=false（在线翻转则脏化，离线保留最近
// 成功快照 D-052）；ok→restOnline=true+acct 落态+dirty。cooldown Record 在锁内。
// D-136 R1 自 account_monitor.go 迁入（REST 落态+快照留痕写径同文件）。
func (m *AccountMonitor) applyAcctResult(ctx context.Context, vk string, acct *reconcile.AccountInfo, err error, snapSrc string) {
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
	wrote := m.writeRestSnapshot(ctx, vk, snapPrev, acct, changed, snapSrc) // D-132/D-136/D-523：REST 面入库列值变→审计留痕
	if vk == "binance:spot" {
		// D-523续②：REST 实读成功同步刷 exec 资产缓存（SpotAssets 只认
		// outboundAccountPosition 帧——手工划转/断窗漏帧=化石遮蔽，SG#502 实证）。
		bus.SpotAssets.Put(vk, "USDT", acct.FreeMargin, 0, time.Now())
	}
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
// 搭车落库（src=rest_poll）。sim/非 cex 不写。
// D-523①②：WS 覆盖键不再豁免——REST 校准/重连回环读值变同落行（src=rest_calib/
// reconnect_resync）；流覆盖键去重基准=账本 patch 后的展示态（changed 位：WS 事件
// 经 OnLedgerAcct 已回写 st.acct，同值 REST 读不再重复落行），REST-only 键仍用
// snapBase（st.acct 会被账本 patch 污染见 D-136）。
// 去重口径按「入库列」比较（snapSame=balance/equity/free_margin/currency/
// buffer_multiple，MT acctFrameState 三值先例的忠实化）。事件驱动搭车
// （kick→poll→列值变→写）非定时器，不违 D-004；nil 写口/写败只 log、基准不前进。
func (m *AccountMonitor) writeRestSnapshot(ctx context.Context, vk string, prev, acct *reconcile.AccountInfo, changed bool, snapSrc string) bool {
	if m.snapWriter == nil || acct == nil {
		return false
	}
	if acct.AssetType != "cex" || acct.Simulated {
		return false
	}
	streamCovered := venue.HasPrivateStream(acct.Venue, acct.VenueType, acct.Simulated)
	if streamCovered {
		// D-604 P2③ 伤①：流覆盖键通用 poll 路径停写——WS 事件行（*_event）
		// 已是权威落地，通用 poll 写 rest_poll/rest_calib=旧 timer 期冗余摄取
		// （§3-4：rest 行降审计档案=只事件锚定 src——resync/消费点 calib 保留）。
		if snapSrc == "" || !changed {
			return false
		}
	} else if snapSame(prev, acct) {
		return false
	}
	src := snapSrc
	if src == "" {
		src = "rest_poll"
		if streamCovered {
			src = "rest_calib"
		}
	}
	rows := snapshotRows(prev, acct)
	for i := range rows {
		rows[i].Src = src
	}
	if err := m.snapWriter(ctx, rows); err != nil {
		log.Printf("[account-mon] %s rest_poll snapshot write: %v", vk, err)
		return false
	}
	m.attributeEarnYield(ctx, rows)     // D-524：earn 池行新写同拍增量归属入账
	m.checkDCILedgerCoverage(ctx, acct) // DCI 台账↔venue 在仓对拍（断点告警）
	return true
}

// snapshotRows 组装写行：默认单行；分币读面（earn/spot/unified 持仓分币枚举，
// D-285 零换算 + D-528 现货非稳定币入账）时每币种一行（读端 DISTINCT ON 含
// currency），并入 prev 币种并集——消失的币种补 0 行防陈旧残值永久滞留。
// free_margin 源=acct.FreeQtyByCcy（填则分币真值；nil=0——earn 池行零可动概念）。
func snapshotRows(prev, acct *reconcile.AccountInfo) []pgstore.VenueAccountSnapshot {
	vt := venue.NormalizeVenueType(acct.AssetType, acct.Venue, acct.VenueType)
	mk := func(ccy string, eq float64) pgstore.VenueAccountSnapshot {
		row := pgstore.VenueAccountSnapshot{
			Venue: acct.Venue, VenueType: vt,
			BalanceRaw: eq, EquityRaw: eq, FreeMarginRaw: acct.FreeQtyByCcy[ccy],
			Currency: ccy, FetchedAt: time.Now(), Src: "rest_poll",
		}
		if acct.BufferMultiple > 0 {
			b := acct.BufferMultiple
			row.BufferMultiple = &b
		}
		return row
	}
	// 判别用归一化 vt 而非原始 acct.VenueType——okx 交易侧配置行原始 type 可为
	// spot/perp/futures（归一才钉 unified）；按原始型判 perp→单行路径把
	// totalEq 挂 USDT 标签写 unified 键=rest_calib 19.95 伪 USDT 行实证（10-15
	// SG：与 okx_event 分币尘埃行同键互踩，NAV 池 flap ±43.5）。
	multiCcy := vt == "earn" || vt == "spot" || vt == "unified" ||
		vt == "funding" || vt == "futures"
	if !multiCcy || len(acct.Holdings) == 0 {
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
		// D-543R2 残波修：okx unified 键分币行=availBal 现金余额切片
		// （FreeQtyByCcy，与事件径 okx_event 同源口径）——h.Qty 是 cashBal
		// （含仓位占额），写过即与事件行同键互踩（USDT 3.4e-9 vs 20 实拍）。
		// 窄域钉死：只 okx 事件竞争池（unified）；binance/gate spot 行 cashBal
		// 口径不动（无事件写者互踩，availBal 别名不混用）。
		q := per[c]
		if acct.Venue == "okx" && vt == "unified" {
			if v, ok := acct.FreeQtyByCcy[c]; ok {
				q = v
			}
		}
		rows = append(rows, mk(c, q))
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
