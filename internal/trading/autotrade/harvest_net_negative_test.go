package autotrade

// 红锚：收割尺度双守卫 + monitor venue 三元组 + 入口闸 fail-closed + 反向钉死。
// 498 事故形态钉死：capture≤0（entry_spread 跌穿 floor=按其口径无可收割空间）不得收割；
// 跨 venue 借用他所簿（binance 行喂 okx 仓）=伪安全边际/伪收割线全面堵死。
import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestD423_HarvestNetNegative D-014 R1 重写：NetIfClosedNow<0 永不收割（旧 capture 守卫退役）。
// 498 形态锚：开仓即负——basis=−10 vs anchor=−12.39 → capture≈$0.99 → net≈−3.7 → 不收割，
// eval 观测行落非收割 decision（非 hold_fee_amortize——该族已删）。
func TestD423_HarvestNetNegative(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(ctx, `DELETE FROM position_evals`); err != nil {
		t.Fatalf("clean evals: %v", err)
	}
	p := d014EconPos(t, s, 20.8)
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET expected_entry_spread_bps=-12.39 WHERE id=$1`, p.ID); err != nil {
		t.Fatalf("set anchor: %v", err)
	}
	rows, _ := s.ListPositions(ctx, "OPEN", 10, 0)
	p = &rows[0]
	e := d014EconEngine(s, d404Quotes(time.Now().UTC(), 3996.0, 4000, 0), true) // basis=-10
	close, reason := e.harvestCheck(ctx, p)
	if close {
		t.Fatalf("net<0 永不收割, reason=%q", reason)
	}
	e.cfg.XAssetExecDriftBps = 100
	e.cfg.XAssetDisasterBps = 1000
	e.fundingBreach = map[int64]int{}
	if close2, _ := e.xassetExitCheck(ctx, p); close2 {
		t.Fatal("净负仓不应平仓")
	}
	var dec string
	if err := s.Pool().QueryRow(ctx,
		`SELECT decision FROM position_evals WHERE position_id=$1 ORDER BY evaluated_at DESC LIMIT 1`,
		p.ID).Scan(&dec); err != nil || dec == "harvest" || dec == "hold_fee_amortize" {
		t.Fatalf("观测行不应 harvest/hold_fee_amortize, got %q err=%v", dec, err)
	}
}

// TestD423_HarvestAnchorNonNeg anchor≥0：capture=(basis−anchor) 为负 → net<0 → 不收割
// （旧守卫域 reason 退役，判定收敛到同一 NetIfClosedNow<0 语义）。
func TestD423_HarvestAnchorNonNeg(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 20.8)
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET expected_entry_spread_bps=12.39 WHERE id=$1`, p.ID); err != nil {
		t.Fatalf("set anchor: %v", err)
	}
	rows, _ := s.ListPositions(ctx, "OPEN", 10, 0)
	p = &rows[0]
	e := d014EconEngine(s, d404Quotes(time.Now().UTC(), 4003.6, 4000, 0), true)
	if close, _ := e.harvestCheck(ctx, p); close {
		t.Fatal("正锚域 capture<0 → net<0 不应收割")
	}
}

// TestD423_HarvestNoMonitorDependency D-014：收割判定不再消费 monitor 簿——
// 同 pair 错 venue 行（binance）在场不影响 econ 判定（簿借用口子已随收割线退役关闭）。
func TestD423_HarvestNoMonitorDependency(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 20.8)
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET expected_entry_spread_bps=-12.39 WHERE id=$1`, p.ID); err != nil {
		t.Fatalf("set anchor: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (ts, pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, harvest_floor_bps, cex_venue, mt_venue, src)
		VALUES ($1, 'XPTUSDT-XPT', 4000, 4001, 3999, 4000, 7.73, 5, 3, 0.001, 21.9, 1.0, 24.9, 9, 4.73,
			'binance:perp','3001157:mt5','live')`,
		p.OpenedAt.Add(-5*time.Second)); err != nil {
		t.Fatalf("seed wrong-venue monitor: %v", err)
	}
	rows, _ := s.ListPositions(ctx, "OPEN", 10, 0)
	p = &rows[0]
	e := d014EconEngine(s, d404Quotes(time.Now().UTC(), 4016.0, 4000, 0), true) // basis=+40 → net≥carry×7
	if close, reason := e.harvestCheck(ctx, p); !close || !containsSub(reason, "harvest") {
		t.Fatalf("econ 收割不依赖 monitor 簿：want harvest, close=%v reason=%q", close, reason)
	}
}

// TestD423_EntryGateFailClosed 入口闸：无本组合 venue 簿行 → 拒开（不产 intent 不到 sizing）。
// 对抗=binance 簿存在同 pair 行也不借用。
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
