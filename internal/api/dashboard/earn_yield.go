// earn_yield.go —— D-524：earn 池收益增量归属入账（快照写径同拍，零新定时器）。
// 语义：earn 池快照行新写时 Δ(equity)−已归属流=收益增量 → income_events
// (income_type='earn_yield')。已归属流=本系统 fin_dci_order opt_type='EARN'
// filled 申购量（申购是池内流入须扣，赎出无台账行故负 Δ 如实负收益票面③）。
// 首拍无前值=基线化不立账（票面⑤）；D-516 归属系数同乘（BTC 池 1/3 份票面④）。
// venue 逐笔回执面（gate lend_records reinvest 等）现无读径接点——池增量兜底
// 为当前唯一口径，回执径接入后须按「回执>池增量」优先级防双记（如实留续）。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

// EarnYieldStore 归属入账读写面（装配注入 pgstore.Store；nil=归属关闭）。
type EarnYieldStore interface {
	PrevSnapshotEquity(ctx context.Context, venue, venueType, ccy string, before time.Time) (float64, time.Time, bool, error)
	EarnSubscribeSum(ctx context.Context, venue string, from, to time.Time) (float64, error)
	InsertIncomeEvents(ctx context.Context, evs []pgstore.IncomeEvent) (int64, error)
	ExchangeConfigID(ctx context.Context, venue, venueType string) (int64, error)
	OwnershipShares(ctx context.Context) (map[string]float64, error)
	// D-527①③：actual_apr 回写读面——池申购本金（提案回链）+窗内收益净额+评估卡写口。
	ListEarnPoolOrders(ctx context.Context, venue, ccy string) ([]pgstore.EarnPoolOrder, error)
	SumEarnYieldSince(ctx context.Context, venue, ccy string, since time.Time) (float64, error)
	UpdateOppEvalActual(ctx context.Context, proposalID int64, apr float64) error
	// DCI 台账对拍读面（快照写径搭车——台账 filled 单 vs venue 在仓引用集）。
	ListFilledDCIOrders(ctx context.Context) ([]pgstore.FilledDCIOrder, error)
	InsertAlertDedup(ctx context.Context, level, message string, window time.Duration) error
}

// SetEarnYieldStore 注入归属读写面（main 装配 st；零注入=仅快照不写收益行）。
func (m *AccountMonitor) SetEarnYieldStore(s EarnYieldStore) {
	m.mu.Lock()
	m.yieldStore = s
	m.mu.Unlock()
}

// attributeEarnYield 快照写径搭车点：earn 行新写成功后逐 ccy 归属。
// 写败/读败只 log 不阻塞快照主径（归属是账本增强非对账前提）。
func (m *AccountMonitor) attributeEarnYield(ctx context.Context, rows []pgstore.VenueAccountSnapshot) {
	if m.yieldStore == nil {
		return
	}
	shares, err := m.yieldStore.OwnershipShares(ctx)
	if err != nil {
		log.Printf("[earn-yield] ownership shares read: %v", err)
		return
	}
	var evs []pgstore.IncomeEvent
	for i, r := range rows {
		if r.VenueType != "earn" {
			continue
		}
		prev, prevTS, has, perr := m.yieldStore.PrevSnapshotEquity(ctx, r.Venue, "earn", r.Currency, r.FetchedAt)
		if perr != nil {
			log.Printf("[earn-yield] %s:earn %s prev read: %v", r.Venue, r.Currency, perr)
			continue
		}
		if !has {
			continue // 首拍基线化——本金存量不归收益
		}
		flow, ferr := m.yieldStore.EarnSubscribeSum(ctx, r.Venue, prevTS, r.FetchedAt)
		if ferr != nil {
			log.Printf("[earn-yield] %s:earn %s flow read: %v", r.Venue, r.Currency, ferr)
			continue
		}
		delta := r.EquityRaw - prev - flow
		if delta > -1e-6 && delta < 1e-6 {
			continue // 浮点残差档零——防 ±ε 收益噪声行
		}
		share, hasShare := pgstore.OwnershipShare(shares, r.Venue, "earn", r.Currency)
		if !hasShare {
			share = 1 // 未配置池全归属（D-516 缺省语义）
		}
		delta *= share
		cfgID, cerr := m.yieldStore.ExchangeConfigID(ctx, r.Venue, "earn")
		if cerr != nil {
			log.Printf("[earn-yield] %s:earn config id: %v", r.Venue, cerr)
			cfgID = 0
		}
		evs = append(evs, pgstore.IncomeEvent{
			Venue: r.Venue, VenueType: "earn", ConfigID: cfgID,
			Symbol: strings.ToUpper(r.Currency), IncomeType: "earn_yield",
			AmountUSD: delta, TranID: r.FetchedAt.UnixNano() + int64(i), Ts: r.FetchedAt,
		})
	}
	if n, werr := m.yieldStore.InsertIncomeEvents(ctx, evs); werr != nil {
		log.Printf("[earn-yield] insert: %v", werr)
	} else if n > 0 {
		log.Printf("[earn-yield] attributed %d earn_yield rows", n)
		m.writeEarnActualAPR(ctx, shares, evs)
	}
}

