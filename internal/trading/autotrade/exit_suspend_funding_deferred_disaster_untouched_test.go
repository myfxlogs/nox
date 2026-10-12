// d053_blackout_engine_test.go —— D-053 财历禁区平仓侧锚（gate 侧件迁 internal/risk/gate，D-212 拆搬）。
package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/testutil"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

func d053Store(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(s.Close)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM macro_event; DELETE FROM autotrade_logs WHERE reason LIKE 'macro blackout%'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	t.Cleanup(func() { // 种子事件清场——包内后续测试共享库不消费残留事件
		_, _ = s.Pool().Exec(context.Background(), `DELETE FROM macro_event`)
	})
	return s
}

// d053SeedHigh 种一条 high 财历事件（t0 相对 now 偏移；source=mql5 语义同生产）。
func d053SeedHigh(t *testing.T, s *pgstore.Store, currency, name string, at time.Time) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), `INSERT INTO macro_event
		(source, event_time_utc, currency, name, importance, forecast, previous, actual)
		VALUES ('mql5', $1, $2, $3, 'high', '0.2', '0.1', '')`, at, currency, name); err != nil {
		t.Fatalf("seed macro event: %v", err)
	}
}

// d053Gate NewGate（禁区窗 ±15min，基集缺省 USD）。
// (b) disaster 条件 → shouldClose=true（保命链不被闸推迟）。
func TestD053_ExitSuspendFundingDeferredDisasterUntouched(t *testing.T) {
	s := d053Store(t)
	ctx := context.Background()
	e := &Engine{store: s, cfg: gate.Config{MacroBlackoutMin: 15, MacroBlackoutCurrencies: "USD"}}
	now := time.Now()
	d053SeedHigh(t, s, "USD", "非农就业", now.Add(10*time.Minute))

	funding := &pgstore.Position{ID: 1, Type: "FUNDING_HEDGE", DedupKey: "FUNDING_HEDGE:BTCUSDT:a:b"}
	hit, why := e.macroBlackoutSuspend(ctx, funding)
	if !hit || why == "" {
		t.Fatalf("禁区内 funding 仓应推迟: hit=%v why=%q", hit, why)
	}
	// XASSET_CARRY 不走 suspend（禁区判定下沉 xassetExitCheck——suspend 恒 false）。
	xasset := &pgstore.Position{ID: 2, Type: "XASSET_CARRY", DedupKey: "XASSET_CARRY:EURUSD:a:b"}
	if hit, _ = e.macroBlackoutSuspend(ctx, xasset); hit {
		t.Fatal("XASSET_CARRY 禁区判定在链内，suspend 应恒 false")
	}

	// (a) 禁区内 + 仅 carry_death 条件（funding −50%/yr 足额负 carry）：
	// hold + fundingBreach 计数冻结 + eval 行 decision=macro_blackout。
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(ctx, `DELETE FROM position_evals`); err != nil {
		t.Fatalf("clean evals: %v", err)
	}
	p := d014EconPos(t, s, -50.0)
	ea := d014EconEngine(s, d404Quotes(now, 3996.0, 4000, 0), true)
	ea.cfg.MacroBlackoutMin, ea.cfg.MacroBlackoutCurrencies = 15, "USD"
	ea.fundingBreach[p.ID] = 1 // 预置 breach 序列——禁区内应冻结不累计不清零
	if close, _ := ea.xassetExitCheck(ctx, p); close {
		t.Fatal("禁区内 carry_death 应冻结（同闭市径 ok=false）")
	}
	if got := ea.fundingBreach[p.ID]; got != 1 {
		t.Fatalf("breach 计数应原样保留=1: got %d", got)
	}
	var dec string
	if err := s.Pool().QueryRow(ctx,
		`SELECT decision FROM position_evals WHERE position_id=$1 ORDER BY evaluated_at DESC LIMIT 1`,
		p.ID).Scan(&dec); err != nil || dec != "macro_blackout" {
		t.Fatalf("eval 应落 decision=macro_blackout: got %q err=%v", dec, err)
	}

	// (b) 禁区内 + CEX 腿结构断裂（D-113：MT 侧偏离只告警不杀；kill=CEX 腿 vs 跨所
	// 共识锚后偏移）→ shouldClose=true——保命退出不被闸推迟。
	d311Truncate(t, s) // 仓/腿/账户/facts 清场（macro_event 不在清单——禁区事件保留）
	p2 := d014EconPos(t, s, 20.8)
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET expected_entry_spread_bps=-12.39 WHERE id=$1`, p2.ID); err != nil {
		t.Fatalf("set anchor: %v", err)
	}
	rows, err := s.ListPositions(ctx, "OPEN", 10, 0) // anchor 复读口径同生产 scan
	if err != nil || len(rows) != 1 {
		t.Fatalf("list pos: rows=%d err=%v", len(rows), err)
	}
	p2 = &rows[0]
	qb := d404Quotes(now, 4160.0, 4000, 0) // basis=+400bps（MT 侧 dev≈412：告警不杀）
	// 共识源贴腿 mid 3999.5（XPTUSDT 同品种跨所）
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "gate", VenueType: "perp", Bid: 3999, Ask: 4000, Time: now, VenueSymbol: "XPT_USDT"})
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "binance", VenueType: "perp", Bid: 3999, Ask: 4000, Time: now, VenueSymbol: "XPTUSDT"})
	eb := d014EconEngine(s, qb, true)
	eb.cfg.MacroBlackoutMin, eb.cfg.MacroBlackoutCurrencies = 15, "USD"
	if close, _ := eb.xassetExitCheck(ctx, p2); close {
		t.Fatal("锚定轮不应平仓")
	}
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: 3929, Ask: 3930, Time: now, VenueSymbol: "XPT-USDT-SWAP"}) // 腿断裂 mid→3929.5
	if close, reason := eb.xassetExitCheck(ctx, p2); !close || !containsSub(reason, "disaster") {
		t.Fatalf("禁区内 disaster 保命链应触发: close=%v reason=%q", close, reason)
	}
}

// T5：禁区拒开落 autotrade_logs（reason 前缀）→ CountMacroBlackoutRejects 计数命中
