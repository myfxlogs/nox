// d193_settle_test.go —— D-193-A S6：结算 fallback 消除 + 净 PNL 事实迁移单测。
//
// 覆盖：cashflow 查询失败不 fallback、quality/completeness 判定、net_realized_usd 写入、
// stats_status 短路/重试语义。使用 fakeDeps（无需 DB）。
package settlement

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d193BaseDeps 构造一个 SWAPARB CLOSED 持仓的 fakeDeps（complete+actual 路径用）。
func d193BaseDeps(posID int64, rp float64) *fakeDeps {
	opened := time.Now().AddDate(0, 0, -7) // 持仓 7 天，确保 expectedUsd>0
	closed := time.Now()
	swapBpsDaily := 0.5
	return &fakeDeps{
		position: &pgstore.Position{
			ID:                   posID,
			OpportunityID:        "SWAPARB:AUDJPY:529:mt5:277:mt5",
			Type:                 "SWAPARB",
			Status:               "CLOSED",
			ExpectedNetBps:       15.0,
			ExpectedSwapBpsDaily: &swapBpsDaily,
			RealizedPnl:          &rp,
			OpenedAt:             &opened,
			ClosedAt:             &closed,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "529:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:           100000,
		csOk:         true,
		fxRate:       150,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: 3.0,
	}
}

// TestSettle_CashflowQueryFail_NoFallback D-193-A S6：查询失败 → 写 unknown/retryable 行，不调统计，返回 error。
func TestSettle_CashflowQueryFail_NoFallback(t *testing.T) {
	d := d193BaseDeps(100, 10.0)
	d.cashflowsQueryErr = errors.New("db down")
	err := Settle(context.Background(), d, 100, noElim, nil)
	if err == nil {
		t.Fatal("Settle 应返回 error（cashflow 查询失败），但返回 nil")
	}
	if d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("查询失败不应更新统计：upsert=%d updateStat=%d", d.upsertVSample, d.updateStat)
	}
	if d.lastSettlementRow == nil {
		t.Fatal("应写入 unknown settlement 行")
	}
	if d.lastSettlementRow.CompletenessStatus != "unknown" {
		t.Errorf("completeness=%q, want unknown", d.lastSettlementRow.CompletenessStatus)
	}
	if d.lastSettlementRow.StatsStatus != "retryable" {
		t.Errorf("stats_status=%q, want retryable", d.lastSettlementRow.StatsStatus)
	}
}

// TestSettle_CashflowQuality_Unknown_NotComplete D-193-A S6：cfQuality=unknown → completeness=unknown，不更新统计。
func TestSettle_CashflowQuality_Unknown_NotComplete(t *testing.T) {
	d := d193BaseDeps(101, 10.0)
	d.cashflowsQuality = "unknown"
	if err := Settle(context.Background(), d, 101, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("unknown 不应更新统计：upsert=%d updateStat=%d", d.upsertVSample, d.updateStat)
	}
	if d.lastSettlementRow.CompletenessStatus != "unknown" {
		t.Errorf("completeness=%q, want unknown", d.lastSettlementRow.CompletenessStatus)
	}
	if d.lastSettlementRow.StatsStatus != "retryable" {
		t.Errorf("stats_status=%q, want retryable", d.lastSettlementRow.StatsStatus)
	}
}

// TestSettle_CashflowQuality_Estimated_NotActual D-193-A S6：cfQuality=estimated → pnlQuality=estimated，不更新统计。
func TestSettle_CashflowQuality_Estimated_NotActual(t *testing.T) {
	d := d193BaseDeps(102, 10.0)
	d.cashflowsQuality = "estimated"
	if err := Settle(context.Background(), d, 102, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("estimated 不应更新统计：upsert=%d updateStat=%d", d.upsertVSample, d.updateStat)
	}
	if d.lastSettlementRow.PnlQuality != "estimated" {
		t.Errorf("pnl_quality=%q, want estimated", d.lastSettlementRow.PnlQuality)
	}
	if d.lastSettlementRow.StatsStatus != "skipped" {
		t.Errorf("stats_status=%q, want skipped", d.lastSettlementRow.StatsStatus)
	}
}

