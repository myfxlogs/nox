// D-336 S1/S3：carry 宇宙扫描器装配（发现层，4h 结算节奏对齐；纯发现不触发开仓）。
package main

import (
	"context"
	"log"
	"strings"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/symdig"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/research/scan"
	"nox/internal/review/kbreport"
	"nox/internal/store/pgstore"
)

// startCarryUniverseScanner 装配并启动宇宙扫描（4h 周期；首轮立即跑）。
// D-474a S2：cexFeeBps 参数退役——纸面费用改读 venue 真值表（scan.paper_fees.go）；
// 保底槽两参数 env 直读（D-407 先例：装配层 env 解析；scan 层保持纯函数不读 env）。
// cfg 供 D-464 symdig AI client 懒解析（kbreport.ResolveClient 先例）。
// D-508 S9：refreshTopic 注入——纸面开平/毕业建议产出→Publish(topic)（StreamUpdates case 14）。
func startCarryUniverseScanner(ctx context.Context, st *pgstore.Store, tradeModeTTL time.Duration, cfg *config.Config, refreshTopic *bus.Topic[string]) {
	refreshPub := newRefreshCoalescer(refreshTopic) // 热修：2s/key 合流，纸面开平高频不扇出 RPC 风暴
	scanner := scan.NewCarryUniverseScanner(scan.NewCarryUniverseStore(st))
	scanner.SetTradeModeTTL(tradeModeTTL) // D-382 S1：trade_mode 新鲜度注入（cfg.TradeModeTTL→main 装配）
	// D-412 F4：读口龄期闸 env 可选（缺省 24h/48h 常量；D-407 先例装配层 env 直读，scan 层不读 env）。
	fundingMaxAge, swapMaxAge := scan.UniverseFundingMaxAge, scan.UniverseSwapMaxAge
	if cfg.UniverseFundingMaxAge > 0 {
		fundingMaxAge = cfg.UniverseFundingMaxAge
	}
	if cfg.UniverseSwapMaxAge > 0 {
		swapMaxAge = cfg.UniverseSwapMaxAge
	}
	scanner.SetAgeGates(fundingMaxAge, swapMaxAge)
	entryStreak := scan.NewEntryStreak()    // D-383C：进场持续闸计数，一次创建逐轮传入（进程态，重启清零=保守向）
	entryStreakMtm := scan.NewEntryStreak() // D-465：mt×mt 轨独立持续闸（键=venuePair|canonical）
	cfdOpts := scan.PaperCfdOpts{           // D-410 S4：flap 冷却 env（缺省 24h；D-050 数量槽退役无上限 env）
		ReopenCooldown: 24 * time.Hour,
	}
	if cfg.PaperAuthorizedReopenCooldownH >= 0 {
		cfdOpts.ReopenCooldown = time.Duration(cfg.PaperAuthorizedReopenCooldownH * float64(time.Hour))
	}
	safego.GoLoop("main:carry-universe", func() {
		runOnce := func() {
			res, err := scanner.Run(ctx, time.Now().UTC().Add(-24*time.Hour))
			if err != nil {
				log.Printf("[scan] carry universe: %v", err)
				return
			}
			rows := make([]pgstore.CarryUniverseRow, 0, len(res.Rows))
			for _, r := range res.Rows {
				rows = append(rows, pgstore.CarryUniverseRow{
					Canonical: r.Canonical, MtVenue: r.MtVenue, CexVenue: r.CexVenue,
					NetAnnualPct: r.NetAnnual, FundingAnnualPct: r.FundingAnn,
					SwapLongPct: r.SwapLongPct, SwapShortPct: r.SwapShortPct,
					SwapFree: r.SwapFree, Structure: r.Structure, Note: r.Note,
				})
			}
			if err := st.ReplaceCarryUniverse(ctx, rows); err != nil {
				log.Printf("[scan] carry universe replace: %v", err)
				return
			}
			// D-072：真候选阈值告警已退役——发现层产物经 D-050 自动进纸面。
			// D-463 S2：KB→pipeline 投影旗标一轮一载（读取失败 fail-open 视同无旗）。
			venueFlags, ferr := st.ListActiveFlags(ctx)
			if ferr != nil {
				log.Printf("[scan] venue symbol flags: %v（视同无旗放行）", ferr)
				venueFlags = nil
			}
			// D-350 阶段②：spot-carry 纸面轨步进（开仓/累积/退出；零执行风险）。
			// D-350R1 S3：开仓告警=事件驱动一条（symbol 清单），天然有界。
			if opened, exited, err := scan.StepPaperSpotCarry(ctx, st, entryStreak, time.Now(), venueFlags); err != nil {
				log.Printf("[scan] paper spot carry step: %v", err)
			} else {
				if len(opened) > 0 {
					if err := st.InsertAlert(ctx, nil, "info", "[spot-carry 纸面开仓] "+strings.Join(opened, ", ")); err != nil {
						log.Printf("[scan] paper spot carry alert: %v", err)
					}
				}
				if len(opened) > 0 || exited > 0 {
					log.Printf("[scan] paper spot carry: opened=%d exited=%d", len(opened), exited)
					refreshPub.Publish("paper_carry")
				}
			}
			// D-374 B 面：cfd×perp 纸面轨步进（res.Rows 内存直传零新查询；行在=信号在）。
			if opened, exited, err := scan.StepPaperCfdCarry(ctx, st, res.Rows, time.Now(), cfdOpts, venueFlags); err != nil {
				log.Printf("[scan] paper cfd carry step: %v", err)
			} else {
				if len(opened) > 0 {
					if err := st.InsertAlert(ctx, nil, "info", "[cfd-carry 纸面开仓] "+strings.Join(opened, ", ")); err != nil {
						log.Printf("[scan] paper cfd carry alert: %v", err)
					}
				}
				if len(opened) > 0 || exited > 0 {
					log.Printf("[scan] paper cfd carry: opened=%d exited=%d", len(opened), exited)
					refreshPub.Publish("paper_carry")
				}
			}
			// D-465：mt×mt 同品种跨商纸面轨步进（cfd 步进后同轮串行，单写者纪律）。
			if opened, exited, err := scanner.StepPaperMtmCarry(ctx, st, entryStreakMtm, time.Now(), venueFlags); err != nil {
				log.Printf("[scan] paper mtm carry step: %v", err)
			} else {
				if len(opened) > 0 {
					if err := st.InsertAlert(ctx, nil, "info", "[mtm-carry 纸面开仓] "+strings.Join(opened, ", ")); err != nil {
						log.Printf("[scan] paper mtm carry alert: %v", err)
					}
				}
				if len(opened) > 0 || exited > 0 {
					log.Printf("[scan] paper mtm carry: opened=%d exited=%d", len(opened), exited)
					refreshPub.Publish("paper_carry")
				}
			}
			// D-403 S2：毕业链步进（同 store 同轮串行单写者；建议产出在 approve 侧，此处零授权）。
			if n, err := scan.StepCarryGraduation(ctx, st, time.Now(), venueFlags); err != nil {
				log.Printf("[scan] carry graduation step: %v", err)
			} else if n > 0 {
				log.Printf("[scan] carry graduation: %d new suggestions", n)
				refreshPub.Publish("carry_graduation")
			}
			// D-464：配对池深挖接 scan 轮（universe 已刷新后跑；分段失败仅记日志）。
			symdigRound(ctx, st, cfg)
			log.Printf("[scan] carry universe: %d rows (skipped=%d)", len(rows), res.Skipped)
		}
		runOnce() // 启动补一轮：universe 新鲜度（计息按 settlement 界幂等，不双计）。
		// D-056③：纸面轮次整点对齐——ticker 首醒=下一个 UTC (h mod 4)=0 的 :05
		// （00:05Z 轮在 08:30BJ 日报前完成当日 00:00Z 结算计息）；此后 4h 步进保持对齐。
		next := nextPaperRoundAt(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		runOnce()
		ticker := time.NewTicker(4 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOnce()
			}
		}
	})
	log.Printf("[wiring] carry universe scanner started (4h cycle, aligned to :05 UTC, discover-only)")
}

// nextPaperRoundAt 下一轮整点对齐时点：UTC 4h 界内 :05 分（00:05/04:05/08:05/
// 12:05/16:05/20:05Z），严格 >now（恰在界点→下一界）。
func nextPaperRoundAt(now time.Time) time.Time {
	u := now.UTC()
	t := time.Date(u.Year(), u.Month(), u.Day(), u.Hour()-u.Hour()%4, 5, 0, 0, time.UTC)
	if !t.After(now) {
		t = t.Add(4 * time.Hour)
	}
	return t
}

// symdigRound D-464：深挖一轮（分类全跑；LLM 段仅在 client 可解析时启用）。
func symdigRound(ctx context.Context, st *pgstore.Store, cfg *config.Config) {
	if !cfg.SymdigEnabled {
		return
	}
	deps := symdig.Deps{Store: st, ReportDir: kbreport.DefaultReportDir, InboxPath: kbreport.DefaultInboxPath, MaxTokens: cfg.SymdigMaxTokens} // R1：/kn 容器约定路径常量，禁第二口径
	if client, err := kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel); err == nil {
		deps.Client = client
	}
	if _, err := symdig.Round(ctx, deps); err != nil {
		log.Printf("[symdig] round: %v", err)
	}
}
