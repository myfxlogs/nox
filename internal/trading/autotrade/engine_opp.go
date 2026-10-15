package autotrade

import (
	"context"
	"fmt"
	"log"
	"nox/internal/data/keydomain"
	"strings"
	"time"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// oppLoop 订阅机会事件 → 过滤 → 产 intent 走 gate（D-072：风控由 gate 统一）。
// 每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
// 日切由 accumulator 内部 pending 队列处理（零定时器，无共享竞态）。
func (e *Engine) oppLoop(ctx context.Context) {
	ch, unsub := e.oppTopic.Subscribe()
	defer unsub()
	acc := newComboAccumulator()
	for {
		select {
		case <-ctx.Done():
			return
		case opp, ok := <-ch:
			if !ok {
				return
			}
			log.Printf("[autotrade] recv opp=%s type=%s netBps=%.2f buy=%s sell=%s", opp.ID, opp.Type, opp.NetBps, opp.VenueBuy, opp.VenueSell)
			e.handleOpportunity(ctx, opp, acc)
			if err := acc.Flush(ctx, e.store); err != nil {
				log.Printf("[autotrade] opp rollup flush: %v", err)
			}
		}
	}
}

// handleOpportunity 过滤机会 → 产 intent 走 gate（D-072：风控由 gate 统一）。
// acc 由调用方传入（每 loop 一个长生命周期实例，处理完 flush 覆盖写）。
// 解除 D-070「放弃 CEX」限制——CFD 机会枯竭（SWAPARB 0 条新），
// PERP_FUNDING 通道已修复（gate testnet 域名迁移+key 验证通过），恢复 CEX 机会执行。
func (e *Engine) handleOpportunity(ctx context.Context, opp pgstore.Opportunity, acc *comboAccumulator) {
	// 全组合喂入 rollup 累积器（含低于门槛——覆盖度完整，write-behind）
	acc.Record(opp.ID, opp.Type, opp.NetBps, time.Now())

	// CARRY 已合并进 SWAPARB 统一 discoverSwapCombos→trySwapArb 路径，
	// 不再走 handleOpportunity。防御性 return——CARRY detector 删除后不会再有 CARRY 机会
	// 从 detector 路径产出，但保留防御以防未来恢复 CARRY detector 时忘记改回；
	// acc.Record 在前保证 rollup 覆盖度完整。
	if opp.Type == "CARRY" {
		log.Printf("[autotrade] skip %s: CARRY merged into SWAPARB unified path (D-187)", opp.ID)
		return
	}

	// 排除品种从 env 读取
	if e.cfg.ExcludeCanonicals[opp.Canonical] {
		log.Printf("[autotrade] skip %s: exclude canonicals (%s)", opp.ID, opp.Canonical)
		return
	}
	// venue/symbol 白名单（D-443 表化热读 holder）
	if reason := e.allowRuleReject(opp); reason != "" {
		log.Printf("[autotrade] skip %s: %s", opp.ID, reason)
		return
	}
	// 旗标执行投影——blocked 拒开（全 opp 类型，旗标=venue 级真相）；
	// warn 只 log 放行。
	if reason := e.flagRuleReject(opp); reason != "" {
		log.Printf("[autotrade] skip %s: %s", opp.ID, reason)
		return
	}
	// + D-168 自动准入：SWAPARB 发现层组合过滤。
	// autoAllow=true：跳过白名单过滤，所有组合产 intent 到 gate 层（由门禁栈兜底）。
	// autoAllow=false：白名单外组合静默过滤（不产 intent 不落 REJECTED），空 allowlist 不过滤（gate 层 fail-closed）。
	if opp.Type == "SWAPARB" && !e.cfg.SwapArbAutoAllow && len(e.swapArbAllowList()) > 0 {
		if exec.MatchComboAllow(e.swapArbAllowList(), opp.Canonical, opp.VenueBuy, opp.VenueSell) == nil {
			log.Printf("[autotrade] skip %s: SWAPARB combo not in allowlist (%s %s×%s)", opp.ID, opp.Canonical, opp.VenueBuy, opp.VenueSell)
			return
		}
	}
	// 黑名单：发现层静默过滤（deny 组合不产 intent）
	if opp.Type == "SWAPARB" && len(e.swapArbDenyList()) > 0 {
		if exec.MatchComboAllow(e.swapArbDenyList(), opp.Canonical, opp.VenueBuy, opp.VenueSell) != nil {
			log.Printf("[autotrade] skip %s: SWAPARB combo in denylist (%s %s×%s)", opp.ID, opp.Canonical, opp.VenueBuy, opp.VenueSell)
			return
		}
	}
	// XASSET_CARRY 前置拦截——零信任白名单（空表=全拒）。
	// 不命中→静默跳过（不产 intent 不落 REJECTED，修 462 条/时 REJECTED 刷屏；
	// 环境门挡不住时白名单是 CEX 真单的最后闸）。canonical_sell nil/空→回退 canonical。
	// spec 捕获下传 H4.2（MaxLongQty 复用为 spec 核准可行量下限）。
	var xassetSpec *exec.XAssetComboAllowSpec
	marginLegN := 0.0
	// FUNDING_HEDGE 同闸——canonical_sell 回退 canonical 与行对齐，复用全栈。
	if opp.Type == "XASSET_CARRY" || opp.Type == "FUNDING_HEDGE" {
		sellCanonical := opp.Canonical
		if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
			sellCanonical = *opp.CanonicalSell
		}
		if xassetSpec = exec.MatchXAssetComboAllow(e.xassetSpecs(), opp.Canonical, sellCanonical, opp.VenueBuy, opp.VenueSell); xassetSpec == nil {
			// 静默跳过不落日志——发现层（detector Allow 前置滤）已把白名单外组合
			// 挡在候选生成前，此处仅兜 TTL 陈旧窗口/手动路径漏网（同 D-311 静默先例；
			// 9h 2500+ 行同义 skip 刷屏根除点）。
			return
		}
		var gateOK bool // D-010 S3：margin 折算+canonical 聚簇闸（engine_opp_margin.go）
		if marginLegN, gateOK = e.xassetMarginGate(ctx, opp, sellCanonical, xassetSpec); !gateOK {
			return
		}
	}
	// 门槛：净收益
	if opp.NetBps < e.cfg.MinNetBps {
		log.Printf("[autotrade] skip %s: netBps %.2f < %.2f", opp.ID, opp.NetBps, e.cfg.MinNetBps)
		return
	}
	// carry 入口 funding regime 持久性闸——入口侧对称闸（退出侧已有「连 3 期转正」
	// 平仓规则），regime 坍塌途中不再照放（pos 485 归因）。失败静默跳过不落 REJECTED
	//（同 D-311 S6 先例）；manual/intent.Execute 路径同判于 ExecuteOpportunityInternal（S4 兜底）。
	if opp.Type == "XASSET_CARRY" {
		sellCanonical := opp.Canonical
		if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
			sellCanonical = *opp.CanonicalSell
		}
		// 休眠轨消费点（dormant.go）——命中即跳过，读败 fail-closed。
		if e.xassetDormantSkip(ctx, &opp) {
			return
		}
		if ok, reason := exec.CarryFundingRegimeCheck(ctx, e.store, opp.VenueBuy, opp.VenueSell, opp.Canonical, sellCanonical, time.Now(), e.cfg.CarryRegime); !ok {
			log.Printf("[autotrade] skip %s: funding regime 未持续 (%s)", opp.ID, reason)
			return
		}
	}
	// FUNDING_HEDGE 入场 regime 闸——v1 钉死正向（买现货+空 perp 收息，
	// 反构型=perp 买腿→借货卖空路径直接拒不进入）。sign=+1 单腿判定（空 perp 收正
	// funding），rows=FundingPredWindow 同 D-328 读法。
	if opp.Type == "FUNDING_HEDGE" {
		if !keydomain.IsType(opp.VenueBuy, "spot") || !keydomain.IsType(opp.VenueSell, "perp") {
			log.Printf("[autotrade] skip %s: FUNDING_HEDGE v1 仅正构型 spot买×perp卖 (%s×%s 拒开)", opp.ID, opp.VenueBuy, opp.VenueSell)
			return
		}
		perpVenue := keydomain.BareOf(opp.VenueSell)
		rows, err := e.store.FundingPredWindow(ctx, perpVenue, opp.Canonical, time.Now().Add(-e.cfg.CarryRegime.Window))
		if err != nil {
			log.Printf("[autotrade] skip %s: funding window %s/%s query: %v (fail-closed)", opp.ID, perpVenue, opp.Canonical, err)
			return
		}
		if gate, _, reason := exec.FundingRegimeOK(rows, +1, e.cfg.CarryRegime.FloorPct, e.cfg.CarryRegime.MinSamples, e.cfg.CarryRegime.PulsedAware); !gate {
			log.Printf("[autotrade] skip %s: funding regime 未持续 (%s/%s %s)", opp.ID, perpVenue, opp.Canonical, reason)
			return
		}
	}
	log.Printf("[autotrade] pass filter opp=%s type=%s netBps=%.2f", opp.ID, opp.Type, opp.NetBps)

	// 安全边际闸——顺价深度 ≤ 24h 摆幅半径 = 摆幅内开仓负期望（485/488 实证）。
	// monitor 符号约定 entry_spread_bps=(binance bid−exness ask)/ask，本结构顺价=正值（单向判定无绝对值）。
	// 数据缺失/过期 fail-open + warn（期望值过滤非风险保护；止损链独立管命）。manual 路径不拦
	//（advisory 管钱 vs D-328 risk-gate 管命，设计显式裁定，防未来误同步）。
	// FUNDING_HEDGE 不适用——monitor 安全边际语义专指 MT×CEX 顺价差。
	if opp.Type == "XASSET_CARRY" {
		// 腿向判定（D-550 试单轨镜像）：buyMT=正向（买 MT 卖 CEX，顺价=cexBid>mtAsk）；
		// sellMT=反向（买 CEX 卖 MT——空 CFD 收 swap×多 perp 付 funding，顺价镜像=
		// mtBid>cexAsk，分母换 cexAsk）；双 CEX/双 MT 无 MT 腿 → monitor 方向未定义拒开。
		sellCanonical := opp.Canonical
		if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
			sellCanonical = *opp.CanonicalSell
		}
		cexVenue, mtVenue := xassetLegVenues(opp.VenueBuy, opp.VenueSell)
		buyMT, sellMT := gate.IsMTVenue(opp.VenueBuy), gate.IsMTVenue(opp.VenueSell)
		if mtVenue == "" || buyMT == sellMT {
			log.Printf("[autotrade] skip %s: 无MT腿/双MT组合 monitor 方向未定义 (%s×%s，拒开)", opp.ID, opp.VenueBuy, opp.VenueSell)
			return
		}
		// 腿位镜像：MT 腿 canonical/venue 与 CEX 腿 canonical/venue 按向取位。
		mtCanon, cexCanon, mtVK, cexVK := opp.Canonical, sellCanonical, opp.VenueBuy, opp.VenueSell
		if !buyMT {
			mtCanon, cexCanon, mtVK, cexVK = sellCanonical, opp.Canonical, opp.VenueSell, opp.VenueBuy
		}
		// pair 键以 monitor 工具存储现值为准（"XAUUSDT-XAU"/"PAXGUSDT-XAU"，S2 钉死）：
		// 首段=CEX 腿 canonical，第二段=MT 腿 canonical 去 USD 后缀——两向同构。
		pairKey := cexCanon + "-" + strings.TrimSuffix(mtCanon, "USD")
		// 判定时刻价差改双腿实时报价现算（monitor 簿 5min 轮询退出决策面；
		// 簿内只留 noise_bps 统计阈值）。公式同 monitor 写入：entry=(cexBid−mtAsk)/mtAsk。
		// 双腿任一报价缺/超龄（>5min 同 tickFreshWindow 口径）→ fail-closed 拒开。
		if _, noiseBps, _, _, ok, err := e.store.LatestXAssetEntryGate(ctx, pairKey, cexVenue, mtVenue, xassetEntryGateMaxAge); err != nil {
			log.Printf("[autotrade] skip %s: safety margin query (%s,%s,%s): %v (fail-closed 拒开)", opp.ID, pairKey, cexVenue, mtVenue, err)
			return
		} else if !ok {
			log.Printf("[autotrade] skip %s: no monitor data (%s,%s,%s) (fail-closed 拒开)", opp.ID, pairKey, cexVenue, mtVenue)
			return
		} else if e.quotes == nil {
			log.Printf("[autotrade] skip %s: quote bus 未接线（实时报价不可得）fail-closed 拒开", opp.ID)
			return
		} else {
			mtQ, ok1 := e.quotes.Latest(mtCanon, mtVK)
			cexQ, ok2 := e.quotes.Latest(cexCanon, cexVK)
			if !ok1 || !ok2 || mtQ.Bid <= 0 || mtQ.Ask <= 0 || cexQ.Bid <= 0 || cexQ.Ask <= 0 ||
				time.Since(mtQ.Time) > 5*time.Minute || time.Since(cexQ.Time) > 5*time.Minute {
				log.Printf("[autotrade] skip %s: 实时报价缺（mt@%s ok=%v cex@%s ok=%v）fail-closed 拒开",
					opp.ID, mtVK, ok1, cexVK, ok2)
				return
			}
			// 顺价方向镜像：正向=(cexBid−mtAsk)/mtAsk；反向=(mtBid−cexAsk)/cexAsk
			// （同 pair 价差摆幅序列共源 noise_bps 阈——两向分母差 <tracking 量级）。
			entryBps := (cexQ.Bid - mtQ.Ask) / mtQ.Ask * 1e4
			if !buyMT {
				entryBps = (mtQ.Bid - cexQ.Ask) / cexQ.Ask * 1e4
			}
			switch {
			case entryBps <= 0:
				log.Printf("[autotrade] skip %s: safety margin entry %.2fbps <= 0 (逆价不开)", opp.ID, entryBps)
				return
			case entryBps <= noiseBps:
				log.Printf("[autotrade] skip %s: safety margin entry %.2fbps <= noise %.2fbps (摆幅内不开)", opp.ID, entryBps, noiseBps)
				return
			}
		}
	}

	notionalUSD := effectiveNotionalUSD(e.cfg.NotionalUSD, xassetSpec) // D-367 S4
	if marginLegN > 0 {                                                // D-010 S3：margin 反推单腿名义覆盖 sizing 基数
		notionalUSD = marginLegN
	}
	qty, perLot, skip := computeBuyQty(opp, notionalUSD, e.service.USDPerLotForLeg)
	if skip {
		// CS-miss 按 (venue,canonical) 聚合计数（语义扩为「无法定尺寸」，含 CS-miss/no-quote/fx-miss）
		e.recordCSMiss(opp.VenueBuy, opp.Canonical)
		return
	}
	if qty <= 0 {
		return
	}
	// econ 入场闸（实现在 engine_econ.go）——装配缺账/回本超阈 fail-closed 拒开。
	if e.econEntryGate(ctx, opp, notionalUSD) {
		return
	}
	// 权益纪律闸（D-030 硬线）——venue 权益 ≥ pct%×(在仓名义+本次腿名义)，
	// 快照缺/超龄 fail-closed 拒开（实现在 risk_lines.go）。
	if e.equityDisciplineGate(ctx, opp, notionalUSD) {
		return
	}
	// H4.2：qty 低于买腿最小步 → debug 跳过（品种适配事实，不产 intent 不落 REJECTED）
	// venueSymbol 从 qbus 取（handleOpportunity 无直接 quote，用 opp.VenueBuy 查）
	intentNotional := notionalUSD // D-367 S4：与 qty 同取有效值（intent 名义=qty 实额基准一致）
	if qbuy, ok := e.quotes.Latest(opp.Canonical, opp.VenueBuy); ok {
		if e.service.QtyBelowMinStep(opp.VenueBuy, qbuy.VenueSymbol, qty) {
			// 断层②：XASSET_CARRY 且 spec 命中时走 spec 驱动可行量下限（§2.2）——
			// 黄金对 perLot≈$435.5k，$1000 预算 qty 0.0023<0.01 每轮被静默吞；不满足条件维持静默跳过。
			floored := false
			if opp.Type == "XASSET_CARRY" && xassetSpec != nil {
				if nq, nn, ok2 := xassetQtyFloor(xassetSpec, !e.service.QtyBelowMinStep(opp.VenueBuy, qbuy.VenueSymbol, xassetSpec.MaxLongQty), perLot); ok2 {
					qty = nq
					intentNotional = nn
					floored = true
				}
			}
			if !floored {
				log.Printf("[autotrade] skip opp=%s: qty %.6f below min step (notional $%.0f too small for %s)",
					opp.ID, qty, e.cfg.NotionalUSD, qbuy.VenueSymbol)
				return
			}
			log.Printf("[autotrade] xasset qty floor from combo spec: %s qty %.6f notional $%.0f", opp.ID, qty, intentNotional)
		}
	}
	// [exec-entry:gate] D-262 M4.3①：正规径——intent.Execute→ExecuteOpportunityInternal。
	var intentSellCanonical string
	if opp.CanonicalSell != nil {
		intentSellCanonical = *opp.CanonicalSell // D-550：gate 试单豁免四元组对拍需卖腿 canonical
	}
	intent := gate.TradeIntent{
		Strategy:      opp.Type,
		Canonical:     opp.Canonical,
		CanonicalSell: intentSellCanonical,
		VenueBuy:      opp.VenueBuy,
		VenueSell:     opp.VenueSell,
		Qty:           qty,
		NotionalUSD:   intentNotional,
		DedupKey:      opp.ID,
		NetBps:        opp.NetBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("autotrade-%d-%s", time.Now().UnixMilli(), opp.ID)
			return e.service.ExecuteOpportunityInternal(ctx, opp.ID, reqID, qty)
		},
	}
	e.gate.Submit(ctx, &intent)
}

// 数量/名义额助手（isMTVenue/effectiveNotionalUSD/computeBuyQty/xassetQtyFloor）在
// engine_opp_qty.go（D-042 R2 拆分保行数）。
