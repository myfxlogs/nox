// d144_book_gate_test.go —— D-144 测试锚：簿结构性不可覆盖→不开仓记因（③）、
// active 仓无簿对→warn 自盯（④）、覆盖 fixture 助手（既有 cfd 开仓族共用）。
package scan

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// d144BookCover 让 (canonical,mt,cex) 组合簿结构性可覆盖——按 CfdBookSpecs 实解析
// seed：MT 侧每候选 exSym 一行 mt5 tick（72h 窗内）+ CEX 侧每候选合约原生名一行
// TRADING universe 行。既有 cfd 开仓测试族在本闸落地后统一改走此助手。
func d144BookCover(t *testing.T, s *pgstore.Store, canonical, mtVenue, cexVenue string) {
	t.Helper()
	testutil.EnsureTicksPartition(t, s)
	ctx := context.Background()
	equiv, err := s.ListAssetEquiv(ctx)
	if err != nil {
		t.Fatalf("list asset equiv: %v", err)
	}
	for _, sp := range newAssetClassView(equiv).cfdBookSpecs(canonical, cexVenue, mtVenue) {
		// MT tick 符号种子=闸候选全集（canonical/spec.ExSym/账户映射 canonical）——
		// fixture 若预播 broker_symbol_map（d374 SOLUSD→SOL 形）闸会查映射 canonical。
		for _, sym := range cfdMtTickCandidates(ctx, s, newAssetClassView(equiv), canonical, mtVenue) {
			if _, err := s.Pool().Exec(ctx,
				`INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask) VALUES (now(), $1, 'mt5', $2, 1, 1)`,
				mtVenue, sym); err != nil {
				t.Fatalf("seed mt tick %s|%s: %v", mtVenue, sym, err)
			}
		}
		// 已有同 canonical 归一键的 uni 行则不再插第二行——两行同键竞争
		// cexRawByCanon（map 序非确定）会把 accrue 的 facts 键弄成 flaky。
		canonKey := NormalizeJoinKey(sp.CexCanon)
		var dup int
		if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM fin_cex_universe WHERE venue=$1 AND status='TRADING'`+""+
			` AND ($2 = ANY (ARRAY[symbol, hedge_canonical]) OR symbol = $3)`,
			cexVenue, canonKey, sp.CexCanon).Scan(&dup); err != nil {
			t.Fatalf("dup check %s|%s: %v", cexVenue, sp.CexCanon, err)
		}
		if nat := CexNativeSym(cexVenue, sp.CexCanon); nat != "" && dup == 0 {
			if _, err := s.Pool().Exec(ctx, `
				INSERT INTO fin_cex_universe (venue, symbol, base, status, first_seen, tier)
				VALUES ($1, $2, $2, 'TRADING', now(), 'T1')
				ON CONFLICT (venue, symbol) DO UPDATE SET status='TRADING'`,
				cexVenue, nat); err != nil {
				t.Fatalf("seed uni %s|%s: %v", cexVenue, nat, err)
			}
		}
	}
}

// TestD144_UncoverableNoOpen 锚③：cfd 干净行 net=12 达标但 MT 侧零 tick（72h 保留窗
// 无该品种采样）→不开纸面仓 + uncoverable 计数 + warn 告警记因（测不了的仓不验证）。
func TestD144_UncoverableNoOpen(t *testing.T) {
	s := d350Store(t)
	s.SetAlertDedupWindow(time.Nanosecond) // D-592 同键抑制会吞复读断言（共享库跨 run 持久）
	ctx := context.Background()
	rows := []UniverseRow{d374Row("D144SX", "277842155", "binance", "long_cfd_short_perp", 12.0, "")}
	// cex 合约行先给（隔离变量：只缺 MT tick 腿）。
	testutil.EnsureTicksPartition(t, s)
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_cex_universe (venue, symbol, base, status, first_seen, tier)
		VALUES ('binance', 'D144SXUSDT', 'D144SXUSDT', 'TRADING', now(), 'T1')`); err != nil {
		t.Fatalf("seed uni: %v", err)
	}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("簿不可覆盖组合不得开仓（mt 无 tick 品种）, got opened=%v", opened)
	}
	active, err := s.ListActivePaperSpotCarry(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active 应为零, got %d err=%v", len(active), err)
	}
	var msg string
	if err := s.Pool().QueryRow(ctx, `SELECT message FROM alerts WHERE level='warn'
		AND message LIKE '%簿结构性不可覆盖%' ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		t.Fatalf("拒开原因应记 warn 告警: %v", err)
	}
	if !strings.Contains(msg, "mt 无对应 tick 品种") {
		t.Fatalf("告警应含 mt 无 tick 原因, got %q", msg)
	}
}

// TestD144_UncoverableCexNoSource 锚③补：cex venue 不在报价源集→不开。
func TestD144_UncoverableCexNoSource(t *testing.T) {
	s := d350Store(t)
	s.SetAlertDedupWindow(time.Nanosecond)
	ctx := context.Background()
	rows := []UniverseRow{d374Row("D144SX", "277842155", "kraken", "long_cfd_short_perp", 12.0, "")}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("cex 不在报价源不得开仓, got %v", opened)
	}
	var msg string
	if err := s.Pool().QueryRow(ctx, `SELECT message FROM alerts WHERE level='warn'
		AND message LIKE '%cex 不在报价源%' ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		t.Fatalf("拒开原因应记 warn 告警: %v", err)
	}
}

