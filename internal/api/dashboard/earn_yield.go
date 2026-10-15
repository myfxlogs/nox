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
	"log"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// EarnYieldStore 归属入账读写面（装配注入 pgstore.Store；nil=归属关闭）。
type EarnYieldStore interface {
	PrevSnapshotEquity(ctx context.Context, venue, venueType, ccy string, before time.Time) (float64, time.Time, bool, error)
	EarnSubscribeSum(ctx context.Context, venue string, from, to time.Time) (float64, error)
	InsertIncomeEvents(ctx context.Context, evs []pgstore.IncomeEvent) (int64, error)
	ExchangeConfigID(ctx context.Context, venue, venueType string) (int64, error)
	OwnershipShares(ctx context.Context) (map[string]float64, error)
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
	}
}
