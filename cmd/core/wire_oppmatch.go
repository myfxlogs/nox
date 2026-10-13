// wire_oppmatch.go —— D-187：模板×账本周期匹配装配。
// boot 首扫（部署即出首份提案/缺口）+每日 00:30 北京时间（sysretro.NextDelay 同款历日节律）；
// 提案=InsertAlert info→D-168 episode 闸，提案级零自动执行；缺口=fin_condition_gap→飞书请求。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/research/oppmatch"
	"nox/internal/review/sysretro"
	"nox/internal/store/pgstore"
)

// startOppMatch 装配机会匹配 loop（总纲衍生③；非报表环故不随 RetroDisabled 关）。
func startOppMatch(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	safego.GoLoop("main:oppmatch", func() { oppMatchLoop(ctx, st, cfg) })
}

func oppMatchLoop(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	// D-260④：live 策略自动执行器——凭据同 D-191 装配径（缺凭据=GoProposal 内 gap 拒单径）。
	ex := buildDCIExecutor(ctx, st)
	// D-193③：首扫等权限探针 boot 轮信号（消首轮假缺口/迟提案；
	// scanner 未接线场景由 startCexScanner 立即标 done 零死等）。
	select {
	case <-bus.PermProbeBootDone():
	case <-ctx.Done():
		return
	}
	if p, g, err := oppmatch.RunOnce(ctx, st, cfg.CapitalBaseUSD, cfg.AutoTradeMaxNotional); err != nil {
		log.Printf("[oppmatch] boot scan: %v", err)
	} else {
		log.Printf("[oppmatch] boot scan: %d proposals %d gap-requests", p, g)
		if n, err := ex.AutoExecLive(ctx); err != nil {
			log.Printf("[autoexec] boot scan: %v", err)
		} else if n > 0 {
			log.Printf("[autoexec] boot scan: %d proposals executed", n)
		}
	}
	for {
		safego.Beat("main:oppmatch")
		timer := time.NewTimer(sysretro.NextDelay(time.Now(), "daily"))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if _, _, err := oppmatch.RunOnce(ctx, st, cfg.CapitalBaseUSD, cfg.AutoTradeMaxNotional); err != nil {
			log.Printf("[oppmatch] daily scan: %v", err)
		} else if n, err := ex.AutoExecLive(ctx); err != nil {
			log.Printf("[autoexec] daily scan: %v", err)
		} else if n > 0 {
			log.Printf("[autoexec] daily scan: %d proposals executed", n)
		}
	}
}
