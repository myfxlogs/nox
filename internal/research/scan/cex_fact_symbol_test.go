// d550_accrue_test.go —— D-550 验收锚：universe raw symbol→facts canonical 键断链根修。
// 病灶：cexRawByCanon 回 universe raw（gate US30_USDT/okx XAU-USDT-SWAP），而 collector
// 发射 facts 键=canonical 紧凑形（US30USDT/XAUUSDT）→边界事实查询零命中→fail-closed
// →gate/okx 全部 cfd 纸面行 accrued 恒 0（生产实证 49 行全零）。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/fact"
)

// A0：raw→facts 键纯函数锚——三 venue 实形全覆盖。
func TestD550_CexFactSymbol(t *testing.T) {
	for in, want := range map[string]string{
		"US30_USDT":     "US30USDT", // gate
		"XAU-USDT-SWAP": "XAUUSDT",  // okx
		"BTC-USDT-SWAP": "BTCUSDT",
		"BTCUSDT":       "BTCUSDT", // binance 原样
		"SOL_USDT":      "SOLUSDT",
	} {
		if got := cexFactSymbol(in); got != want {
			t.Fatalf("cexFactSymbol(%q)=%q want %q", in, got, want)
		}
	}
}

// A1：gate raw 键（D550_USDT）+facts 在 canonical 键（D550USDT）→ 应计 >0。
// 修复前 raw 键零命中 fail-closed accrued=0；修复后等价 d374 B5 窗界断言。
func TestD550_GateRawAccrue(t *testing.T) {
	d550AccrueCase(t, "gate", "D550_USDT", "D550", "D550USDT")
}

// A2：okx raw 键（D551-USDT-SWAP）同款断链（同函数一并回归）。
func TestD550_OkxRawAccrue(t *testing.T) {
	d550AccrueCase(t, "okx", "D551-USDT-SWAP", "D551", "D551USDT")
}

func d550AccrueCase(t *testing.T, cexVenue, rawSym, canonical, factSym string) {
	t.Helper()
	// 时钟注入——开仓/累积轮挂固定 UTC 时段（06:50 与 22:00 UTC 各跑一次；
	// 22:00 UTC=+0800 本地次日=旧 swapLegBoundaries 本地日历日拼 UTC 午夜的破相点）。
	for _, utc := range []string{"06:50", "22:00"} {
		t.Run("utc_"+utc, func(t *testing.T) {
			h, _ := time.Parse("15:04", utc)
			day := time.Date(2026, 10, 2, h.Hour(), h.Minute(), 0, 0, time.UTC)
			d550AccrueCaseAt(t, cexVenue, rawSym, canonical, factSym, day)
		})
	}
}

func d550AccrueCaseAt(t *testing.T, cexVenue, rawSym, canonical, factSym string, now time.Time) {
	t.Helper()
	s := d410Store(t)
	ctx := context.Background()
	// facts 隔离——前轮真实时钟残留的 next_settle（Ts 更新）会赢 LatestFacts，
	// 注入时钟的 seed 被遮蔽 → 累积窗错位。venue 维度清除本例相关键。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM facts WHERE venue = $1 OR venue = '277842155'`, cexVenue); err != nil {
		t.Fatalf("clean facts: %v", err)
	}
	// universe 行=raw symbol 形态（生产 fin_cex_universe 实形），hedge_canonical=join 键。
	funding := 11.1
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_cex_universe (venue, symbol, base, status, first_seen, tier,
			last_funding_ann, last_vol24h, hedge_canonical)
		VALUES ($1,$2,$3,'TRADING',$4,'T3',$5,1e8,$6)`,
		cexVenue, rawSym, canonical, now.Add(-72*time.Hour), funding, canonical); err != nil {
		t.Fatalf("seed uni %s|%s: %v", cexVenue, rawSym, err)
	}
	// facts 只在 canonical 键下（与 collector 发射键同形）——raw 键下零行是病灶核心。
	seedSettleFacts(t, s, cexVenue, factSym, now)
	// MT 腿：broker_symbol_map canonical→broker 裸符号 + swap_long 事实面。
	if _, err := s.Pool().Exec(ctx, `INSERT INTO broker_accounts (venue, venue_type, host, port, password)
		VALUES ('277842155','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING;
		INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary)
		VALUES ('277842155','D550USD','D550',true), ('277842155','D551USD','D551',true)
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed symbol map: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "277842155", Symbol: canonical + "USD", Value: 0, Ts: now},
	}); err != nil {
		t.Fatalf("seed swap facts: %v", err)
	}
	rows := []UniverseRow{d374Row(canonical, "277842155", cexVenue, "long_cfd_short_perp", 10.95, "")}
	d144BookCover(t, s, canonical, "277842155", cexVenue) // D-144 ③
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, now, d410Opts(), nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 回拨 last_accrue_ts 24h → 窗内恰 3 个 8h 界（同 d374 B5 推导）。
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET last_accrue_ts = $1`, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, now, d410Opts(), nil); err != nil {
		t.Fatalf("accrue round: %v", err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	got, n := 0.0, 0
	for _, a := range active {
		if a.Symbol == canonical && a.VenueB == cexVenue {
			got, n = a.AccruedUSD, n+1
		}
	}
	if n != 1 {
		t.Fatalf("%s: want 1 active %s row, got %d", cexVenue, canonical, n)
	}
	// 断链未修则 accrued=0（边界事实零命中 fail-closed）；修复后 ≥1 个 8h 界入共同水位窗
	// → accrued>0（精确界数随 now 与 MT 日界水位位置浮动，锚定非零即证键通）。
	if got <= 0 {
		t.Fatalf("%s: accrued=%v want >0（raw→facts 键断链未修则=0）", cexVenue, got)
	}
}
