// D-272 S8 测试：到期提醒 checkMaturity（fake store 注入，纯逻辑测试）。
// 关键用例：checkMaturity 与 D-270 门控完全解耦——Enabled=false 时到期提醒仍发。
package notify

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCheckMaturity_FiresAlert 到期 open 记录 → warn 级告警 + UpdateMaturityReminded。
func TestCheckMaturity_FiresAlert(t *testing.T) {
	store := &fakeReminderStore{
		cfg: defaultCfg(),
		maturedTrades: []MaturedRepoTrade{
			{ID: 1, InstrumentCode: "204001", PrincipalCny: 400000},
		},
	}
	svc := newReminderSvc(t, store, thursdayAt10())
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.maturityReminded {
		t.Error("UpdateMaturityReminded not called")
	}
	// 找到期提醒告警（warn 级，含"逆回购到期"）
	found := false
	for _, a := range store.alerts {
		if strings.HasPrefix(a, "warn:") && strings.Contains(a, "逆回购到期") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("maturity alert not found in %v", store.alerts)
	}
}

// TestCheckMaturity_DisabledStillFires Enabled=false 时到期提醒仍发（解耦验证）。
func TestCheckMaturity_DisabledStillFires(t *testing.T) {
	store := &fakeReminderStore{
		cfg: &RepoConfigRow{IdleCny: 50000, MinReturnCny: 5, ReminderHour: 10, Enabled: false},
		maturedTrades: []MaturedRepoTrade{
			{ID: 1, InstrumentCode: "204001", PrincipalCny: 400000},
		},
	}
	svc := newReminderSvc(t, store, thursdayAt10())
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.maturityReminded {
		t.Error("UpdateMaturityReminded not called when Enabled=false — maturity must be decoupled from D-270 gates")
	}
	found := false
	for _, a := range store.alerts {
		if strings.Contains(a, "逆回购到期") {
			found = true
			break
		}
	}
	if !found {
		t.Error("maturity alert not sent when Enabled=false — must be decoupled")
	}
}

// TestCheckMaturity_BeforeHourStillFires 未到提醒时段时到期提醒仍发（解耦验证）。
func TestCheckMaturity_BeforeHourStillFires(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	before10 := time.Date(2026, 9, 10, 9, 0, 0, 0, loc) // 周四 9:00 < 10
	store := &fakeReminderStore{
		cfg: defaultCfg(),
		maturedTrades: []MaturedRepoTrade{
			{ID: 1, InstrumentCode: "204001", PrincipalCny: 400000},
		},
	}
	svc := newReminderSvc(t, store, before10)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.maturityReminded {
		t.Error("UpdateMaturityReminded not called before reminder hour — maturity must be decoupled")
	}
}

// TestCheckMaturity_NonTargetDayStillFires 非周四/周五/周一到期提醒仍发（解耦验证）。
func TestCheckMaturity_NonTargetDayStillFires(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	tuesday := time.Date(2026, 9, 8, 10, 0, 0, 0, loc) // 周二
	store := &fakeReminderStore{
		cfg: defaultCfg(),
		maturedTrades: []MaturedRepoTrade{
			{ID: 1, InstrumentCode: "204001", PrincipalCny: 400000},
		},
	}
	svc := newReminderSvc(t, store, tuesday)
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !store.maturityReminded {
		t.Error("UpdateMaturityReminded not called on non-target day — maturity must be decoupled")
	}
}

// TestCheckMaturity_NoMaturedTrades 无到期记录 → 无告警。
func TestCheckMaturity_NoMaturedTrades(t *testing.T) {
	store := &fakeReminderStore{
		cfg:           defaultCfg(),
		maturedTrades: nil,
	}
	svc := newReminderSvc(t, store, thursdayAt10())
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if store.maturityReminded {
		t.Error("UpdateMaturityReminded called with no matured trades, want no action")
	}
	for _, a := range store.alerts {
		if strings.Contains(a, "逆回购到期") {
			t.Errorf("maturity alert sent with no matured trades: %s", a)
		}
	}
}

// TestCheckMaturity_InstrumentName 告警含品种名（GC001 而非 204001）。
func TestCheckMaturity_InstrumentName(t *testing.T) {
	store := &fakeReminderStore{
		cfg: defaultCfg(),
		maturedTrades: []MaturedRepoTrade{
			{ID: 1, InstrumentCode: "204001", PrincipalCny: 400000},
		},
	}
	svc := newReminderSvc(t, store, thursdayAt10())
	if err := svc.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	found := false
	for _, a := range store.alerts {
		if strings.Contains(a, "GC001") && strings.Contains(a, "逆回购到期") {
			found = true
			break
		}
	}
	if !found {
		t.Error("alert should contain instrument name GC001")
	}
}
