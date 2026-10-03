// d029_session_test.go —— D-029 验收锚：
// T1 周表休市段 → mtLegMarketClosed=true（carry_death breach 不累计由 xassetExitCheck 消费同一判定）；
// T2 周表开市 + 报价新鲜度超阈 → true；周表开市 + 报价新鲜 + N 已配 → false；
// T3 N 缺 → fail-closed true；T4 周表缺席 → fail-open false。
package autotrade

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// ctx029 测试背景（包内统一短名）。

// d029SessionPos 建单 MT 腿 OPEN+FILLED 持仓（legs.Venue=3001157:mt5、Symbol=XAUUSDm）。
func d029SessionPos(t *testing.T, s *pgstore.Store) *pgstore.Position {
	t.Helper()
	p := d014EconPos(t, s, -10)
	return p
}

func d029SeedSessions(t *testing.T, s *pgstore.Store, now time.Time, windows ...float64) {
	t.Helper()
	fs := make([]fact.Fact, 0, len(windows))
	for i, w := range windows {
		// D-029：symbol=canon#idx（adapter 同形——LatestFactsByKind 按 (venue,symbol) 折叠）
		// Symbol=canon#idx（adapter 同形——LatestFactsByKind 按 (venue,symbol) 折叠）；腿 canonical=XPTUSD（d014EconPos fixture 同源）
		fs = append(fs, fact.Fact{Kind: fact.KindTradeSession, Venue: "3001157", Symbol: "XPTUSD#" + fmt.Sprint(i), Value: w, Unit: "utc_session", Ts: now, Src: "mt5"})
	}
	if err := s.InsertFacts(ctx029(), fs); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
}

func ctx029() context.Context { return context.Background() }

// d029Ticks 插报价新鲜度（age 参数：0=新鲜 / 30min=超阈）。
func d029Ticks(t *testing.T, s *pgstore.Store, now time.Time, age time.Duration) {
	t.Helper()
	if err := s.EnsureMonthPartition(ctx029(), now.Year(), int(now.Month())); err != nil {
		t.Fatalf("partition: %v", err)
	}
	// D-029：席位库 ticks 表跨 run/跨测共享——先清本腿行防残留遮蔽新鲜度判定。
	if _, err := s.Pool().Exec(ctx029(), `DELETE FROM ticks WHERE venue='3001157' AND symbol='XPTUSD'`); err != nil {
		t.Fatalf("clean ticks: %v", err)
	}
	if err := s.InsertTicks(ctx029(), []pgstore.TickRow{
		// Symbol=腿 broker 裸符号（d014EconPos 同源）
		{Ts: now.Add(-age), Venue: "3001157", VenueType: "mt5", Symbol: "XPTUSD", Bid: 4000, Ask: 4000.5},
	}); err != nil {
		t.Fatalf("insert ticks: %v", err)
	}
}

// TestD029_SessionTableClosed 周表休市段（周六窗、now=周六）→ true； breach 消费同判。
func TestD029_SessionTableClosed(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) // 周六
	p := d029SessionPos(t, s)
	d029SeedSessions(t, s, now,
		float64(6*1e8),             // 周六 00:00-00:00（全日闭退化窗）
		float64(0*1e8+420*1e4+300), // 周日 07:00-05:00 跨午夜（无关本判）
	)
	e := d014EconEngine(s, d404Quotes(now, 4000, 4000, 0), true)
	e.SetSessionNowFn(func() time.Time { return now }) // 同上：时相注入
	if !e.mtLegMarketClosed(ctx029(), p) {
		t.Fatal("周表休市段应判闭")
	}
}

// TestD029_SessionOpenQuoteStaleVsFresh 周表开市：报价超阈→true；新鲜（N=30 配置）→false；N 缺→true。
func TestD029_SessionOpenQuoteStaleVsFresh(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	// now=周五 12:00 UTC（周五窗 08:00-20:00 内）。
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	p := d029SessionPos(t, s)
	// D-029：席位库 facts 表跨 run/跨测共享——前测残留 trade_mode=CloseOnly 会经径 1 短路；
	// 显式种 Full 覆盖（最新 ts 赢 LatestFacts），隔离出纯周表/动态径被测面。
	if err := s.InsertFacts(ctx029(), []fact.Fact{
		// Ts=真实 now（须新于跨测残留的 CloseOnly 行——LatestFacts 取最新，seat 库 facts 表共享）
		{Kind: fact.KindTradeMode, Venue: "3001157", Symbol: "XPTUSD", Value: float64(fact.TradeModeFull), Unit: fact.UnitMode, Ts: time.Now().UTC(), Src: "mt5"},
	}); err != nil {
		t.Fatalf("seed trade_mode full: %v", err)
	}
	d029SeedSessions(t, s, now, float64(5*1e8+480*1e4+1200)) // 周五 08:00-20:00（value=day*1e7+start*1e4+end）
	e := d014EconEngine(s, d404Quotes(now, 4000, 4000, 0), true)
	e.SetSessionNowFn(func() time.Time { return now }) // D-029：判定时刻注入（假周五，真钟周六会串相）

	d029Ticks(t, s, now, 30*time.Minute) // 报价 30min 前=超阈
	e.cfg.MtQuoteStaleMin = 10
	if !e.mtLegMarketClosed(ctx029(), p) {
		t.Fatal("周表开市但报价静默超阈应判闭（动态径）")
	}

	d029Ticks(t, s, now, 2*time.Minute) // 报价 2min 前=新鲜
	if e.mtLegMarketClosed(ctx029(), p) {
		t.Fatal("周表开市且报价新鲜应判开")
	}

	d029Ticks(t, s, now, 30*time.Minute)
	e.cfg.MtQuoteStaleMin = 0 // N 缺 → fail-closed 判闭
	if !e.mtLegMarketClosed(ctx029(), p) {
		t.Fatal("N 缺应 fail-closed 判闭（无动态佐证不判开）")
	}
}

// TestD029_SessionTableAbsentFailOpen 周表缺席 → false（fail-open 既有链；10018 兜底）。
func TestD029_SessionTableAbsentFailOpen(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d029SessionPos(t, s)
	e := d014EconEngine(s, d404Quotes(time.Now(), 4000, 4000, 0), true)
	if e.mtLegMarketClosed(ctx029(), p) {
		t.Fatal("周表缺席应 fail-open 不拦（下单回报 10018 兜底）")
	}
}

// TestD029_EconEntryGateRejectsClosed econEntryGate：MT 腿闭市 → 拒开（true）。
func TestD029_EconEntryGateRejectsClosed(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) // 周六
	d029SeedSessions(t, s, now, float64(6*1e8))
	e := d014EconEngine(s, d404Quotes(now, 4000, 4000, 0), true)
	e.SetSessionNowFn(func() time.Time { return now })
	opp := pgstore.Opportunity{
		ID:        "XASSET:XAUUSD:3001157:mt5:okx:perp:29",
		Type:      "XASSET_CARRY",
		Canonical: "XAUUSD",
		VenueBuy:  "3001157:mt5",
		VenueSell: "okx:perp",
	}
	if !e.econEntryGate(ctx029(), opp, 1000) {
		t.Fatal("MT 腿闭市应拒开（econEntryGate true）")
	}
}
