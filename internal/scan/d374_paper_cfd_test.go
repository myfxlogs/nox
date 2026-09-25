// d374_paper_cfd_test.go —— D-374 B 面：cfd×perp 纸面轨锚（先红后绿，真库 full-step）。
// 生产实证：fin_carry_universe 浮现 SOL long_cfd_short_perp 277842155×binance net=10.95
// （MT5 facts swap_long=0=真 swap-free）——cfd 结构此前零验证通道。
package scan

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/fact"
)

// d374Row 构造 cfd 结构 UniverseRow（note 默认 clean 空串）。
func d374Row(canonical, mtVenue, cexVenue, structure string, net float64, note string) UniverseRow {
	return UniverseRow{Canonical: canonical, MtVenue: mtVenue, CexVenue: cexVenue,
		NetAnnual: net, FundingAnn: net + 0.15, Structure: structure, Note: note}
}

// TestD374_CfdOpen_ComboVenue 锚B1：long_cfd_short_perp net=10.95 note=” → 开纸面行，
// venue=`mt×cex` 组合串、symbol=canonical、structure/side/mt_venue 落列正确。
func TestD374_CfdOpen_ComboVenue(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	rows := []UniverseRow{d374Row("SOL", "277842155", "binance", "long_cfd_short_perp", 10.95, "")}
	opened, exited, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || opened[0] != "SOL" || exited != 0 {
		t.Fatalf("锚B1: want opened=[SOL] exited=0, got opened=%v exited=%d", opened, exited)
	}
	active, err := s.ListActivePaperSpotCarry(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("锚B1: want 1 active row, got %d", len(active))
	}
	p := active[0]
	if p.Venue != "277842155×binance" || p.Symbol != "SOL" {
		t.Fatalf("锚B1: venue/symbol = %q|%q, want 277842155×binance|SOL", p.Venue, p.Symbol)
	}
	if p.Structure != "long_cfd_short_perp" || p.Side != "short_perp" {
		t.Fatalf("锚B1: structure/side = %q/%q, want long_cfd_short_perp/short_perp", p.Structure, p.Side)
	}
	if p.MtVenue == nil || *p.MtVenue != "277842155" {
		t.Fatalf("锚B1: mt_venue = %v, want 277842155", p.MtVenue)
	}
	if p.EntryBasisBps != nil {
		t.Fatalf("锚B1: entry_basis_bps want NULL 诚实缺, got %v", *p.EntryBasisBps)
	}
}

// TestD374_CfdOpen_Rejected 锚B2/B3：net=7（地板下）不开；note≠”（D-345 suspect）不开。
func TestD374_CfdOpen_Rejected(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	rows := []UniverseRow{
		d374Row("SOL", "277842155", "binance", "long_cfd_short_perp", 7, ""),           // 地板 8 下
		d374Row("XRP", "277842155", "binance", "long_cfd_short_perp", 12, "swap 证据不足"), // suspect 行
	}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("锚B2/B3: want 0 opened, got %v", opened)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 0 {
		t.Fatalf("锚B2/B3: want 0 active, got %d", len(active))
	}
}

