// dci_go.go —— D-191：dciexec.Executor 装配适配（凭据=exchange_configs binance:spot
// 非 sim 行同 NewEarnCollector 选径；bn sapi base=BinanceApiSegBase 同 D-106 helper）。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/ledger/settlement"
	"nox/internal/platform/analyst"
	"nox/internal/platform/httpx"
	"nox/internal/platform/safego"
	"nox/internal/portfolio/lifecycle"
	"nox/internal/research/oppmatch"
	"nox/internal/review/sysretro"
	"nox/internal/store/pgstore"
	"nox/internal/trading/autotrade"
	"nox/internal/trading/dciexec"
)

// wireJit jit 划转执行件装配：armed 缺资归集缝（atEngine.JitSweepFn——败因分类
// 落箱 permission=授权面）+结算回转钩（settlements 径 PostSettled——平仓腿保证
// 金释放权威时点，票面核验②：非 ClosePositionFn 挂钩）。
func wireJit(atEngine *autotrade.Engine, settleDeps settlement.Deps, ex *dciexec.Executor, dashSvc *dashboard.Service) {
	dashSvc.SetProposalGoRunner(ex.GoProposal) // D-661：approve 轨=GoProposal 同径（闸链零绕）
	if h, ok := settleDeps.(interface {
		SetPostSettleHook(func(context.Context, int64))
	}); ok {
		h.SetPostSettleHook(ex.JitReturnForPosition)
	}
	atEngine.JitSweepFn = func(ctx context.Context, vk string, need, oppAPR float64) (string, error) {
		err := ex.JitSweep(ctx, vk, need, oppAPR)
		if err != nil {
			return dciexec.JitErrKind(err), err
		}
		return "", nil
	}
}

// buildDCIExecutor 凭据装配（无 binance:spot 凭据行=Executor 持空 key，
// Go 内 credential 缺口径回「请配置凭据」——凭据缺不炸装配）。
func buildDCIExecutor(ctx context.Context, st *pgstore.Store, modeGate dciexec.ModeGate) *dciexec.Executor {
	ex := &dciexec.Executor{Store: st, Client: httpx.Client(), ModeGate: modeGate, LiveEquityMapFn: dciexec.LiveEquityMap}
	ex.Now = time.Now
	oppmatch.AutoexecHook = ex.GoProposal      // D-598：autoexec 模板提案落库即执行
	ex.KBDir = analyst.KnRoot + "/docs/经验库/条目" // 拒单消费闸：坑签名→条目教训附告警尾
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
		// earn 执行径多 venue 化：okx（余币宝）/gate（uni-lend）凭据行按 venue_type
		// 优先序兜取——同所多行不猜哪条，OKX unified>perp>spot、gate perp>unified；
		// 均无则 venue 无 EarnCreds 行→rail 缺席=credential 缺口如实出。
		for _, vtpref := range []struct {
			venue string
			types []string
		}{{"okx", []string{"unified", "perp", "spot", ""}},
			{"gate", []string{"perp", "unified", ""}}} {
			var best *pgstore.ExchangeConfig
			bestPri := len(vtpref.types)
			for i := range exchs {
				e := &exchs[i]
				if e.Venue != vtpref.venue || e.Simulated || e.APIKey == "" || e.APISecret == "" {
					continue
				}
				for pri, vt := range vtpref.types {
					if e.VenueType == vt && pri < bestPri {
						best, bestPri = e, pri
					}
				}
			}
			if best != nil {
				if ex.EarnCreds == nil {
					ex.EarnCreds = map[string]dciexec.EarnCred{}
				}
				ex.EarnCreds[vtpref.venue] = dciexec.EarnCred{
					Key: best.APIKey, Secret: best.APISecret, Pass: best.Passphrase, BaseURL: best.BaseURL}
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
				// pending_disposal 清扫（10-15 事故次生缺口：卖败留仓行无件再碰
				// =永等人工）——同拍搭车，零新定时器。
				if n, err := ex.SweepPendingDisposals(ctx); err != nil {
					log.Printf("[dci-sweep] scan: %v", err)
				} else if n > 0 {
					log.Printf("[dci-sweep] scan: %d 留仓行已卖回", n)
				}
			}
		}
	})
}

// lifecycleCriteria 判据行映射：pgstore.StrategyCriteria→lifecycle.Criterion
// （D-260：store 底层包不回指 lifecycle，adapter 在本层）。
func lifecycleCriteria(rows []pgstore.StrategyCriteria) []lifecycle.Criterion {
	out := make([]lifecycle.Criterion, 0, len(rows))
	for _, r := range rows {
		out = append(out, lifecycle.Criterion{Strategy: r.Strategy, Kind: r.Kind, Pred: r.Pred, Params: r.Params})
	}
	return out
}

// newDBEliminator D-260：settlement 淘汰判定适配——每轮实时读判据表。
func newDBEliminator(st *pgstore.Store) func(ctx context.Context, totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string) {
	return lifecycle.NewEliminator(func(ctx context.Context, kind string) ([]lifecycle.Criterion, error) {
		rows, err := st.ListStrategyCriteria(ctx, "*", kind)
		return lifecycleCriteria(rows), err
	})
}
