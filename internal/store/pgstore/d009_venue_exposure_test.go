package pgstore

// D-009 S4：venue 系统实占行集 + 建库守卫 venue 复查（执行面接闸）。
// 锚点：①系统仓腿行可见、外单域不入（表内即天然排除）；②venueSum 回调入参=bare；
// ③实占+proposed>cap 拒、≤cap 放行、cap 行缺席放行、cap=0 冻结。

import (
	"context"
	"strings"
	"testing"
)

func seedVenuePosition(t *testing.T, s *Store, ctx context.Context) int64 {
	t.Helper()
	// 底仓：venue 3001157:mt5 一腿 FILLED（系统仓）；notional=4500。
	notional := 4500.0
	pos := &Position{OpportunityID: "XASSET_CARRY:WTIUSD:3001157:mt5:gate:perp:d009", Type: "XASSET_CARRY",
		Status: "OPEN", NotionalUSD: &notional, Environment: "test"}
	id, err := s.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("底仓建库: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO position_legs
		(position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status)
		VALUES ($1,0,'OPEN',1,'3001157','mt5','USOILm','BUY',1,60,'FILLED'),
		       ($1,1,'OPEN',1,'gate','perp','WTI_USDT','SELL',10,60,'FILLED')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestD009_OpenVenueSystemLegs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	seedVenuePosition(t, s, ctx)
	rows, err := s.OpenVenueSystemLegs(ctx, "3001157")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].VenueKey != "3001157:mt5" || rows[0].Qty != 1 {
		t.Fatalf("3001157 应只见本 venue 一腿, got %+v", rows)
	}
	rows2, err := s.OpenVenueSystemLegs(ctx, "gate")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 1 || rows2[0].VenueKey != "gate:perp" {
		t.Fatalf("gate 应只见 gate:perp 腿, got %+v", rows2)
	}
	// 无仓 venue → 空集不报错。
	rows3, err := s.OpenVenueSystemLegs(ctx, "okx")
	if err != nil || len(rows3) != 0 {
		t.Fatalf("空 venue 应空集, got %v %v", rows3, err)
	}
}

func TestD009_CapGuardVenueRecheck(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, fin_venue_exposure_caps RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO fin_venue_exposure_caps (venue_key, cap_usd, enabled)
		VALUES ('3001157:mt5',5000,true),('gate:perp',0,true)`); err != nil {
		t.Fatal(err)
	}
	var gotBare string
	sumFn := func(_ context.Context, bare string) (float64, error) {
		gotBare = bare
		return 4500, nil
	}
	mk := func(opp string) *Position {
		return &Position{OpportunityID: opp, Type: "XASSET_CARRY", Status: "OPENING", Environment: "test"}
	}
	// 实占 4500 + proposed 600 > cap 5000 → 拒（执行面锁内复查锚）。
	if _, err := s.CreatePositionWithCapGuard(ctx, mk("opp-d009-v1"),
		[]string{"WTIUSD"}, []string{"3001157:mt5"}, 600, sumFn); err == nil || !strings.Contains(err.Error(), "venue 敞口超限") {
		t.Fatalf("4500+600>5000 应拒, got %v", err)
	}
	if gotBare != "3001157" {
		t.Fatalf("venueSum 回调入参应=bare '3001157', got %q", gotBare)
	}
	// proposed 400 → 4900≤5000 放行；cap=0 的 gate:perp 不在本仓腿集不判。
	if _, err := s.CreatePositionWithCapGuard(ctx, mk("opp-d009-v2"),
		[]string{"WTIUSD"}, []string{"3001157:mt5"}, 400, sumFn); err != nil {
		t.Fatalf("4500+400≤5000 应放行, got %v", err)
	}
	// cap=0 腿含入 → 冻结拒。
	if _, err := s.CreatePositionWithCapGuard(ctx, mk("opp-d009-v3"),
		[]string{"WTIUSD"}, []string{"gate:perp"}, 1, sumFn); err == nil || !strings.Contains(err.Error(), "venue 敞口冻结") {
		t.Fatalf("gate:perp cap=0 应冻结, got %v", err)
	}
	// 无闸行 venue → 放行（与展示闸同判据：行缺席不拦）。
	if _, err := s.CreatePositionWithCapGuard(ctx, mk("opp-d009-v4"),
		[]string{"WTIUSD"}, []string{"okx:perp"}, 50, sumFn); err != nil {
		t.Fatalf("无闸行 venue 应放行, got %v", err)
	}
}
