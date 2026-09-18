package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// oppLoop 订阅机会事件 → 过滤 → 产 intent 走 gate（D-072：风控由 gate 统一）。
// D-080 R7：每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
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
// D-080 R7：acc 由调用方传入（每 loop 一个长生命周期实例，处理完 flush 覆盖写）。
// D-120（2026-08-27）：解除 D-070「放弃 CEX」限制——CFD 机会枯竭（SWAPARB 0 条新），
// PERP_FUNDING 通道已修复（gate testnet 域名迁移+key 验证通过），恢复 CEX 机会执行。
func (e *Engine) handleOpportunity(ctx context.Context, opp pgstore.Opportunity, acc *comboAccumulator) {
	// D-080 S4：全组合喂入 rollup 累积器（含低于门槛——覆盖度完整，write-behind）
	acc.Record(opp.ID, opp.Type, opp.NetBps, time.Now())

	// D-187：CARRY 已合并进 SWAPARB 统一 discoverSwapCombos→trySwapArb 路径，
	// 不再走 handleOpportunity。防御性 return——CARRY detector 删除后不会再有 CARRY 机会
	// 从 detector 路径产出，但保留防御以防未来恢复 CARRY detector 时忘记改回；
	// acc.Record 在前保证 rollup 覆盖度完整。
	if opp.Type == "CARRY" {
		log.Printf("[autotrade] skip %s: CARRY merged into SWAPARB unified path (D-187)", opp.ID)
		return
	}

	// D-072 §3.4：排除品种从 env 读取
	if e.cfg.ExcludeCanonicals[opp.Canonical] {
		log.Printf("[autotrade] skip %s: exclude canonicals (%s)", opp.ID, opp.Canonical)
		return
	}
	// D-072 §3.4：venue 白名单从 env 读取
	if !e.cfg.AllowVenues[opp.VenueBuy] || !e.cfg.AllowVenues[opp.VenueSell] {
		log.Printf("[autotrade] skip %s: venue not in allowlist (%s/%s)", opp.ID, opp.VenueBuy, opp.VenueSell)
		return
	}
	// D-072 §3.4：symbol 白名单从 env 读取
	if len(e.cfg.AllowCanonicals) > 0 && !e.cfg.AllowCanonicals[opp.Canonical] {
		log.Printf("[autotrade] skip %s: canonical not in allowlist (%s)", opp.ID, opp.Canonical)
		return
	}
	// D-159 W1a R1 + D-168 自动准入：SWAPARB 发现层组合过滤。
	// autoAllow=true：跳过白名单过滤，所有组合产 intent 到 gate 层（由门禁栈兜底）。
	// autoAllow=false：白名单外组合静默过滤（不产 intent 不落 REJECTED），空 allowlist 不过滤（gate 层 fail-closed）。
	if opp.Type == "SWAPARB" && !e.cfg.SwapArbAutoAllow && len(e.cfg.SwapArbAllowCombos) > 0 {
		if exec.MatchComboAllow(e.cfg.SwapArbAllowCombos, opp.Canonical, opp.VenueBuy, opp.VenueSell) == nil {
			log.Printf("[autotrade] skip %s: SWAPARB combo not in allowlist (%s %s×%s)", opp.ID, opp.Canonical, opp.VenueBuy, opp.VenueSell)
			return
		}
	}
	// D-168 黑名单：发现层静默过滤（deny 组合不产 intent）
	if opp.Type == "SWAPARB" && len(e.cfg.SwapArbDenyCombos) > 0 {
		if exec.MatchComboAllow(e.cfg.SwapArbDenyCombos, opp.Canonical, opp.VenueBuy, opp.VenueSell) != nil {
			log.Printf("[autotrade] skip %s: SWAPARB combo in denylist (%s %s×%s)", opp.ID, opp.Canonical, opp.VenueBuy, opp.VenueSell)
			return
		}
	}
	// D-311 S6：XASSET_CARRY 前置拦截——零信任白名单（空表=全拒）。
	// 不命中→静默跳过（不产 intent 不落 REJECTED，修 462 条/时 REJECTED 刷屏；
	// 环境门挡不住时白名单是 CEX 真单的最后闸）。canonical_sell nil/空→回退 canonical。
	// D-330 S3：spec 捕获下传 H4.2（MaxLongQty 复用为 spec 核准可行量下限）。
	var xassetSpec *exec.XAssetComboAllowSpec
	if opp.Type == "XASSET_CARRY" {
		sellCanonical := opp.Canonical
		if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
			sellCanonical = *opp.CanonicalSell
		}
		if xassetSpec = exec.MatchXAssetComboAllow(e.cfg.XAssetAllowCombos, opp.Canonical, sellCanonical, opp.VenueBuy, opp.VenueSell); xassetSpec == nil {
			log.Printf("[autotrade] skip %s: XASSET_CARRY combo not in allowlist (%s/%s %s×%s)", opp.ID, opp.Canonical, sellCanonical, opp.VenueBuy, opp.VenueSell)
			return
		}
	}
	// 门槛：净收益
	if opp.NetBps < e.cfg.MinNetBps {
		log.Printf("[autotrade] skip %s: netBps %.2f < %.2f", opp.ID, opp.NetBps, e.cfg.MinNetBps)
		return
	}
	// D-328 S3：carry 入口 funding regime 持久性闸——入口侧对称闸（退出侧已有「连 3 期转正」
	// 平仓规则），regime 坍塌途中不再照放（pos 485 归因）。失败静默跳过不落 REJECTED
	//（同 D-311 S6 先例）；manual/intent.Execute 路径同判于 ExecuteOpportunityInternal（S4 兜底）。
	if opp.Type == "XASSET_CARRY" {
		sellCanonical := opp.Canonical
		if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
			sellCanonical = *opp.CanonicalSell
		}
		if ok, reason := exec.CarryFundingRegimeCheck(ctx, e.store, opp.VenueBuy, opp.VenueSell, opp.Canonical, sellCanonical, time.Now(), e.cfg.CarryRegime); !ok {
			log.Printf("[autotrade] skip %s: funding regime 未持续 (%s)", opp.ID, reason)
			return
		}
	}
	log.Printf("[autotrade] pass filter opp=%s type=%s netBps=%.2f", opp.ID, opp.Type, opp.NetBps)

	// D-335 v2：安全边际闸——顺价深度 ≤ 24h 摆幅半径 = 摆幅内开仓负期望（485/488 实证）。
	// monitor 符号约定 entry_spread_bps=(binance bid−exness ask)/ask，本结构顺价=正值（单向判定无绝对值）。
	// 数据缺失/过期 fail-open + warn（期望值过滤非风险保护；止损链独立管命）。manual 路径不拦
	//（advisory 管钱 vs D-328 risk-gate 管命，设计显式裁定，防未来误同步）。
	if opp.Type == "XASSET_CARRY" {
		// pair 键以 monitor 工具存储现值为准（"XAUUSDT-XAU"，S2 钉死）：第二段=canonical 去 USD 后缀。
		pairKey := opp.Canonical
		if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
			pairKey = *opp.CanonicalSell + "-" + strings.TrimSuffix(opp.Canonical, "USD")
		}
		if entryBps, noiseBps, ok, err := e.store.LatestXAssetEntryGate(ctx, pairKey, xassetEntryGateMaxAge); err != nil {
			log.Printf("[autotrade] safety margin: %s query: %v (fail-open)", pairKey, err)
		} else if ok {
			switch {
			case entryBps <= 0:
				log.Printf("[autotrade] skip %s: safety margin entry %.2fbps <= 0 (逆价不开)", opp.ID, entryBps)
				return
			case entryBps <= noiseBps:
				log.Printf("[autotrade] skip %s: safety margin entry %.2fbps <= noise %.2fbps (摆幅内不开)", opp.ID, entryBps, noiseBps)
				return
			}
		} else {
			log.Printf("[autotrade] safety margin: no monitor data for %s (fail-open)", pairKey)
		}
	}

	qty, perLot, skip := computeBuyQty(opp, e.cfg.NotionalUSD, e.service.USDPerLotForLeg)
	if skip {
		// D-140 S2/D-190：CS-miss 按 (venue,canonical) 聚合计数（语义扩为「无法定尺寸」，含 CS-miss/no-quote/fx-miss）
		e.recordCSMiss(opp.VenueBuy, opp.Canonical)
		return
	}
	if qty <= 0 {
		return
	}
	// H4.2：qty 低于买腿最小步 → debug 跳过（品种适配事实，不产 intent 不落 REJECTED）
	// venueSymbol 从 qbus 取（handleOpportunity 无直接 quote，用 opp.VenueBuy 查）
	intentNotional := e.cfg.NotionalUSD
	if qbuy, ok := e.quotes.Latest(opp.Canonical, opp.VenueBuy); ok {
		if e.service.QtyBelowMinStep(opp.VenueBuy, qbuy.VenueSymbol, qty) {
			// D-330 断层②：XASSET_CARRY 且 spec 命中时走 spec 驱动可行量下限（§2.2）——
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
	intent := TradeIntent{
		Strategy:    opp.Type,
		Canonical:   opp.Canonical,
		VenueBuy:    opp.VenueBuy,
		VenueSell:   opp.VenueSell,
		Qty:         qty,
		NotionalUSD: intentNotional,
		DedupKey:    opp.ID,
		NetBps:      opp.NetBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("autotrade-%d-%s", time.Now().UnixMilli(), opp.ID)
			return e.service.ExecuteOpportunityInternal(ctx, opp.ID, reqID, qty)
		},
	}
	e.gate.Submit(ctx, &intent)
}

