// Package settlement 结算钩子（D-087 §3.2）。
//
// 每笔平仓有唯一、幂等、可审计的结算动作：strategy_stats / verification_stats / 淘汰统计同源更新。
// Settle 是唯一结算入口，三个调用点（手动平仓 RPC / scanAndClose / reconcile auto_close）统一替换。
package settlement

import (
	"context"
	"fmt"
	"log"
	"nox/internal/data/keydomain"
	"strings"
	"time"

	"nox/internal/review/verification"
	"nox/internal/store/pgstore"
)

// 注入面类型 Deps/Eliminator/FamilyResolver 在 settlement_deps.go（D-042 R2 拆分保行数）。

// Settle 唯一结算入口（D-087 §3.2，D-193-A 消除 fallback + 净 PNL 事实迁移）。
// 幂等短路：stats_status=applied/skipped → 直接返回（D-193 审计 P0-1）。
// 跳过规则（写行但不入样本）：realized NULL / expectedUsd≤0 / fx 缺失 / pnl_estimated。
// cashflows 查询失败 → 写 unknown/retryable 行 + 返回 error（消除 fallback-to-zero）。
// 通过（complete+actual）→ UpsertVerificationSample + UpdateStrategyStat + 淘汰检查；
// 统计更新失败 → MarkSettlementStatsRetryable + 返回 error（D-193 审计 P0-2）。
// family 非 nil 时淘汰聚合改用族级（CARRY/SWAPARB 合并样本）。
func SettleAt(ctx context.Context, deps Deps, posID int64, elim Eliminator, family FamilyResolver, now time.Time) error {
	// 审计 P0-1：短路逻辑改为检查 stats_status。
	// applied/skipped → 已完成，短路返回 nil。
	// pending/retryable/""（无行或 legacy）→ 继续走 Settle 流程。
	statsStatus, err := deps.GetSettlementStatsStatus(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: check existing stats status: %w", err)
	}
	if statsStatus == "applied" || statsStatus == "skipped" {
		return nil // 已完成，幂等短路
	}

	// 1. 读 position
	p, err := deps.GetPosition(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: get position %d: %w", posID, err)
	}
	if p.Status != "CLOSED" {
		return fmt.Errorf("settle: position %d not CLOSED (status=%s)", posID, p.Status)
	}

	// 2. 派生 vkey
	vkey := verification.VkeyFromOppID(p.OpportunityID)

	// 3. 取腿算 notional + holdDays
	legs, err := deps.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: list legs: %w", err)
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	notionalUsd, fxSkipped := computeNotionalUSD(ctx, deps, legs, canonical)
	holdDays := computeHoldDays(p, legs)

	// 4. 算 expectedUsd（D-087 §3.4 typed 新签名：netBps + swapBpsDaily 按字段取值）
	netBps := p.ExpectedNetBps
	swapBpsDaily := 0.0
	if p.ExpectedSwapBpsDaily != nil {
		swapBpsDaily = *p.ExpectedSwapBpsDaily
	}
	expectedUsd := verification.ExpectedUsd(p.Type, netBps, swapBpsDaily, notionalUsd, holdDays)

	// 5. 跳过规则
	skippedReason := ""
	switch {
	case p.Type == pgstore.PositionTypePROBE:
		// D-047④：探针仓写 settlement 审计行但永入样本/策略统计/淘汰
		//（stats_status='skipped'；现金流入账本同其它仓）。
		skippedReason = "probe position"
	case p.RealizedPnl == nil:
		skippedReason = "realized_pnl is NULL"
	case fxSkipped:
		skippedReason = "fx: quote ccy unavailable"
	case expectedUsd <= 0:
		skippedReason = "expected_usd <= 0"
	case strings.Contains(p.Note, "pnl_estimated"): // D-159 W3a R4：estimated 不入正式统计
		skippedReason = "pnl_estimated: partial or no calibration"
	}

	// 6. D-193-A：cashflows 查询 + 净 PNL + completeness 判定（在 InsertSettlement 前完成，
	//    因 row 需写入 net_realized_usd/pnl_quality/completeness_status/stats_status）。
	var (
		cashflowsNet   float64
		cfQuality      string
		pnlQuality     string
		completeness   string
		rowStatsStatus string
		netPnl         float64
		pricePnl       float64
	)
	if skippedReason == "" {
		// 消除 fallback-to-zero。查询失败 → settlement 进入 unknown/retryable，不写 complete，不更新统计。
		// funding 腿判据=FILLED OPEN 的 %:perp 腿（funding 经济上仅存于 perp 合约；
		// 不复用 swapledger.isFundingLeg——其对非 XASSET 类型忽略 venue 直取 isFundingType，
		// 会漏非 funding 型仓持 perp 腿实收的场景）。
		hasFundingLeg := false
		for _, l := range legs {
			if l.Phase == "OPEN" && l.Status == "FILLED" && keydomain.IsType(l.Venue, "perp") {
				hasFundingLeg = true
				break
			}
		}
		fundingDue := holdDays >= 0.5 // ≥12h 必跨≥1个 funding 窗（OKX 00/08/16 UTC，窗距8h）
		cashflowsNet, cfQuality, err = deps.SumCashflowsNetForSettle(ctx, posID, hasFundingLeg, fundingDue)
		if err != nil {
			mv := 1
			errRow := &pgstore.SettlementRow{
				PositionID:             posID,
				Vkey:                   vkey,
				ExpectedUsd:            expectedUsd,
				SkippedReason:          "cashflow query failed: " + err.Error(),
				SettledAt:              now,
				PnlQuality:             "unknown",
				CompletenessStatus:     "unknown",
				StatsStatus:            "retryable",
				SettlementModelVersion: &mv,
				SettlementEventKey:     fmt.Sprintf("settle:%d", posID),
			}
			if p.RealizedPnl != nil {
				errRow.PricePnlUsd = p.RealizedPnl
				errRow.RealizedUsd = *p.RealizedPnl
			}
			_, _ = deps.InsertSettlement(ctx, errRow) // best-effort 写入，错误已返回
			return fmt.Errorf("settle: cashflow query pos=%d: %w", posID, err)
		}
		// ②：net_pnl = positions.realized_pnl——exec 侧经 pgstore.PositionNetUSD
		// 单源写入（价差+funding/swap+commission），结算只传播同一值，不再二次加现金流。
		// cashflowsNet 仅作组件列与 quality/completeness 判据；pricePnl 组件=净额−现金流。
		// ②：EXTERNAL/CEX FILLED 平仓腿 fee NULL → partial（MT 恒显式 0 行不算缺；retryable 可升级）。
		hasPartialFeeLeg := false
		for _, l := range legs {
			if l.Phase == "CLOSE" && l.FeeUSD == nil &&
				(l.Status == "EXTERNAL" || (l.Status == "FILLED" && !keydomain.IsMTKey(l.Venue))) {
				hasPartialFeeLeg = true
				break
			}
		}
		netPnl = *p.RealizedPnl
		pricePnl = netPnl - cashflowsNet
		pnlQuality, completeness = "actual", "complete"
		if hasPartialFeeLeg {
			completeness = "partial"
		}
		if cfQuality == "unknown" {
			pnlQuality = "unknown"
			completeness = "unknown"
		} else if cfQuality == "estimated" {
			pnlQuality = "estimated" // estimated 不入正式统计（stats 层不消费）
		} else if cfQuality == "no_cashflows" && hasCashflowKinds(p.Type) {
			// 审计 P1-3：传 p.Type 不是 posID
			// 应有现金流但查不到 → unknown
			pnlQuality = "unknown"
			completeness = "unknown"
		}
		// 审计 P0-2：stats_status 在 InsertSettlement 时直接写入正确值，不用二次 UPDATE。
		// complete+actual → stats_status='applied'；非 complete/actual → 'skipped' 或 'retryable'。
		rowStatsStatus = "applied"
		if completeness != "complete" || pnlQuality != "actual" {
			if completeness == "unknown" || completeness == "partial" {
				rowStatsStatus = "retryable"
			} else {
				rowStatsStatus = "skipped"
			}
		}
	} else {
		// skipped 行：completeness=skipped，pnlQuality=unknown，stats_status=skipped
		pnlQuality = "unknown"
		completeness = "skipped"
		rowStatsStatus = "skipped"
		if p.RealizedPnl != nil {
			pricePnl = *p.RealizedPnl
		}
	}

	// 7. 写 position_settlements（含 D-193-A 净 PNL + 完整性字段；ON CONFLICT DO NOTHING 防并发）
	mv := 1
	row := &pgstore.SettlementRow{
		PositionID:             posID,
		Vkey:                   vkey,
		ExpectedUsd:            expectedUsd,
		HoldDays:               holdDays,
		SkippedReason:          skippedReason,
		SettledAt:              now,
		PnlQuality:             pnlQuality,
		CompletenessStatus:     completeness,
		StatsStatus:            rowStatsStatus, // D-193 审计 P0-2：直接写最终值，无二次 UPDATE
		SettlementModelVersion: &mv,
		SettlementEventKey:     fmt.Sprintf("settle:%d", posID),
	}
	if skippedReason == "" {
		row.NotionalUsd = &notionalUsd // 非跳过行写真值
		row.PricePnlUsd = &pricePnl
		row.CashflowsNetUsd = &cashflowsNet
		row.NetRealizedUsd = &netPnl
	} // 跳过行 NotionalUsd=nil → INSERT 写 NULL
	if p.RealizedPnl != nil {
		row.RealizedUsd = *p.RealizedPnl // 兼容旧查询
	}
	won, err := deps.InsertSettlement(ctx, row)
	if err != nil {
		return fmt.Errorf("settle: insert settlement: %w", err)
	}
	if !won {
		log.Printf("[settlement] pos=%d concurrent loser (settlement already exists), skipping stat update", posID)
		return nil
	}

	// ①：结算行已落账——30d 账本回撤线等 post-settle 风控检查（可选钩子，
	// 行可见于本条查询连接后触发；并发输家路径不触发，赢家侧会带）。
	if h, ok := deps.(PostSettler); ok {
		h.PostSettled(ctx, posID)
	}

	// 8. 非 complete/actual → 不入样本不入统计，直接返回
	if rowStatsStatus != "applied" {
		if skippedReason != "" {
			log.Printf("[settlement] pos=%d skipped: %s", posID, skippedReason)
		} else {
			log.Printf("[settlement] pos=%d not complete/actual: completeness=%s quality=%s stats=%s",
				posID, completeness, pnlQuality, rowStatsStatus)
		}
		return nil
	}

	// 9. 通过（complete+actual）→ UpsertVerificationSample + UpdateStrategyStat + 淘汰
	// netPnl = price_pnl + cashflows_net（净值一次合成，无双计）。
	closedAt := now
	if p.ClosedAt != nil {
		closedAt = *p.ClosedAt
	}
	if err := deps.UpsertVerificationSample(ctx, vkey, expectedUsd, netPnl, closedAt); err != nil {
		// 审计 P0-2：统计更新失败 → 改 stats_status 为 retryable，下轮重试。
		_ = deps.MarkSettlementStatsRetryable(ctx, posID)
		return fmt.Errorf("settle: upsert verification pos=%d: %w", posID, err)
	}
	if err := deps.UpdateStrategyStat(ctx, p.Type, netPnl); err != nil {
		_ = deps.MarkSettlementStatsRetryable(ctx, posID)
		return fmt.Errorf("settle: update strategy stat pos=%d: %w", posID, err)
	}

	// 淘汰检查（D-261 S5：提取至 settlement_elim.go runElimination）。
	runElimination(ctx, deps, p, elim, family)
	// XASSET_CARRY 预测-实测对照（观测面非钱闸——失败只 log 不阻塞）。
	checkXassetPredictVsActual(ctx, deps, p, posID, cashflowsNet)
	return nil
}
