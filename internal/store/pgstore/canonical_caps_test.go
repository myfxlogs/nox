package pgstore

// 验收：fin_canonical_exposure_caps 读写 + 聚簇前瞻判据 +
// CreatePositionWithCapGuard advisory 锁内复查（TOCTOU：第二笔同 canonical 被拦）。

import (
	"context"
	"strings"
	"testing"
)

func TestD006_CanonicalCaps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_canonical_exposure_caps, positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// upsert 幂等 + list 回读。
	if err := s.UpsertCanonicalExposureCap(ctx, &CanonicalExposureCap{Canonical: "XAUUSD", CapUSD: 5000, Enabled: true, Note: "a"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.UpsertCanonicalExposureCap(ctx, &CanonicalExposureCap{Canonical: "XAUUSD", CapUSD: 4000, Enabled: true, Note: "b"}); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	caps, err := s.ListCanonicalExposureCaps(ctx)
	if err != nil || len(caps) != 1 || caps["XAUUSD"].CapUSD != 4000 || caps["XAUUSD"].Note != "b" {
		t.Fatalf("list: %v %+v", err, caps)
	}
	// 负 cap CHECK 拒。
	if err := s.UpsertCanonicalExposureCap(ctx, &CanonicalExposureCap{Canonical: "BTCUSDT", CapUSD: -1, Enabled: true}); err == nil {
		t.Fatal("负 cap 应由 CHECK 拒")
	}
	// 无行 canonical → 默认 6000 判据；行在档 → 表值。
	if r := s.CanonicalCapReject(ctx, "BTCUSDT", 1000); r != "" {
		t.Fatalf("默认 6000 内应放行: %s", r)
	}
	if r := s.CanonicalCapReject(ctx, "XAUUSD", 4001); !strings.Contains(r, "聚簇超限") {
		t.Fatalf("超 cap 应拒: %q", r)
	}
	// cap=0 冻结；enabled=false 放行。
	if err := s.UpsertCanonicalExposureCap(ctx, &CanonicalExposureCap{Canonical: "XAUUSD", CapUSD: 0, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if r := s.CanonicalCapReject(ctx, "XAUUSD", 1); !strings.Contains(r, "冻结") {
		t.Fatalf("cap=0 应冻结: %q", r)
	}
	if err := s.UpsertCanonicalExposureCap(ctx, &CanonicalExposureCap{Canonical: "XAUUSD", CapUSD: 4000, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if r := s.CanonicalCapReject(ctx, "XAUUSD", 99999); r != "" {
		t.Fatalf("enabled=false 应放行: %q", r)
	}
}

// TOCTOU 锚：第一笔守卫建库成功后，同 canonical 第二笔（proposed 撞线）串行到锁后被拦。
func TestD006_CapGuardSequentialReject(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_canonical_exposure_caps, positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.UpsertCanonicalExposureCap(ctx, &CanonicalExposureCap{Canonical: "XAUUSD", CapUSD: 5000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	notional := 4500.0
	id, err := s.CreatePositionWithCapGuard(ctx,
		&Position{OpportunityID: "opp-d006-1", Type: "XASSET_CARRY", Status: "OPENING", Environment: "test"},
		[]string{"XAUUSD"}, nil, notional, nil)
	if err != nil {
		t.Fatalf("第一笔应放行: %v", err)
	}
	// 写腿（OPEN 态仓+canonical 映射）→ 复查面可见。
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET status='OPEN', notional_usd=$1 WHERE id=$2`, notional, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO position_legs
		(position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status)
		VALUES ($1,0,'OPEN',1,'3001157','mt5','XAUUSDm','BUY',0.01,2400,'FILLED')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
		 VALUES ('3001157','mt5','h',443,'p',true) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(context.Background(),
			`DELETE FROM broker_symbol_map WHERE venue='3001157'; DELETE FROM broker_accounts WHERE venue='3001157'`)
	})
	if _, err := s.Pool().Exec(ctx, `INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, source)
		VALUES ('3001157','XAUUSDm','XAUUSD','test') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	// 锁后复查路径直证：同 canonical proposed=1000 → 4500+1000>5000 拒。
	if _, err := s.CreatePositionWithCapGuard(ctx,
		&Position{OpportunityID: "opp-d006-2", Type: "XASSET_CARRY", Status: "OPENING", Environment: "test"},
		[]string{"XAUUSD"}, nil, 1000, nil); err == nil || !strings.Contains(err.Error(), "聚簇超限") {
		t.Fatalf("第二笔应被拦（4500+1000>5000），got %v", err)
	}
	// 不同 canonical 不互锁。
	if _, err := s.CreatePositionWithCapGuard(ctx,
		&Position{OpportunityID: "opp-d006-3", Type: "XASSET_CARRY", Status: "OPENING", Environment: "test"},
		[]string{"BTCUSDT"}, nil, 5900, nil); err != nil {
		t.Fatalf("异 canonical 应放行（默认 cap 6000）: %v", err)
	}
}

// 并发锁语义桩：同 canonical 两笔并发守卫建库，在途 4500 + proposed 1000×2 > cap 5000
// → 至多一笔放行（advisory 锁内复查钉死；无锁形态下两笔同窗全放行=回归锚）。
func TestD006_CapGuardConcurrentSecondBlocked(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_canonical_exposure_caps, positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.UpsertCanonicalExposureCap(ctx, &CanonicalExposureCap{Canonical: "WTIUSD", CapUSD: 5500, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// 底仓在途 4500（含 canonical 映射腿）。
	baseNotional := 4500.0
	pos := &Position{OpportunityID: "opp-d006-base", Type: "XASSET_CARRY", Status: "OPEN", NotionalUSD: &baseNotional, Environment: "test"}
	base, err := s.CreatePositionWithCapGuard(ctx, pos, []string{"WTIUSD"}, nil, 4500, nil)
	if err != nil {
		t.Fatalf("底仓应放行: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO position_legs
		(position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status)
		VALUES ($1,0,'OPEN',1,'3001157','mt5','USOILm','BUY',1,60,'FILLED')`, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
		 VALUES ('3001157','mt5','h',443,'p',true) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(context.Background(),
			`DELETE FROM broker_symbol_map WHERE venue='3001157'; DELETE FROM broker_accounts WHERE venue='3001157'`)
	})
	if _, err := s.Pool().Exec(ctx, `INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, source)
		VALUES ('3001157','USOILm','WTIUSD','test') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	// 两笔并发同 canonical proposed=1000：4500+1000≤5500 第一笔过，
	// 第二笔锁后复查须见 5500+1000>5500 被拦（否则=双放行 TOCTOU 回归）。
	errCh := make(chan error, 2)
	go func() {
		_, err := s.CreatePositionWithCapGuard(ctx,
			&Position{OpportunityID: "opp-d006-c1", Type: "XASSET_CARRY", Status: "OPENING", Environment: "test"},
			[]string{"WTIUSD"}, nil, 1000, nil)
		errCh <- err
	}()
	go func() {
		_, err := s.CreatePositionWithCapGuard(ctx,
			&Position{OpportunityID: "opp-d006-c2", Type: "XASSET_CARRY", Status: "OPENING", Environment: "test"},
			[]string{"WTIUSD"}, nil, 1000, nil)
		errCh <- err
	}()
	e1, e2 := <-errCh, <-errCh
	rejected := 0
	for _, e := range []error{e1, e2} {
		if e != nil {
			if !strings.Contains(e.Error(), "聚簇超限") {
				t.Fatalf("拒因应聚簇超限: %v", e)
			}
			rejected++
		}
	}
	if rejected != 1 {
		t.Fatalf("两笔并发应恰一笔被拦（锁后复查），rejected=%d", rejected)
	}
}