// isMTVenue 判断 venueKey 是否为 MT5/MT4 venue（D-135 sizing CS fail-closed 用）。
func isMTVenue(venue string) bool {
	return strings.HasSuffix(venue, ":mt5") || strings.HasSuffix(venue, ":mt4")
}

// computeBuyQty 计算买腿 qty（D-190：USD notional 统一，价格源改 live qbus ask）。
// usdPerLot = e.service.USDPerLotForLeg（内部 legContractSizeOf + usdnotional.PerLot，CS 源单一）。
// ok=false → skip=true（fail-closed log 含 opp.ID 与原因：CS-miss / no-quote / fx-miss）。
// **行为变化（D-190 有意）**：价格源从 opp.PriceBuy（机会行，可能陈旧）改为 live qbus ask（更优口径）；
// CEX 腿现在也要求 qbus 报价在（detector 产 opp 前提下必有）。
// D-330：返回值扩 perLot（H4.2 spec 可行量下限换算 intent.NotionalUSD=qty×perLot 实额，取最小 diff）。
func computeBuyQty(opp pgstore.Opportunity, notionalUSD float64, usdPerLot func(canonical, venueKey, side string) (float64, bool)) (qty, perLot float64, skip bool) {
	perLot, ok := usdPerLot(opp.Canonical, opp.VenueBuy, "ask")
	if !ok || perLot <= 0 {
		log.Printf("[autotrade] skip opp=%s: USD-per-lot unavailable (fail-closed) canonical=%s venue=%s", opp.ID, opp.Canonical, opp.VenueBuy)
		return 0, 0, true
	}
	return notionalUSD / perLot, perLot, false
}

