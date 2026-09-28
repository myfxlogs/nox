package autotrade

// D-423 S1 红锚：收割尺度双守卫 + monitor venue 三元组 + 入口闸 fail-closed + 反向钉死。
// 498 事故形态钉死：capture≤0（entry_spread 跌穿 floor=按其口径无可收割空间）不得收割；
// 跨 venue 借用他所簿（binance 行喂 okx 仓）=伪安全边际/伪收割线全面堵死。
import (
	"context"
	"testing"
	"time"

	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// TestD423_HarvestNoCapture capture≤0 守卫：anchor=−12.39/floor=4.73，开仓时点
// monitor 行 entry=4.26（498 形态：入场即跌穿 floor）→ capture=−0.47≤0 →
// 不收割，reason=hold_anchor_nonneg?否——hold_no_capture；且观测行落同 decision。
func TestD423_HarvestNoCapture(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(ctx, `DELETE FROM position_evals`); err != nil {
		t.Fatalf("clean evals: %v", err)
	}
	p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73))
	// 498 实证行：开仓前 5min 行 entry=7.73（floor 源）+ 开仓后 14s 行 entry=4.26。
	// 谓词 ts<=opened_at 选前 → capture=7.73−4.73=3>0 非本例；构造 capture≤0：
	// 开仓前唯一行 entry=4.26 → capture=4.26−4.73=−0.47≤0。
	d404SeedMonitorRowAt(t, s, p.OpenedAt.Add(-5*time.Second), 4.26, 4.73)
	e := d404Engine(s, d404Quotes(time.Now().UTC(), 3996.0, 4000, 0)) // basis=−10 ≤ line 旧口径会误收
	close, reason := e.harvestCheck(ctx, p)
	if close || reason != "hold_no_capture" {
		t.Fatalf("capture≤0 应 hold_no_capture 不收割（旧口径 line=12.86 误杀）, close=%v reason=%q", close, reason)
	}
	// 观测链：xassetExitCheck 落 decision=hold_no_capture 行。
	// 漂移/灾难闸抬阈隔离本票断言对象（fill=0 vs anchor −12.39 恒漂）。
	e.cfg.XAssetExecDriftBps = 100
	e.cfg.XAssetDisasterBps = 1000
	e.fundingBreach = map[int64]int{}
	if close2, _ := e.xassetExitCheck(ctx, p); close2 {
		t.Fatal("守卫域不应平仓")
	}
	var dec string
	if err := s.Pool().QueryRow(ctx,
		`SELECT decision FROM position_evals WHERE position_id=$1 ORDER BY evaluated_at DESC LIMIT 1`,
		p.ID).Scan(&dec); err != nil || dec != "hold_no_capture" {
		t.Fatalf("观测行 want hold_no_capture, got %q err=%v", dec, err)
	}
}

// TestD423_HarvestAnchorNonNeg anchor≥0 守卫（与 D-404 正锚改义互补锚）：reason 钉死。
func TestD423_HarvestAnchorNonNeg(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d404XAssetPos(t, s, d341fptr(12.39), d341fptr(4.73))
	d404SeedMonitorRowAt(t, s, p.OpenedAt.Add(-5*time.Second), 7.73, 4.73)
	e := d404Engine(s, d404Quotes(time.Now().UTC(), 4003.6, 4000, 0))
	if close, reason := e.harvestCheck(ctx, p); close || reason != "hold_anchor_nonneg" {
		t.Fatalf("anchor≥0 应 hold_anchor_nonneg, close=%v reason=%q", close, reason)
	}
}