// TestD144_CoverableStillOpens 对照：簿可覆盖组合（tick+合约齐）照常开仓——精确闸非误杀。
func TestD144_CoverableStillOpens(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	rows := []UniverseRow{d374Row("D144C", "277842155", "binance", "long_cfd_short_perp", 12.0, "")}
	d144BookCover(t, s, "D144C", "277842155", "binance")
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || opened[0] != "D144C" {
		t.Fatalf("簿可覆盖组合应开仓, got opened=%v", opened)
	}
}

// TestD144_ActiveBookAbsentWarn 锚④：active cfd 仓三元组簿零行→warn 自盯告警；
// 簿有行→不告警（反向对拍）。
func TestD144_ActiveBookAbsentWarn(t *testing.T) {
	s := d350Store(t)
	s.SetAlertDedupWindow(time.Nanosecond)
	ctx := context.Background()
	// 独立 canonical + 自对清理（簿行/paper 行跨 run 持久——复用 SOL 会污染 d374 NULL 锚）。
	if _, err := s.Pool().Exec(context.Background(), `DELETE FROM fin_xasset_monitor WHERE pair LIKE 'D144A%'`); err != nil {
		t.Fatalf("clean monitor: %v", err)
	}
	rows := []UniverseRow{d374Row("D144A", "277842155", "binance", "long_cfd_short_perp", 12.0, "")}
	d144BookCover(t, s, "D144A", "277842155", "binance")
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("open: %v opened=%v", err, opened)
	}
	// 簿零行（fin_xasset_monitor 空）→ 次轮应告警。
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil {
		t.Fatalf("step2: %v", err)
	}
	var msg string
	if err := s.Pool().QueryRow(ctx, `SELECT message FROM alerts WHERE level='warn'
		AND message LIKE '%active 仓 monitor 簿缺席%' ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		t.Fatalf("active 仓簿缺席应告警: %v", err)
	}
	if !strings.Contains(msg, "277842155×binance|D144A") {
		t.Fatalf("告警应含仓三元组, got %q", msg)
	}
	// 簿有行→不告警：插三元组簿行再跑一轮。
	if _, err := s.Pool().Exec(ctx, `INSERT INTO fin_xasset_monitor
		(pair, binance_bid, binance_ask, exness_bid, exness_ask, cex_venue, mt_venue,
		 entry_spread_bps, exit_spread_bps, entry_net_bps,
		 funding_rate_8h, funding_annual_pct, tracking_error_bps, equiv_annual_pct, src)
		VALUES ('D144AUSDT-D144A',1,1,1,1,'binance:perp','277842155:mt5',1,1,1,0,0,0,0,'live')`); err != nil {
		t.Fatalf("seed monitor row: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE '%active 仓 monitor 簿缺席%'`); err != nil {
		t.Fatalf("clear alerts: %v", err)
	}
	if _, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil {
		t.Fatalf("step3: %v", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE '%active 仓 monitor 簿缺席%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("簿有行不得告警, got n=%d err=%v", n, err)
	}
}