// xassetEntryGateMaxAge D-335：monitor 行龄上限（5min 采集节奏的 6 倍容差）；过期 fail-open。
const xassetEntryGateMaxAge = 30 * time.Minute

// xassetQtyFloor D-330 断层②：XASSET_CARRY qty<minStep 时的 spec 驱动可行量下限（§2.2）。
// MaxLongQty 复用为「spec 核准可行量」（cap 字段兼任——floorToStepQtyMin 无私有外出路，
// QtyBelowMinStep(bool) 是现成公共判据；误配大值由 gross 预闸 + pipeline checkXAssetComboAllow
// 终审双层兜底，安全方向正确）。gross 预闸=双腿 2×qty×perLot ≤ MaxGrossUSD（>0 时）。
// ok=false → 调用方维持静默跳过（原 H4.2 语义）；正常路径（qty≥minStep）不消费 MaxLongQty。
func xassetQtyFloor(spec *exec.XAssetComboAllowSpec, specQtyExecutable bool, perLot float64) (qty, notionalUSD float64, ok bool) {
	if spec == nil || spec.MaxLongQty <= 0 || !specQtyExecutable {
		return 0, 0, false
	}
	if spec.MaxGrossUSD > 0 && 2*spec.MaxLongQty*perLot > spec.MaxGrossUSD {
		return 0, 0, false
	}
	return spec.MaxLongQty, spec.MaxLongQty * perLot, true
}

// === D-140 S2：CS-miss 聚合告警 ===

// csMissAlertInterval CS-miss 告警 flush 间隔（D-140 S2）。
const csMissAlertInterval = 10 * time.Minute

// recordCSMiss 记一次 CS-miss（D-140 S2）。按 (venue,canonical) 聚合，
// 定期 flushCSMissAlerts 发 info 告警（同 key 去重 ≥1h）。
func (e *Engine) recordCSMiss(venue, canonical string) {
	key := venue + "|" + canonical
	e.csMissMu.Lock()
	e.csMissCounts[key]++
	e.csMissMu.Unlock()
}

// flushCSMissAlerts 发出聚合 CS-miss 告警（D-140 S2）。
// 由 positionLoop 定期调用（csMissAlertInterval）；同 key 去重 ≥1h。
func (e *Engine) flushCSMissAlerts() {
	e.csMissMu.Lock()
	if len(e.csMissCounts) == 0 {
		e.csMissMu.Unlock()
		return
	}
	counts := e.csMissCounts
	e.csMissCounts = make(map[string]int)
	now := time.Now()
	// 去重：跳过 1h 内已发过的 key
	var lines []string
	for key, n := range counts {
		if last, ok := e.csMissSent[key]; ok && now.Sub(last) < time.Hour {
			continue
		}
		e.csMissSent[key] = now
		lines = append(lines, fmt.Sprintf("%s count=%d", key, n))
	}
	e.csMissMu.Unlock()
	if len(lines) == 0 {
		return
	}
	msg := "unmapped-or-no-CS: " + strings.Join(lines, "; ")
	log.Printf("[autotrade] %s", msg)
	if e.AlertSink != nil {
		e.AlertSink("info", msg)
	}
}