// TestD423_VenueTripleIsolation 三元组隔离：只有 binance:perp×3001157:mt5 簿行，
// okx:perp×3001157:mt5 仓不得命中——真腿路径 found=false → 代理路径同键 miss →
// hold_no_monitor（红=pair-only 查询借用 binance 行算出假 capture）。
func TestD423_VenueTripleIsolation(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73)) // 仓腿=okx:perp×3001157:mt5
	// 同 pair 错 venue 行（binance:perp）——若查询不带 venue 即借用成功。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (ts, pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, harvest_floor_bps, cex_venue, mt_venue)
		VALUES ($1, 'XPTUSDT-XPT', 4000, 4001, 3999, 4000, 7.73, 5, 3, 0.001, 21.9, 1.0, 24.9, 9, 4.73,
			'binance:perp','3001157:mt5')`,
		p.OpenedAt.Add(-5*time.Second)); err != nil {
		t.Fatalf("seed wrong-venue monitor: %v", err)
	}
	e := d404Engine(s, d404Quotes(time.Now().UTC(), 3996.0, 4000, 0))
	close, reason := e.harvestCheck(ctx, p)
	if close || reason != "hold_no_monitor" {
		t.Fatalf("okx 仓不得借 binance 簿：want hold_no_monitor, close=%v reason=%q", close, reason)
	}
}

// TestD423_EntryGateFailClosed 入口闸：无本组合 venue 簿行 → 拒开（不产 intent 不到 sizing）。
// 对抗=binance 簿存在同 pair 行也不借用。
func TestD423_EntryGateFailClosed(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(ctx, `TRUNCATE strategy_stats, fin_xasset_monitor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	d335SeedRegime(t, s)
	d330SeedDisabled(t, s)
	e, opp, ctx := d335Engine(s, 12) // opp=277842155:mt5×binance:perp
	// 只种错 venue 行（okx:perp×277842155:mt5）——pair 同但 cex_venue 不同不得命中。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, cex_venue, mt_venue)
		VALUES ('XAUUSDT-XAU', 4355, 4356, 4354, 4355, 12, 5, 3, 0.001, 21.9, 1.0, 24.9, 9,
			'okx:perp','277842155:mt5')`); err != nil {
		t.Fatalf("seed wrong-venue: %v", err)
	}
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if e.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("binance 簿缺（仅 okx 行）应 fail-closed 拒开不借用, got %v", e.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Errorf("拒开不应产 intent, got %d 行", n)
	}
}

// TestD423_ReverseComboRejected 反向组合钉死：VenueBuy=CEX（CEX 为买腿/SELL 为 MT）
// monitor 方向语义未定义 → 拒开（红=闸不查方向照样放行）。
func TestD423_ReverseComboRejected(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(ctx, `TRUNCATE strategy_stats, fin_xasset_monitor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	d335SeedRegime(t, s)
	d330SeedDisabled(t, s)
	e, opp, ctx := d335Engine(s, 12)
	// 反向：买腿=binance:perp（CEX），卖腿=277842155:mt5（MT）——方向未定义拒开。
	opp.VenueBuy, opp.VenueSell = "binance:perp", "277842155:mt5"
	opp.ID = "XASSET_CARRY:XAUUSD:binance:perp:277842155:mt5"
	e.cfg.XAssetAllowCombos = []exec.XAssetComboAllowSpec{{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT",
		VenueBuy: "binance:perp", VenueSell: "277842155:mt5",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUSD: 10000,
	}}
	// 即便同 pair 有 monitor 行（任意 venue）也不得放行——方向闸先行。
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(12), floatPtr(9))
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if len(e.csMissCounts) != 0 {
		t.Errorf("反向组合应拒开（未定义 monitor 方向）, csMiss=%v", e.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Errorf("反向组合不应产 intent, got %d 行", n)
	}
}

// TestD423_XassetPairKeyTriple pairKey 三元组解析锚：仓腿 venue 优先于机会字段
// （腿=实际在场 venue；机会字段兜底）。
func TestD423_XassetPairKeyTriple(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73))
	pair, cex, mt, ok := e_xassetPairKey(t, s, ctx, p)
	if !ok || pair != "XPTUSDT-XPT" || cex != "okx:perp" || mt != "3001157:mt5" {
		t.Fatalf("triple=(%s,%s,%s) ok=%v", pair, cex, mt, ok)
	}
}

func e_xassetPairKey(t *testing.T, s *pgstore.Store, ctx context.Context, p *pgstore.Position) (string, string, string, bool) {
	t.Helper()
	e := d404Engine(s, nil)
	return e.xassetPairKey(ctx, p)
}
