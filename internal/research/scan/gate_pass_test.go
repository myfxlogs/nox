// d051_cex_gate_test.go —— D-051②锚：CEX 纸面入口质量闸（真库，仿 d555 形态）——
// 双腿 24h 均深度≥$5k ∧ perp 腿 7d funding 均值≥10% 才开；无样本/低值 fail-closed
// 计入 skips；gate 所零深度覆盖天然排除。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d051Store 真库+本票夹具域清空（D51% 前缀隔离——d410 清理不覆盖本前缀）。
func d051Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := d555Store(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM paper_spot_carry WHERE symbol LIKE 'D51%'`,
		`DELETE FROM fin_cex_depth WHERE symbol LIKE 'D51%'`,
		`DELETE FROM facts WHERE symbol LIKE 'D51%'`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("clean d051 domain: %v", err)
		}
	}
	return s
}

func d051Opts(skips *PaperSkipCounter) PaperCexOpts {
	return PaperCexOpts{ReopenCooldown: 24 * time.Hour, MinDepthUSD: 5000, MinFundingPct: 10, Skips: skips}
}

// d051SeedDepth venue 侧 24h 内两笔深度样本（venue 原生符号形：binance D51xUSDT /
// gate D51x_USDT / okx D51x-USDT-SWAP——canonical D51xUSDT 剥尾得基名 D51x）。
func d051SeedDepth(t *testing.T, s *pgstore.Store, venue, base string, mid float64) {
	t.Helper()
	sym := map[string]string{"binance": base + "USDT", "gate": base + "_USDT", "okx": base + "-USDT-SWAP"}[venue]
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := s.Pool().Exec(ctx, `INSERT INTO fin_cex_depth (venue, symbol, ts, bid1_usd, ask1_usd, bid1_qty, ask1_qty, mid_depth_usd)
			VALUES ($1,$2,$3,100,100.1,10,10,$4)`, venue, sym, time.Now().Add(-time.Duration(i)*time.Hour), mid); err != nil {
			t.Fatalf("seed depth %s %s: %v", venue, sym, err)
		}
	}
}

// d051SeedFunding perp 腿 7d 窗内 funding_history（均值=avg）。
func d051SeedFunding(t *testing.T, s *pgstore.Store, venue, canonical string, avg float64) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
		VALUES ('funding_history',$1,$2,$3,'pct_annual',$4,'d051test'),('funding_history',$1,$2,$3,'pct_annual',$5,'d051test')`,
		venue, canonical, avg, time.Now().Add(-24*time.Hour), time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatalf("seed funding %s %s: %v", venue, canonical, err)
	}
}

