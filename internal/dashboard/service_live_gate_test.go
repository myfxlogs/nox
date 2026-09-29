// service_live_gate_test.go —— D-159 W6a S2：手动 RPC LIVE gate 检查纯函数单测。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// setupLiveGateServiceStore 构造真库 store + broker_accounts 种子 is_real。
func setupLiveGateServiceStore(t *testing.T, venueLong, venueShort string, longReal, shortReal bool) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE broker_accounts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	insert := func(venue string, isReal bool) {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', 's', 'connected', $2) ON CONFLICT DO NOTHING`,
			venue, isReal); err != nil {
			t.Fatalf("insert broker_account %s: %v", venue, err)
		}
	}
	vL, _, _ := strings.Cut(venueLong, ":")
	vS, _, _ := strings.Cut(venueShort, ":")
	insert(vL, longReal)
	insert(vS, shortReal)
	return s
}

// TestCheckLiveGate_RealVenue_Rejected D-159 W6a S2：is_real=true → 返回拒绝原因。
func TestCheckLiveGate_RealVenue_Rejected(t *testing.T) {
	s := setupLiveGateServiceStore(t, "vA:mt5", "vB:mt5", true, false)
	svc := &Service{store: s}
	msg := svc.checkLiveGate(context.Background(), "vA:mt5", "vB:mt5")
	if msg == "" {
		t.Error("is_real=true venue: want non-empty reject reason, got empty")
	}
	if !strings.Contains(msg, "实盘交易开关关闭") {
		t.Errorf("want reason contains 'live trade disabled', got %q", msg)
	}
}

// TestCheckLiveGate_DemoVenue_Pass D-159 W6a S2：is_real=false（demo）→ 返回空（放行）。
func TestCheckLiveGate_DemoVenue_Pass(t *testing.T) {
	s := setupLiveGateServiceStore(t, "vA:mt5", "vB:mt5", false, false)
	svc := &Service{store: s}
	msg := svc.checkLiveGate(context.Background(), "vA:mt5", "vB:mt5")
	if msg != "" {
		t.Errorf("demo venue: want empty (pass), got %q", msg)
	}
}

// TestCheckLiveGate_VenueNotFound_FailClosed D-159 W6a S2：venue 不存在 → fail-closed 拒绝。
func TestCheckLiveGate_VenueNotFound_FailClosed(t *testing.T) {
	s := setupLiveGateServiceStore(t, "vA:mt5", "vB:mt5", false, false)
	svc := &Service{store: s}
	msg := svc.checkLiveGate(context.Background(), "vX:mt5", "vB:mt5")
	if msg == "" {
		t.Error("venue not found: want non-empty (fail-closed), got empty")
	}
}

// 确保 time 包被引用（testutil 可能在某些 go 版本需要）
var _ = time.Now
