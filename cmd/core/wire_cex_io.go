// wire_cex_io.go —— D-365 S3：CEX 进出账采集装配（binance+okx real 配置，15min 周期；
// 首拉 90d 回填+水位增量；幂等落库 fin_venue_io）。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/collect/exchange"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// startCexIOCollector 装配并启动进出账采集（初始跑一轮+15min ticker——进出账低频，
// 不与高频采集抢）。配置逐轮重读（增删自动跟随，同 income 惯例）。
// MT5 io 同拍挂车（零新定时器）：connected mt5 账号 OrderHistory Balance 行→fin_venue_io。
func startCexIOCollector(ctx context.Context, st *pgstore.Store, kickCh chan struct{}, bm *dashboard.BrokerManager) {
	col := exchange.NewCexIOCollector(st, func() []exchange.CexIOAccount {
		cfgs, err := st.ListExchangeConfigs(ctx)
		if err != nil {
			log.Printf("[wiring] cex-io ListExchangeConfigs: %v (round skipped)", err)
			return nil
		}
		return exchange.FilterIOAccounts(cfgs)
	})
	safego.GoLoop("main:cex-io", func() {
		// D-469a S4/S5：binance DEPOSIT/WITHDRAW kick 主路；ticker 降 30min 断流兜底。
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			if n, err := col.RunOnce(ctx); err != nil {
				log.Printf("[wiring] cex-io RunOnce: %v", err)
			} else if n > 0 {
				log.Printf("[wiring] cex-io round: %d rows inserted", n)
			}
			if bm != nil {
				if n := mtIORound(ctx, st, bm); n > 0 {
					log.Printf("[wiring] mt-io round: %d rows inserted", n)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-kickCh:
				// 出入账到账延迟小，kick 后 30s 再拉（同 incomeKickSettleDelay 口径）。
				if _, done := waitOrKick(ctx, 30*time.Second, nil); done {
					return
				}
			}
		}
	})
	log.Printf("[wiring] cex-io collector started (kick-driven, 30min watermark fallback; mt-io 同拍)")
}

// mtIORound MT5 充提采集轮（水位增量——每方向独立游标，overlap 1h 防边界漏；
// 首拉 90d 回填）。幂等键=mt5:ticket:{ticket}（(venue,config_id,direction,external_id)
// 唯一冲突 DO NOTHING）。ccy=账户 AccountSummary 原生币种，缺→fail-closed 该账户跳
// （不编 USD）。
func mtIORound(ctx context.Context, st *pgstore.Store, bm *dashboard.BrokerManager) int {
	accs, err := st.MTIOAccountIDs(ctx)
	if err != nil {
		log.Printf("[wiring] mt-io accounts: %v", err)
		return 0
	}
	total := 0
	for _, a := range accs {
		scan := bm.IOScan(a.Venue)
		if scan == nil {
			continue // 未接线（connecting/stopped）——下轮重试如实记
		}
		ccy, ok := bm.MTAccountCurrency(ctx, a.Venue)
		if !ok {
			log.Printf("[wiring] mt-io %s 账户币种不可得（fail-closed 跳）", a.Venue)
			continue
		}
		now := time.Now().UTC()
		for _, dir := range []string{"deposit", "withdraw"} {
			from := now.Add(-90 * 24 * time.Hour)
			if ts, ok, _ := st.LatestVenueIOTs(ctx, a.ID, dir); ok {
				from = ts.Add(-time.Hour) // 水位 overlap
			}
			evs, err := scan(ctx, from, now)
			if err != nil {
				log.Printf("[wiring] mt-io %s %s scan: %v", a.Venue, dir, err)
				continue
			}
			var rows []pgstore.VenueIO
			for _, e := range evs {
				d := "deposit"
				amt := e.Amount
				if amt < 0 {
					d, amt = "withdraw", -amt
				}
				if d != dir {
					continue
				}
				rows = append(rows, pgstore.VenueIO{
					Venue: a.Venue, ConfigID: a.ID, Direction: d, Ccy: ccy,
					Amount: amt, ExternalID: fmt.Sprintf("mt5:ticket:%d", e.Ticket), Ts: e.CloseTime,
				})
			}
			if n, err := st.InsertVenueIO(ctx, rows); err != nil {
				log.Printf("[wiring] mt-io %s insert: %v", a.Venue, err)
			} else {
				total += int(n)
			}
		}
	}
	return total
}