func d051ActiveN(t *testing.T, s *pgstore.Store, canonical string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM paper_spot_carry WHERE symbol=$1 AND structure='long_spot_short_perp' AND status='active'`,
		canonical).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

// TestD051_GatePass 深度达标+funding 达标 → 开（发现段全量，D-050 无槽）。
func TestD051_GatePass(t *testing.T) {
	s := d051Store(t)
	skips := &PaperSkipCounter{}
	d051SeedDepth(t, s, "okx", "D51A", 8000)
	d051SeedFunding(t, s, "okx", "D51AUSDT", 15.0)
	opened, _, err := StepPaperCexCarry(context.Background(), s,
		[]CexPaperCand{d555Cand("D51AUSDT", "okx:spot", "okx:perp", 12.0)}, time.Now(), d051Opts(skips), nil, nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || d051ActiveN(t, s, "D51AUSDT") != 1 {
		t.Fatalf("双闸达标应开 1 行，opened=%v active=%d skips=%v", opened, d051ActiveN(t, s, "D51AUSDT"), skips.Snapshot())
	}
}

// TestD051_GateReject 闸全拒面：perp 腿无样本 / 深度 $4999 / funding 9.9% /
// gate 零覆盖 / 读面 fail-closed——逐因计数断言。
func TestD051_GateReject(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("perp腿深度无样本", func(t *testing.T) {
		s := d051Store(t)
		skips := &PaperSkipCounter{}
		d051SeedDepth(t, s, "okx", "D51B", 8000) // spot 腿 okx 有
		d051SeedFunding(t, s, "binance", "D51BUSDT", 15.0)
		opened, _, err := StepPaperCexCarry(ctx, s,
			[]CexPaperCand{d555Cand("D51BUSDT", "okx:spot", "binance:perp", 12.0)}, now, d051Opts(skips), nil, nil)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if len(opened) != 0 || skips.Snapshot()["depth_missing"] != 1 {
			t.Fatalf("perp 腿无样本应拒且计 depth_missing，opened=%v skips=%v", opened, skips.Snapshot())
		}
	})

	t.Run("深度4999不开", func(t *testing.T) {
		s := d051Store(t)
		skips := &PaperSkipCounter{}
		d051SeedDepth(t, s, "okx", "D51C", 4999) // 均 4999 < 5000
		d051SeedFunding(t, s, "okx", "D51CUSDT", 15.0)
		opened, _, err := StepPaperCexCarry(ctx, s,
			[]CexPaperCand{d555Cand("D51CUSDT", "okx:spot", "okx:perp", 12.0)}, now, d051Opts(skips), nil, nil)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if len(opened) != 0 || skips.Snapshot()["depth_low"] != 1 {
			t.Fatalf("深度 4999 应拒且计 depth_low，opened=%v skips=%v", opened, skips.Snapshot())
		}
	})

	t.Run("funding9.9不开", func(t *testing.T) {
		s := d051Store(t)
		skips := &PaperSkipCounter{}
		d051SeedDepth(t, s, "okx", "D51D", 8000)
		d051SeedFunding(t, s, "okx", "D51DUSDT", 9.9)
		opened, _, err := StepPaperCexCarry(ctx, s,
			[]CexPaperCand{d555Cand("D51DUSDT", "okx:spot", "okx:perp", 30.0)}, now, d051Opts(skips), nil, nil)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if len(opened) != 0 || skips.Snapshot()["funding_low"] != 1 {
			t.Fatalf("funding 9.9%% 应拒且计 funding_low，opened=%v skips=%v", opened, skips.Snapshot())
		}
	})

	t.Run("funding无样本", func(t *testing.T) {
		s := d051Store(t)
		skips := &PaperSkipCounter{}
		d051SeedDepth(t, s, "okx", "D51E", 8000) // 无 funding 行
		opened, _, err := StepPaperCexCarry(ctx, s,
			[]CexPaperCand{d555Cand("D51EUSDT", "okx:spot", "okx:perp", 12.0)}, now, d051Opts(skips), nil, nil)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if len(opened) != 0 || skips.Snapshot()["funding_low"] != 1 {
			t.Fatalf("funding 无样本应拒且计 funding_low，opened=%v skips=%v", opened, skips.Snapshot())
		}
	})

	t.Run("gate所零深度天然排除", func(t *testing.T) {
		s := d051Store(t)
		skips := &PaperSkipCounter{}
		d051SeedFunding(t, s, "gate", "D51FUSDT", 20.0) // gate 无 fin_cex_depth 覆盖
		opened, _, err := StepPaperCexCarry(ctx, s,
			[]CexPaperCand{d555Cand("D51FUSDT", "gate:spot", "gate:perp", 30.0)}, now, d051Opts(skips), nil, nil)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if len(opened) != 0 || skips.Snapshot()["depth_missing"] != 1 {
			t.Fatalf("gate 零深度应拒且计 depth_missing，opened=%v skips=%v", opened, skips.Snapshot())
		}
	})
}

// TestD051_GateOffCompat 阈零值=闸不激活（既有测试域回归——d555Opts 不带阈照常开）。
func TestD051_GateOffCompat(t *testing.T) {
	s := d051Store(t)
	opened, _, err := StepPaperCexCarry(context.Background(), s,
		[]CexPaperCand{d555Cand("D51GUSDT", "okx:spot", "okx:perp", 12.0)}, time.Now(), d555Opts(), nil, nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 {
		t.Fatalf("阈零值闸不激活应照旧开，opened=%v", opened)
	}
}
