// d160_stale_test.go —— D-160 锚：分类纯函数表驱动（symbolAge×锚→still/dead）+
// integration 造 stale symbol×fresh venue → warn 非 critical、venue 全死 → critical 照旧。
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestD160_ClassifyTable 分类表驱动（venueAnchorFn seam 注入锚值隔离 DB）：
// symbolAge×venueAnchor→still/dead。
func TestD160_ClassifyTable(t *testing.T) {
	anchor := true
	orig := venueAnchorFn
	venueAnchorFn = func(context.Context, *pgxpool.Pool, *staleDataErr) bool { return anchor }
	t.Cleanup(func() { venueAnchorFn = orig })
	classify := func(age time.Duration) string {
		return classifyStale(context.Background(), nil,
			&staleDataErr{kind: "ticks", venue: "okx", symbol: "XAUUSDT", age: age})
	}
	cases := []struct {
		name   string
		age    time.Duration
		anchor bool
		want   string
	}{
		{"窗内 stale+锚鲜→still（簿面静止）", 150 * time.Second, true, "still"},
		{"窗内 stale+锚死→dead（真断流）", 150 * time.Second, false, "dead"},
		{"恰帽 30min+锚鲜→still（≤帽未升级）", stillHardCap, true, "still"},
		{"超帽+锚鲜→dead（订阅疑似被摘）", stillHardCap + time.Second, true, "dead"},
		{"无行 age=0+锚鲜→dead（比超帽更劣）", 0, true, "dead"},
		{"无行+锚死→dead", 0, false, "dead"},
		{"窗内边界 120s 恰阈+锚鲜→still", goldStaleTicks, true, "still"},
	}
	for _, c := range cases {
		anchor = c.anchor
		if got := classify(c.age); got != c.want {
			t.Errorf("%s: age=%v anchor=%v want %s got %s", c.name, c.age, c.anchor, c.want, got)
		}
	}
}

// d160SeedVenueTick 播指定 venue/age 的 perp ticks 行（活性锚播种/陈旧对象播种）。
func d160SeedVenueTick(t *testing.T, pool *pgxpool.Pool, venue, symbol string, age time.Duration) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask) VALUES (now()-$3::interval, $1, 'perp', $2, 1, 1)`,
		venue, symbol, (age).String())
	if err != nil {
		t.Fatalf("seed %s/%s: %v", venue, symbol, err)
	}
}

// TestD160_VenueAnchor 集成锚：venue×perp 任一鲜行 → 锚成立；全旧/无行 → 锚死。
func TestD160_VenueAnchor(t *testing.T) {
	s := d335r1Store(t)
	pool := s.Pool()
	ctx := context.Background()
	d160SeedVenueTick(t, pool, "d160okx", "OTHERUSDT", time.Second)    // venue 鲜行（他 symbol）
	d160SeedVenueTick(t, pool, "d160okx", "D160TUSDT", 3*time.Minute)  // 本 symbol 陈旧
	d160SeedVenueTick(t, pool, "d160dead", "D160TUSDT", 3*time.Minute) // 异 venue 全旧
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM ticks WHERE venue LIKE 'd160%'`)
	})
	if !venueAnchor(ctx, pool, &staleDataErr{kind: "ticks", venue: "d160okx", symbol: "D160TUSDT", age: 3 * time.Minute}) {
		t.Error("同 venue 任一 perp 鲜行应锚成立")
	}
	if venueAnchor(ctx, pool, &staleDataErr{kind: "ticks", venue: "d160dead", symbol: "D160TUSDT", age: 3 * time.Minute}) {
		t.Error("venue 全旧应锚死")
	}
	if venueAnchor(ctx, pool, &staleDataErr{kind: "ticks", venue: "d160none", symbol: "X", age: time.Minute}) {
		t.Error("venue 无行应锚死")
	}
	// venue 隔离：他 venue 鲜行不借用（KB cross-venue-data-borrowing-pollution）。
	if venueAnchor(ctx, pool, &staleDataErr{kind: "ticks", venue: "d160iso", symbol: "D160TUSDT", age: time.Minute}) {
		t.Error("他 venue 鲜行不应为本 venue 举证")
	}
}

// TestD160_RunPairsStillWarn 集成锚：stale symbol×fresh venue → runPairs 无 error 返回
// （非 critical 链）+ alerts warn 行落库一次；venue 全死 → error 返回走原 critical 链。
func TestD160_RunPairsStillWarn(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	d335r1SeedTick(t, s, "3001157", "XAUUSD", 4354, 4355)
	// okx XAUUSDT ticks 陈旧 3min（簿面静止对象=生产实证同款）+ 同 venue 其他
	// symbol 鲜行（活性锚）。funding 不会走到——ticks stale 先返。
	d160SeedVenueTick(t, s.Pool(), "okx", "XAUUSDT", 3*time.Minute)
	d160SeedVenueTick(t, s.Pool(), "okx", "BTCUSDT", time.Second)
	t.Cleanup(func() {
		s.Pool().Exec(ctx, `DELETE FROM ticks WHERE venue='okx'`)
		s.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE 'xasset-monitor%'`)
	})
	var pairs []monitorPair
	for _, p := range derivedPairs() {
		if p.cexVenue == "okx:perp" && p.sym == "XAU-USDT-SWAP" {
			pairs = append(pairs, p)
		}
	}
	if len(pairs) == 0 {
		t.Fatal("未找到 okx XAU-USDT-SWAP pair")
	}
	if err := runPairs(ctx, s, pairs); err != nil {
		t.Fatalf("簿面静止应降 warn 非 error: %v", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE 'xasset-monitor 簿面静止%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("应落一条 warn 汇总行 n=%d err=%v", n, err)
	}
	// stale 价不落簿（D-591 指标纯洁）。
	if n := d335r1Count(t, s, "XAUUSDT-XAU"); n != 0 {
		t.Errorf("stale 行不应入 fin_xasset_monitor, got %d", n)
	}
}

// TestD160_RunPairsDeadCritical 集成锚：venue 无任何鲜行 → error 返回（reportFatal 链
// 走 critical+飞书照旧——由调用方 defer 接管，本锚钉返回值非降级）。
func TestD160_RunPairsDeadCritical(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	d335r1SeedTick(t, s, "3001157", "XAUUSD", 4354, 4355)
	d160SeedVenueTick(t, s.Pool(), "okx", "XAUUSDT", 3*time.Minute) // venue 仅此陈旧行
	t.Cleanup(func() { s.Pool().Exec(ctx, `DELETE FROM ticks WHERE venue='okx'`) })
	var pairs []monitorPair
	for _, p := range derivedPairs() {
		if p.cexVenue == "okx:perp" && p.sym == "XAU-USDT-SWAP" {
			pairs = append(pairs, p)
		}
	}
	if err := runPairs(ctx, s, pairs); err == nil {
		t.Error("venue 全死应返回 error 走 critical 链, got nil")
	}
}
