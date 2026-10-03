// d042_datalost_cooldown_test.go —— D-042 R1 验收锚：
// T1 保底段两次 data_lost 后第三轮不开（openCand 冷却守卫本体——flap 径以 ReopenCooldown=0 关闭）；
// T2 perp 有 funding 但无报价不开（双腿闸：funding facts 不作报价活性源——任一腿缺即拦；
// R2 补例：有历史报价但 last change 2h 前>1h 窗 → 不开）；
// T3 24h 后解冻重开；T4 发现段两次 data_lost 后第三轮不开（含正控：零 data_lost 可开）；
// T5 data_lost 计数查询失败 → fail-closed 不开（R2 ②）。
package scan

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d042Opts 保底段守卫断言专用：关 flap 径（ReopenCooldown=0），冷却拦截只剩 openCand 守卫。
func d042Opts() PaperCexOpts {
	o := d555Opts()
	o.ReopenCooldown = 0
	return o
}

// d042AliveStub 双腿报价活性桩（生产=bus LastChangeAt 接线；测试按腿给 ok 位）。
func d042AliveStub(spot, perp bool) func(string, string) (time.Time, bool) {
	at := func(ok bool) time.Time {
		if ok {
			return time.Now()
		}
		return time.Time{}
	}
	return d042AliveStubAt(at(spot), at(perp))
}

// d042AliveStubAt 报价活性桩时间版（R2：ok=true 但 last change 可置陈旧）。
func d042AliveStubAt(spotAt, perpAt time.Time) func(string, string) (time.Time, bool) {
	return func(venueKey, _ string) (time.Time, bool) {
		switch venueKey {
		case "gate:spot":
			return spotAt, !spotAt.IsZero()
		case "gate:perp":
			return perpAt, !perpAt.IsZero()
		}
		return time.Time{}, false
	}
}

// d042SeedAlive seed funding facts（T2：证明 funding 在场不替代报价活性）。
func d042SeedAlive(t *testing.T, s *pgstore.Store, now time.Time, venue string) {
	t.Helper()
	if err := s.InsertFacts(context.Background(), []fact.Fact{
		{Kind: fact.KindFunding, Venue: venue, Symbol: "XAUUSD", Value: 12.5, Unit: "pct_annualized", Ts: now, Src: venue + "_ws"},
	}); err != nil {
		t.Fatalf("seed alive funding: %v", err)
	}
}

// d042SeedDataLost seed data_lost 退出行（直接 SQL：InsertPaperSpotCarry 不含 exit 字段；
// n 行 exit_ts=now-2h·i，均落 24h 冷却窗内）。
func d042SeedDataLost(t *testing.T, s *pgstore.Store, now time.Time, symbol string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		exitTs := now.Add(-time.Duration(i+1) * 2 * time.Hour)
		if _, err := s.Pool().Exec(ctx, `INSERT INTO paper_spot_carry
			(venue_a, venue_type_a, venue_b, venue_type_b, symbol, side, structure,
			 entry_funding_ann, qty_usd, status, exit_ts, exit_reason, entry_ts)
			VALUES ('gate','spot','gate','perp',$1,'short_perp','long_spot_short_perp',
			 0,1000,'exited',$2,'data_lost',$3)`, symbol, exitTs, now.Add(-72*time.Hour)); err != nil {
			t.Fatalf("seed data_lost row: %v", err)
		}
	}
}

func d042CleanRows(t *testing.T, s *pgstore.Store, symbol string) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), `DELETE FROM paper_spot_carry WHERE symbol=$1`, symbol); err != nil {
		t.Fatalf("clean paper rows: %v", err)
	}
}

