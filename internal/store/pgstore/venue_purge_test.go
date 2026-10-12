package pgstore

// 锚点：①venue 有活跃真金仓 → 删除闸拒（CountVenueOpenPositions>0）；
// ②PurgeVenueData 清采集域（ticks/facts/equiv/universe/monitor）+纸面退出，
// 审计域（positions/legs）行不动。
// PurgeVenueData 劈为 PurgeVenueDerived（同步小表）+PurgeVenueBulk（异步批量）。

import (
	"context"
	"testing"
)

func TestD150_CountVenueOpenPositions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountVenueOpenPositions(ctx, "3001157"); err != nil || n != 0 {
		t.Fatalf("empty want 0, got %d err=%v", n, err)
	}
	seedVenuePosition(t, s, ctx)
	if n, err := s.CountVenueOpenPositions(ctx, "3001157"); err != nil || n != 1 {
		t.Fatalf("open want 1, got %d err=%v", n, err)
	}
	// 腿 venue 维度计数：gate 腿被该 open 仓引用 → 计 1（RemoveBroker 只走 MT venue 径，此断言钉语义）
	if n, err := s.CountVenueOpenPositions(ctx, "gate"); err != nil || n != 1 {
		t.Fatalf("gate open leg want 1, got %d err=%v", n, err)
	}
	if n, err := s.CountVenueOpenPositions(ctx, "99999999"); err != nil || n != 0 {
		t.Fatalf("unknown venue want 0, got %d err=%v", n, err)
	}
	// CLOSED 不拦
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET status='CLOSED'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountVenueOpenPositions(ctx, "3001157"); err != nil || n != 0 {
		t.Fatalf("closed want 0, got %d err=%v", n, err)
	}
}

func TestD150_PurgeVenueData(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, q := range []string{
		`TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`,
		`DELETE FROM ticks WHERE venue='62200001'`,
		`DELETE FROM facts WHERE venue='62200001'`,
		`DELETE FROM asset_equiv WHERE venue='62200001'`,
		`DELETE FROM fin_carry_universe WHERE mt_venue='62200001'`,
		`DELETE FROM fin_xasset_monitor WHERE mt_venue LIKE '62200001%'`,
		`DELETE FROM paper_spot_carry WHERE mt_venue='62200001'`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// ticks 按月分区且测试库只随迁移重建（无当月分区）——D-508 同款远月分区播种
	if _, err := s.Pool().Exec(ctx, `CREATE TABLE IF NOT EXISTS ticks_d150 PARTITION OF ticks FOR VALUES FROM ('2030-02-01') TO ('2030-03-01')`); err != nil {
		t.Fatalf("partition: %v", err)
	}
	defer s.Pool().Exec(ctx, `DROP TABLE IF EXISTS ticks_d150`)
	// 播种：ticks/facts/equiv/universe/monitor + active 纸面行
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO ticks (venue, symbol, bid, ask, ts, venue_type) VALUES ('62200001','EURUSD',1.1,1.1001,'2030-02-15','mt5');
		INSERT INTO facts (kind, venue, symbol, value, ts) VALUES ('swap_long','62200001','EURUSD',0.1,now());
		INSERT INTO asset_equiv (asset_class, canonical, venue_type, venue) VALUES ('WTI','WTI','mt5','62200001');
		INSERT INTO fin_carry_universe (canonical, mt_venue, cex_venue, net_annual_pct, funding_annual_pct, structure)
			VALUES ('WTI','62200001','okx',20,10,'short_cfd_long_perp');
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask, entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct, tracking_error_bps, equiv_annual_pct, cex_venue, mt_venue)
			VALUES ('CLUSDT-WTI',1,1,1,1,0,0,0,0,0,0,0,'okx:perp','62200001:mt5');
		INSERT INTO paper_spot_carry (symbol, side, entry_funding_ann, qty_usd, venue_a, venue_b, mt_venue)
			VALUES ('WTI','long_perp',10,100,'62200001','okx','62200001');
	`); err != nil {
		t.Fatal(err)
	}
	derived, err := s.PurgeVenueDerived(ctx, "62200001")
	if err != nil {
		t.Fatalf("derived purge: %v", err)
	}
	for _, k := range []string{"asset_equiv", "fin_carry_universe", "fin_xasset_monitor", "paper_exited"} {
		if derived[k] != 1 {
			t.Fatalf("derived %s want 1, got %v (all=%v)", k, derived[k], derived)
		}
	}
	bulk, err := s.PurgeVenueBulk(ctx, "62200001")
	if err != nil {
		t.Fatalf("bulk purge: %v", err)
	}
	for _, k := range []string{"ticks", "facts"} {
		if bulk[k] != 1 {
			t.Fatalf("bulk %s want 1, got %v (all=%v)", k, bulk[k], bulk)
		}
	}
	var st, reason string
	if err := s.Pool().QueryRow(ctx, `SELECT status, exit_reason FROM paper_spot_carry WHERE mt_venue='62200001'`).Scan(&st, &reason); err != nil {
		t.Fatal(err)
	}
	if st != "exited" || reason != "venue_removed" {
		t.Fatalf("paper row want exited/venue_removed, got %s/%s", st, reason)
	}
}