// TestSettle_NetRealizedUsd_Written D-193-A S6：complete+actual → net_realized_usd = price_pnl + cashflows_net，stats_status=applied。
func TestSettle_NetRealizedUsd_Written(t *testing.T) {
	d := d193BaseDeps(103, 100.0)
	d.cashflowsNet = 3.0
	d.cashflowsQuality = "actual"
	if err := Settle(context.Background(), d, 103, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.upsertVSample != 1 || d.updateStat != 1 {
		t.Errorf("complete+actual 应更新统计：upsert=%d updateStat=%d", d.upsertVSample, d.updateStat)
	}
	if d.lastSettlementRow == nil || d.lastSettlementRow.NetRealizedUsd == nil {
		t.Fatal("net_realized_usd 未写入")
	}
	// D-013 ②：net_realized_usd=realized_pnl（PositionNetUSD 单源净额），cashflows 仅组件列。
	if !approx(*d.lastSettlementRow.NetRealizedUsd, 100.0, 0.001) {
		t.Errorf("net_realized_usd=%.4f, want 100.0", *d.lastSettlementRow.NetRealizedUsd)
	}
	if d.lastSettlementRow.StatsStatus != "applied" {
		t.Errorf("stats_status=%q, want applied", d.lastSettlementRow.StatsStatus)
	}
	if !approx(d.lastNetPnl, 100.0, 0.001) {
		t.Errorf("UpdateStrategyStat net=%.4f, want 100.0", d.lastNetPnl)
	}
}

// TestSettle_SkippedReason_StillSkipped D-193-A S6：realized NULL → completeness=skipped，stats_status=skipped。
func TestSettle_SkippedReason_StillSkipped(t *testing.T) {
	now := time.Now()
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             104,
			OpportunityID:  "SWAPARB:AUDJPY:529:mt5:277:mt5",
			Type:           "SWAPARB",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    nil, // → skipped
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "529:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true,
	}
	if err := Settle(context.Background(), d, 104, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("skipped 不应更新统计：upsert=%d updateStat=%d", d.upsertVSample, d.updateStat)
	}
	if d.lastSettlementRow == nil {
		t.Fatal("应写入 skipped settlement 行")
	}
	if d.lastSettlementRow.CompletenessStatus != "skipped" {
		t.Errorf("completeness=%q, want skipped", d.lastSettlementRow.CompletenessStatus)
	}
	if d.lastSettlementRow.StatsStatus != "skipped" {
		t.Errorf("stats_status=%q, want skipped", d.lastSettlementRow.StatsStatus)
	}
}

// TestSettle_Retryable_RetriesOnNextCall D-193 审计 P0-1：stats_status=retryable → 不短路，继续走流程。
func TestSettle_Retryable_RetriesOnNextCall(t *testing.T) {
	d := d193BaseDeps(105, 100.0)
	d.statsStatus = "retryable" // 已有 retryable 行 → 不短路，重试
	d.cashflowsQuality = "actual"
	if err := Settle(context.Background(), d, 105, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// retryable 不短路 → 走完整流程 → 统计更新
	if d.upsertVSample != 1 {
		t.Errorf("retryable 应重试（不短路）：upsertVSample=%d, want 1", d.upsertVSample)
	}
}

// TestSettle_Applied_ShortCircuits D-193 审计 P0-1：stats_status=applied → 短路返回 nil。
func TestSettle_Applied_ShortCircuits(t *testing.T) {
	d := d193BaseDeps(106, 100.0)
	d.statsStatus = "applied"
	if err := Settle(context.Background(), d, 106, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 0 || d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("applied 应短路：insert=%d upsert=%d updateStat=%d", d.insertSettlement, d.upsertVSample, d.updateStat)
	}
}

// TestSettle_StatsUpdateFail_Retryable D-193 审计 P0-2：UpsertVerificationSample 失败 → stats_status 改为 retryable。
func TestSettle_StatsUpdateFail_Retryable(t *testing.T) {
	base := d193BaseDeps(107, 100.0)
	base.cashflowsQuality = "actual"
	d := &failingDeps{fakeDeps: base, upsertErr: errors.New("db down")}
	err := Settle(context.Background(), d, 107, noElim, nil)
	if err == nil {
		t.Fatal("Settle 应返回 error（upsert 失败），但返回 nil")
	}
	if base.markRetryableCount != 1 {
		t.Errorf("MarkSettlementStatsRetryable 调用次数=%d, want 1", base.markRetryableCount)
	}
}

// TestHasCashflowKinds D-331 T2：XASSET_CARRY 归「应有现金流」型——该型仓位 funding
// 现金流缺失必须走 no_cashflows→unknown→retryable（D-193 保守口径），不再静默 skipped；
// CROSS_VENUE/MEANREVERT 负例=无 swap/funding 现金流型，no_cashflows 属正常状态。
func TestHasCashflowKinds(t *testing.T) {
	for _, typ := range []string{"SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING", "XASSET_CARRY"} {
		if !hasCashflowKinds(typ) {
			t.Errorf("hasCashflowKinds(%q) = false, want true", typ)
		}
	}
	if hasCashflowKinds("CROSS_VENUE") {
		t.Error(`hasCashflowKinds("CROSS_VENUE") = true, want false（无现金流型负例）`)
	}
	if hasCashflowKinds("MEANREVERT") {
		t.Error(`hasCashflowKinds("MEANREVERT") = true, want false（无现金流型负例）`)
	}
}