func d042ActiveCount(t *testing.T, s *pgstore.Store, symbol string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM paper_spot_carry WHERE symbol=$1 AND status='active'`, symbol).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

// TestD042_TwoDataLostBlocksThirdOpen T1：24h 内两次 data_lost → 第三轮不开
// （openCand 冷却守卫拦开，非仅告警——flap 径已关）。
func TestD042_TwoDataLostBlocksThirdOpen(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d042CleanRows(t, s, "XAUUSD")
	now := time.Now()
	d042SeedDataLost(t, s, now, "XAUUSD", 2)
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")

	opened, _, err := StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)},
		now, d042Opts(), nil, d042AliveStub(true, true))
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("两次 data_lost 后冷却应不开: opened=%v", opened)
	}
	if n := d042ActiveCount(t, s, "XAUUSD"); n != 0 {
		t.Fatalf("冷却期不应有 active 行: %d", n)
	}
}

// TestD042_ContinuityInsufficientBlocks T2：perp 有 funding 但无报价 → 不开（R1 退回②：
// funding facts 不作报价活性源）；spot 腿缺同理。双腿各自近 1h 报价有行才开。
func TestD042_ContinuityInsufficientBlocks(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d042CleanRows(t, s, "XAUUSD")
	now := time.Now()
	d042SeedAlive(t, s, now, "gate") // funding facts 在场——不作报价证据
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")

	opened, _, err := StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)},
		now, d042Opts(), nil, d042AliveStub(true, false))
	if err != nil {
		t.Fatalf("step perp dead: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("perp 腿无报价应不开: opened=%v", opened)
	}
	opened, _, err = StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)},
		now, d042Opts(), nil, d042AliveStub(false, true))
	if err != nil {
		t.Fatalf("step spot dead: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("spot 腿无报价应不开: opened=%v", opened)
	}
	// R2：有历史报价（ok=true）但 last change 2h 前（>1h 窗）→ 不开（陈旧报价非活性）。
	opened, _, err = StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)},
		now, d042Opts(), nil, d042AliveStubAt(now.Add(-2*time.Hour), now.Add(-2*time.Hour)))
	if err != nil {
		t.Fatalf("step stale: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("last change >1h 前陈旧报价应不开: opened=%v", opened)
	}
	if n := d042ActiveCount(t, s, "XAUUSD"); n != 0 {
		t.Fatalf("报价缺席期不应有 active 行: %d", n)
	}
}

// TestD042_ThawAfter24h T3：data_lost 行 exit_ts 全部 >24h 前 → 解冻重开。
func TestD042_ThawAfter24h(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d042CleanRows(t, s, "XAUUSD")
	now := time.Now()
	// 3 行 data_lost 但 exit_ts 均 >24h 前（冷却窗外）→ 解冻。
	for i := 0; i < 3; i++ {
		if _, err := s.Pool().Exec(ctx, `INSERT INTO paper_spot_carry
			(venue_a, venue_type_a, venue_b, venue_type_b, symbol, side, structure,
			 entry_funding_ann, qty_usd, status, exit_ts, exit_reason, entry_ts)
			VALUES ('gate','spot','gate','perp','XAUUSD','short_perp','long_spot_short_perp',
			 0,1000,'exited',$1,'data_lost',$2)`, now.Add(-25*time.Hour), now.Add(-96*time.Hour)); err != nil {
			t.Fatalf("seed thaw rows: %v", err)
		}
	}
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")

	opened, _, err := StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)},
		now, d555Opts(), nil, d042AliveStub(true, true))
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || opened[0] != "XAUUSD" {
		t.Fatalf("24h 后解冻应重开: opened=%v", opened)
	}
}

// TestD042_DiscoverySegmentCooldown T4：发现段（无白名单 combo）两次 data_lost 后
// 第三轮不开——守卫在 openCand 入口对保底+发现两路统一生效；正控=零 data_lost 同路可开。
func TestD042_DiscoverySegmentCooldown(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d042CleanRows(t, s, "D042AG")
	now := time.Now()
	cand := []CexPaperCand{d555Cand("D042AG", "gate:spot", "gate:perp", 12.5)}

	// 正控：无 data_lost 历史 → 发现段照开（combo 未种=走发现段）。
	opened, _, err := StepPaperCexCarry(ctx, s, cand, now, d555Opts(), nil, d042AliveStub(true, true))
	if err != nil {
		t.Fatalf("step control: %v", err)
	}
	if len(opened) != 1 || opened[0] != "D042AG" {
		t.Fatalf("发现段正控应开: opened=%v", opened)
	}
	// 历史两轮 data_lost → 第三轮不开。
	d042CleanRows(t, s, "D042AG")
	d042SeedDataLost(t, s, now, "D042AG", 2)
	opened, _, err = StepPaperCexCarry(ctx, s, cand, now, d555Opts(), nil, d042AliveStub(true, true))
	if err != nil {
		t.Fatalf("step cooldown: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("发现段两次 data_lost 后应不开: opened=%v", opened)
	}
	if n := d042ActiveCount(t, s, "D042AG"); n != 0 {
		t.Fatalf("冷却期不应有 active 行: %d", n)
	}
}

// d042ErrStore 包 PaperStore 注入 CountDataLostExits 错误（T5：fail-closed 验收）。
type d042ErrStore struct {
	PaperStore
}

func (d042ErrStore) CountDataLostExits(context.Context, string, string, string, time.Time) (int, error) {
	return 0, errors.New("injected count err")
}

// TestD042_CountErrFailClosed T5：data_lost 计数查询失败 → fail-closed 不开
// （R2 ②：冷却判定不可用不误放开仓）。
func TestD042_CountErrFailClosed(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	d042CleanRows(t, s, "XAUUSD")
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")

	opened, _, err := StepPaperCexCarry(ctx, d042ErrStore{s},
		[]CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)},
		time.Now(), d042Opts(), nil, d042AliveStub(true, true))
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("计数失败应 fail-closed 不开: opened=%v", opened)
	}
	if n := d042ActiveCount(t, s, "XAUUSD"); n != 0 {
		t.Fatalf("fail-closed 不应有 active 行: %d", n)
	}
}
