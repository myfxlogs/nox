// d053_blackout_test.go —— D-053 ③⑤验收锚：禁区内开仓拒/窗外放行、EURUSD 命中 EUR 事件
// （canonical 两半 ISO 币种推导）、MacroHighEventsIn 错 → 开仓拒（fail-closed）、
// 平仓侧禁区内 funding 常规退出被推迟而 XASSET_CARRY（disaster 链）不受影响。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// d053Store 真库 store + 本票涉及表清空（席位隔离 DSN）。
func d053Store(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
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
func d053Gate(s *pgstore.Store) *Gate {
	return NewGate(s, Config{MacroBlackoutMin: 15}, nil)
}

func d053Intent(canonical string) *TradeIntent {
	return &TradeIntent{Strategy: "SWAPARB", Canonical: canonical, DedupKey: "SWAPARB:" + canonical + ":a:b"}
}

// T1：USD 事件在窗内（now+10min）→ REJECTED，reason 前缀 macro blackout；窗外放行。
func TestD053_BlackoutBlocksOpenInsideWindow(t *testing.T) {
	s := d053Store(t)
	ctx := context.Background()
	g := d053Gate(s)
	now := time.Now()
	d053SeedHigh(t, s, "USD", "非农就业", now.Add(10*time.Minute))

	res, blocked := g.macroBlackoutGate(ctx, d053Intent("BTCUSDT"))
	if !blocked || res.Status != "REJECTED" {
		t.Fatalf("窗内应拒: res=%v blocked=%v", res, blocked)
	}
	if len(res.Reason) < len("macro blackout") || res.Reason[:len("macro blackout")] != "macro blackout" {
		t.Fatalf("reason 应 macro blackout 前缀: %q", res.Reason)
	}

	// 事件拨出窗外（now+20min > 15min 窗）→ 放行。
	if _, err := s.Pool().Exec(ctx, `UPDATE macro_event SET event_time_utc = $1`, now.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	res, blocked = g.macroBlackoutGate(ctx, d053Intent("BTCUSDT"))
	if blocked {
		t.Fatalf("窗外应放行: res=%v", res)
	}
}

// T2：EURUSD 命中 EUR 事件——canonical 6 字母两半 ISO → 币种集并入 EUR（配置基集仅 USD）。
func TestD053_CanonicalSplitsHitEuroEvent(t *testing.T) {
	s := d053Store(t)
	ctx := context.Background()
	g := d053Gate(s)
	d053SeedHigh(t, s, "EUR", "欧央行利率决议", time.Now().Add(5*time.Minute))

	res, blocked := g.macroBlackoutGate(ctx, d053Intent("EURUSD"))
	if !blocked || res.Status != "REJECTED" {
		t.Fatalf("EURUSD 应命中 EUR 事件拒开: res=%v blocked=%v", res, blocked)
	}
	if want := "欧央行利率决议"; !contains(res.Reason, want) {
		t.Fatalf("reason 应含事件名 %q: %q", want, res.Reason)
	}
	// 反向：3+4 字母非 ISO 形（SOLUSDT 7 字母/USDT 非 ISO 半）不推导——基集 USD 不含 SOL。
	res, blocked = g.macroBlackoutGate(ctx, d053Intent("SOLUSDT"))
	if blocked {
		t.Fatalf("SOLUSDT 不应命中 EUR 事件: %v", res)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// T3：MacroHighEventsIn 查询失败 → 开仓拒（fail-closed）。
func TestD053_QueryFailureFailsClosed(t *testing.T) {
	s := d053Store(t)
	ctx := context.Background()
	g := d053Gate(s)
	s.Pool().Close() // 连接池关闭 → 查询失败

	res, blocked := g.macroBlackoutGate(ctx, d053Intent("BTCUSDT"))
	if !blocked || res.Status != "REJECTED" {
		t.Fatalf("查询失败应 fail-closed 拒开: res=%v blocked=%v", res, blocked)
	}
}

// T4：平仓侧——禁区内 FUNDING_HEDGE 推迟（suspend true）而 XASSET_CARRY 不推迟
// （disaster 保命链恒放行，票面「disaster/止损路径不经此闸」）。
func TestD053_ExitSuspendFundingDeferredDisasterUntouched(t *testing.T) {
	s := d053Store(t)
	ctx := context.Background()
	e := &Engine{store: s, cfg: Config{MacroBlackoutMin: 15, MacroBlackoutCurrencies: "USD"}}
	d053SeedHigh(t, s, "USD", "非农就业", time.Now().Add(10*time.Minute))

	funding := &pgstore.Position{ID: 1, Type: "FUNDING_HEDGE", DedupKey: "FUNDING_HEDGE:BTCUSDT:a:b"}
	hit, why := e.macroBlackoutSuspend(ctx, funding)
	if !hit || why == "" {
		t.Fatalf("禁区内 funding 仓应推迟: hit=%v why=%q", hit, why)
	}
	xasset := &pgstore.Position{ID: 2, Type: "XASSET_CARRY", DedupKey: "XASSET_CARRY:EURUSD:a:b"}
	if hit, _ = e.macroBlackoutSuspend(ctx, xasset); hit {
		t.Fatal("XASSET_CARRY（disaster 链内）不应被 suspend 推迟")
	}
}

// T5：禁区拒开落 autotrade_logs（reason 前缀）→ CountMacroBlackoutRejects 计数命中
// （selfwatch ④「禁区拒开次数/24h」读口）。
func TestD053_RejectsCounted(t *testing.T) {
	s := d053Store(t)
	ctx := context.Background()
	g := d053Gate(s)
	d053SeedHigh(t, s, "USD", "非农就业", time.Now().Add(10*time.Minute))
	intent := d053Intent("BTCUSDT")
	g.rejectLog(ctx, *intent, "macro blackout USD 非农就业（BJ 10-04 21:30 ±15min）", false)
	g.rejectLog(ctx, *intent, "other reject", false)

	n, err := s.CountMacroBlackoutRejects(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("禁区拒开应计 1（other 不计）: got %d", n)
	}
}
