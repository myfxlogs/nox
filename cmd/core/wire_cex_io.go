// wire_cex_io.go —— D-365 S3：CEX 进出账采集装配（binance+okx real 配置，15min 周期；
// 首拉 90d 回填+水位增量；幂等落库 fin_venue_io）。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/data/collect/exchange"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// startCexIOCollector 装配并启动进出账采集（初始跑一轮+15min ticker——进出账低频，
// 不与高频采集抢）。配置逐轮重读（增删自动跟随，同 income 惯例）。
func startCexIOCollector(ctx context.Context, st *pgstore.Store, kickCh chan struct{}) {
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
	log.Printf("[wiring] cex-io collector started (kick-driven, 30min watermark fallback)")
}