// writeEarnActualAPR D-527①③：earn 收益事件落账同拍——池实现 APR 回写提案评估卡。
// 口径：actual_apr=Σearn_yield（自最早在持申购起）/(Σ申购本金×归属份)×365/持有天数×100。
// 同 venue+ccy 池多单共享池实现 APR（池收益行不可拆单——不虚构单级拆分，注释即口径）；
// 赎回负 Δ 已记负收益行→负 APR 如实；无收益事件不触发（无执行事件不写≠0）；
// 「同一提案多执行事件取末次实现值」=每事件按累计重算覆盖，天然末次为准。
func (m *AccountMonitor) writeEarnActualAPR(ctx context.Context, shares map[string]float64, evs []pgstore.IncomeEvent) {
	seen := map[string]bool{}
	for _, ev := range evs {
		key := ev.Venue + "|" + ev.Symbol
		if seen[key] {
			continue
		}
		seen[key] = true
		orders, err := m.yieldStore.ListEarnPoolOrders(ctx, ev.Venue, ev.Symbol)
		if err != nil {
			log.Printf("[earn-yield] %s %s pool orders: %v", ev.Venue, ev.Symbol, err)
			continue
		}
		if len(orders) == 0 {
			continue
		}
		var since time.Time
		var principal float64
		for _, o := range orders {
			if since.IsZero() || o.CreatedAt.Before(since) {
				since = o.CreatedAt
			}
			principal += o.Qty
		}
		share, hasShare := pgstore.OwnershipShare(shares, ev.Venue, "earn", ev.Symbol)
		if !hasShare {
			share = 1 // 未配置池全归属（D-516 缺省语义，同归属径）
		}
		principal *= share
		days := time.Since(since).Hours() / 24
		if principal <= 0 || days <= 0 {
			continue
		}
		income, ierr := m.yieldStore.SumEarnYieldSince(ctx, ev.Venue, ev.Symbol, since)
		if ierr != nil {
			log.Printf("[earn-yield] %s %s yield sum: %v", ev.Venue, ev.Symbol, ierr)
			continue
		}
		apr := income / principal * 365 / days * 100
		for _, o := range orders {
			if uerr := m.yieldStore.UpdateOppEvalActual(ctx, o.ProposalID, apr); uerr != nil {
				log.Printf("[earn-yield] eval actual apr proposal#%d: %v", o.ProposalID, uerr)
			}
		}
	}
}

// checkDCILedgerCoverage 台账↔venue 在仓对拍（快照写径搭车，earn 读径采回的
// DCIOpenRefs vs fin_dci_order filled 单）：台账说在仓但 venue 在仓引用集无该
// product_ref=断点告警（settle 漏翻/venue 移行/手工干预实证坑候选）。
// venue→台账方向不查（业主手动申购属正常在仓）。nil refs=该径未读 DCI 不判。
func (m *AccountMonitor) checkDCILedgerCoverage(ctx context.Context, acct *reconcile.AccountInfo) {
	if m.yieldStore == nil || acct == nil || acct.DCIOpenRefs == nil {
		return
	}
	orders, err := m.yieldStore.ListFilledDCIOrders(ctx)
	if err != nil {
		log.Printf("[dci-cover] %s 台账读败（对拍跳拍）: %v", acct.VenueKey, err)
		return
	}
	present := map[string]bool{}
	for _, ref := range acct.DCIOpenRefs {
		present[ref] = true
	}
	for _, o := range orders {
		if o.Venue != acct.Venue || o.ProductRef == "" || present[o.ProductRef] {
			continue
		}
		if err := m.yieldStore.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
			"DCI 台账断点：%s order#%d %s filled 在仓但 venue 在仓集无行（快照对拍）",
			acct.VenueKey, o.ID, o.ProductRef), 24*time.Hour); err != nil {
			log.Printf("[dci-cover] %s alert: %v", acct.VenueKey, err)
		}
	}
}