// TestD374_CfdExit_MissStreak 锚B4（D-474a S4 改写）：行从 res.Rows 消失 → miss_streak
// 独立计数（low_streak 不受缺席污染），连缺 3 轮 → data_lost 退出（不混入 normalized）。
func TestD374_CfdExit_MissStreak(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	rows := []UniverseRow{d374Row("SOL", "277842155", "binance", "long_cfd_short_perp", 10.95, "")}
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	// miss#1/2：miss_streak 递增、low_streak 恒 0，行仍 active 不退
	for i := 1; i <= 2; i++ {
		_, exited, err := StepPaperCfdCarry(ctx, s, nil, time.Now(), d410Opts(), nil)
		if err != nil {
			t.Fatalf("miss%d: %v", i, err)
		}
		if exited != 0 {
			t.Fatalf("锚B4: miss#%d want 0 exited, got %d", i, exited)
		}
		active, _ := s.ListActivePaperSpotCarry(ctx)
		if len(active) != 1 || active[0].LowStreak != 0 || active[0].MissStreak != i {
			t.Fatalf("锚B4: miss#%d want 1 active low=0 miss=%d, got %+v", i, i, active)
		}
	}
	// miss#3：达连缺线 → data_lost
	_, exited3, err := StepPaperCfdCarry(ctx, s, nil, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("miss3: %v", err)
	}
	if exited3 != 1 {
		t.Fatalf("锚B4: miss#3 want 1 exited, got %d", exited3)
	}
	board, err := s.ListPaperSpotCarryBoard(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	found := false
	for _, p := range board {
		if p.Symbol == "SOL" && p.Status == "exited" {
			found = true
			if p.ExitReason != "data_lost" {
				t.Fatalf("锚B4: exit_reason = %q, want data_lost", p.ExitReason)
			}
		}
	}
	if !found {
		t.Fatal("锚B4: exited SOL row not found in board")
	}
}

// TestD374_CfdAccrual 锚B5：accrual 数值断言（net=10.95、qty=$1000、1 整天 → +0.30）
// +低 net streak 轨（net<8 → streak=1 不退；回稳 → 归零）。
func TestD374_CfdAccrual(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Now()
	rows := []UniverseRow{d374Row("SOL", "277842155", "binance", "long_cfd_short_perp", 10.95, "")}
	// D-474a S1：结算对齐累积需双腿事实面——cex funding 边界+MT swap 边界+符号映射。
	seedUni(t, s, "binance", "SOL", "TRADING", "T3", 72*time.Hour, 11.1)
	seedSettleFacts(t, s, "binance", "SOL", now)
	if _, err := s.Pool().Exec(ctx, `INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary)
		VALUES ('277842155','SOLUSD','SOL',true) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed symbol map: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: "swap_long", Venue: "277842155", Symbol: "SOLUSD", Value: 0, Ts: now}, // swap-free 腿=0
	}); err != nil {
		t.Fatalf("seed swap facts: %v", err)
	}
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, now, d410Opts(), nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 回拨 last_accrue_ts 24h → 本轮窗 (now−24h, now] 恰 3 个 8h 界
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET last_accrue_ts = now() - interval '24 hours'`); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, now, d410Opts(), nil); err != nil {
		t.Fatalf("accrue round: %v", err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 {
		t.Fatalf("锚B5: want 1 active, got %d", len(active))
	}
	p := active[0]
	// 结算对齐：24h 窗恰 3 个 8h funding 界 × 当期率 11.1%/yr；MT 腿 swap=0。
	// funding 腿金额 = 11.1/100 × 1000 × 3/1095（每界占年化 8h/8760h）。
	want := 11.1 / 100 * p.QtyUSD * (3 * 8.0 / 8760.0)
	if math.Abs(p.AccruedUSD-want) > 1e-6 {
		t.Fatalf("锚B5: accrued = %v, want %v", p.AccruedUSD, want)
	}
	if p.LowStreak != 0 {
		t.Fatalf("锚B5: net=10.95 want streak=0, got %d", p.LowStreak)
	}
	// 下一轮 net=7 → streak=1（未达 2 轮不退）且照常累积
	low := []UniverseRow{d374Row("SOL", "277842155", "binance", "long_cfd_short_perp", 7, "")}
	if _, exited, err := StepPaperCfdCarry(ctx, s, low, time.Now(), d410Opts(), nil); err != nil || exited != 0 {
		t.Fatalf("low round: err=%v exited=%d", err, exited)
	}
	active, _ = s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 || active[0].LowStreak != 1 {
		t.Fatalf("锚B5: net=7 want streak=1 still active, got %+v", active)
	}
	// 回稳 → streak 归零
	if _, exited, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil || exited != 0 {
		t.Fatalf("recover round: err=%v exited=%d", err, exited)
	}
	active, _ = s.ListActivePaperSpotCarry(ctx)
	if len(active) != 1 || active[0].LowStreak != 0 {
		t.Fatalf("锚B5: recover want streak=0, got %+v", active)
	}
}
