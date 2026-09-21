// ws_d370_test.go —— D-370 S5：T1 relogin 缝 / T2 guard 1-strike / T4 断流告警进
// alerts 表 / T5·T6 heartbeat 行标记与剔除键。
package cmc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// d369StoreCMC D-370 测试库 store（Migrate 全量应用含 0118）。
func d369StoreCMC(t *testing.T) *pgstore.Store {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestIsSessionExpired D-370 S1 前置：status 字符串 + auth ack 前缀双保险。
func TestIsSessionExpired(t *testing.T) {
	if !isSessionExpired(errors.New("auth ack status=Session expired")) {
		t.Error("Session expired 状态应命中")
	}
	if !isSessionExpired(errors.New("Session expired")) {
		t.Error("裸 Session expired 应命中")
	}
	if isSessionExpired(errors.New("read: EOF")) || isSessionExpired(nil) {
		t.Error("EOF/nil 不应命中（非 session 故障）")
	}
}

// mockReloginAuth mockAuth + Login（D-370 S1：NewWsClient 类型断言注入 relogin 缝）。
type mockReloginAuth struct {
	mockAuth
	loginCalls int
}

func (m *mockReloginAuth) Login(ctx context.Context) error {
	m.loginCalls++
	return nil
}

// TestD370_T1_ReloginOnSessionExpired T1：Session expired 错误 → relogin 缝被调（非仅
// refreshJWT）；EOF 错误不触发。
func TestD370_T1_ReloginOnSessionExpired(t *testing.T) {
	auth := &mockReloginAuth{}
	ws := NewWsClient(auth, 1, nil, "ws://x", "http://x")
	if ws.relogin == nil {
		t.Fatal("NewWsClient 应注入 relogin 缝（auth 实现 Login）")
	}
	if !ws.maybeRelogin(context.Background(), errors.New("auth ack status=Session expired")) {
		t.Error("Session expired 应触发 relogin")
	}
	if auth.loginCalls != 1 {
		t.Errorf("Login 调用 = %d, want 1", auth.loginCalls)
	}
	if ws.maybeRelogin(context.Background(), errors.New("read: EOF")) {
		t.Error("EOF 不应触发 relogin")
	}
	if auth.loginCalls != 1 {
		t.Errorf("EOF 后 Login 调用 = %d, want 1（不变）", auth.loginCalls)
	}
}

// TestD370_T2T3_GuardTick T2：1 次 stale 即 unhealthy（R4 前 2 次门槛+marketOpen 门控
// =生产 0 触发根因）；T3：账本 unhealthySince 供断流告警窗口判定。
func TestD370_T2T3_GuardTick(t *testing.T) {
	auth := &mockAuth{jwt: "j"}
	ws := NewWsClient(auth, 1, nil, "ws://x", "http://x")
	var unhealthySince, lastAlert time.Time

	ws.lastEvent6Mu.Lock()
	ws.lastEvent6 = time.Now().Add(-11 * time.Minute) // > wsGuardStale
	ws.lastEvent6Mu.Unlock()
	ws.guardTick(context.Background(), &unhealthySince, &lastAlert)
	if ws.healthy.Load() {
		t.Fatal("stale 11min 应 unhealthy")
	}
	if unhealthySince.IsZero() {
		t.Error("unhealthySince 应起账")
	}

	ws.guardTick(context.Background(), &unhealthySince, &lastAlert)
	if ws.healthy.Load() {
		t.Fatal("持续 stale 应仍 unhealthy")
	}
}

// TestD370_T4_GuardAlertInsert D-370 S5 T4：unhealthy 持续 >10min → 告警进 alerts 表
// （level=warn，同指纹 30min 冷却内不重复）。
func TestD370_T4_GuardAlertInsert(t *testing.T) {
	s := d369StoreCMC(t)
	ctx := context.Background()
	auth := &mockAuth{jwt: "j"}
	ws := NewWsClient(auth, 1, s, "ws://x", "http://x")
	now := time.Now()
	unhealthySince := now.Add(-11 * time.Minute)
	lastAlert := time.Time{}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	ws.guardTick(ctx, &unhealthySince, &lastAlert)
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE message LIKE '[cmc-ws] 断流%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("断流告警行 = %d (err=%v), want 1", n, err)
	}
	var level, msg string
	if err := s.Pool().QueryRow(ctx,
		`SELECT level, message FROM alerts WHERE message LIKE '[cmc-ws] 断流%'`).Scan(&level, &msg); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if level != "warn" || !strings.Contains(msg, "[cmc-ws] 断流") {
		t.Errorf("告警 level/message = %s/%q", level, msg)
	}
}

// TestD370_T5T6_HeartbeatSrcMarker D-370 S5 T5/T6：heartbeat 行 src='cmc_ws_hb'
// （可 WHERE src 剔除）；正常价行 src='cmc_ws'。
func TestD370_T5T6_HeartbeatSrcMarker(t *testing.T) {
	s := d369StoreCMC(t)
	ctx := context.Background()
	auth := &mockAuth{jwt: "j"}
	ws := NewWsClient(auth, 1, s, "ws://x", "http://x")
	ws.priceMu.Lock()
	ws.evalAsk = 0.3245
	ws.priceMu.Unlock()

	ws.persistPriceHeartbeat(context.Background())
	ws.persistPrice(context.Background(), 0.3250, time.Now(), false)

	var srcs []string
	if err := s.Pool().QueryRow(ctx, `
		SELECT array_agg(src ORDER BY fetched_at DESC) FROM
		  (SELECT src, fetched_at FROM fin_cmc_prices ORDER BY fetched_at DESC LIMIT 2) t`).
		Scan(&srcs); err != nil {
		t.Fatalf("scan srcs: %v", err)
	}
	if len(srcs) != 2 || srcs[0] != "cmc_ws" || srcs[1] != "cmc_ws_hb" {
		t.Errorf("最近两行 src = %v, want [cmc_ws cmc_ws_hb]（新→旧）", srcs)
	}
}
