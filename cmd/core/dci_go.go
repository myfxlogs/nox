// dci_go.go —— D-191：dciexec.Executor 装配适配（凭据=exchange_configs binance:spot
// 非 sim 行同 NewEarnCollector 选径；bn sapi base=BinanceApiSegBase 同 D-106 helper）。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/httpx"
	"nox/internal/platform/safego"
	"nox/internal/review/sysretro"
	"nox/internal/store/pgstore"
	"nox/internal/trading/dciexec"
)

// buildDCIExecutor 凭据装配（无 binance:spot 凭据行=Executor 持空 key，
// Go 内 credential 缺口径回「请配置凭据」——凭据缺不炸装配）。
func buildDCIExecutor(ctx context.Context, st *pgstore.Store) *dciexec.Executor {
	ex := &dciexec.Executor{Store: st, Client: httpx.Client()}
	ex.Now = time.Now
	exchs, err := st.ListExchangeConfigs(ctx)
	if err == nil {
		for _, e := range exchs {
			if e.Venue == "binance" && e.VenueType == "spot" && !e.Simulated {
				ex.BnKey = e.APIKey
				ex.BnSec = e.APISecret
				ex.Sapi = httpx.BinanceApiSegBase(e.BaseURL, "sapi")
				ex.APIBase = httpx.BinanceApiSegBase(e.BaseURL, "api")
			}
		}
	}
	return ex
}

// startDCIPoolReconcile 托管池日对账（D-191 R3：每日 00:40 北京同 sysretro 节律；
// 对账=账本水位核对+漂移告警自动修正，非业务数据刷新——定时器口径合规）。
func startDCIPoolReconcile(ctx context.Context, ex *dciexec.Executor) {
	safego.GoLoop("main:dci-pool-reconcile", func() {
		if l, v, d, err := ex.ReconcilePoolOnce(ctx); err != nil {
			log.Printf("[dci-pool-reconcile] boot: %v", err)
		} else {
			log.Printf("[dci-pool-reconcile] boot: ledger %.8f live %.8f drift %v", l, v, d)
		}
		for {
			delay := sysretro.NextDelay(time.Now(), "daily")
			t := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			if l, v, d, err := ex.ReconcilePoolOnce(ctx); err != nil {
				log.Printf("[dci-pool-reconcile] daily: %v", err)
			} else {
				log.Printf("[dci-pool-reconcile] daily: ledger %.8f live %.8f drift %v", l, v, d)
			}
		}
	})
}

// startDCIResume D-198：首个 cexscan DCI 采集拍完成后（事件式 bus 信号非裸 sleep）
// 提案断点续执一次——败单提案对 outstanding 复验后续走 GoProposal 同径。
func startDCIResume(ctx context.Context, ex *dciexec.Executor) {
	safego.GoPlain("setup:dciResume", func() {
		select {
		case <-ctx.Done():
			return
		case <-bus.DCIBootDone():
		}
		n, err := ex.ResumeFailedProposals(ctx)
		if err != nil {
			log.Printf("[dci-resume] boot: %v", err)
			return
		}
		if n > 0 {
			log.Printf("[dci-resume] boot: %d 提案续执", n)
		}
	})
}

// startDCISettle D-207①：DCI 交割观察——订阅既有 cexscan 扫描拍
// （refreshTopic "cex_scan" 事件，零新定时器）；每拍对过期 filled 订单做
// venue 持仓终态确认翻 settled。scanner 缺=无拍不扫（订单态保持 filled
// 如实，不假装结算）。
func startDCISettle(ctx context.Context, ex *dciexec.Executor, refreshTopic *bus.Topic[string]) {
	if ex == nil || refreshTopic == nil {
		return
	}
	ch, unsub := refreshTopic.Subscribe()
	safego.GoPlain("setup:dciSettle", func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				if n, err := ex.SettleExpiredOrders(ctx); err != nil {
					log.Printf("[dci-settle] scan: %v", err)
				} else if n > 0 {
					log.Printf("[dci-settle] scan: %d 单交割翻态", n)
				}
			}
		}
	})
}
