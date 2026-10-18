// s2bridge_test.go —— S2 桥锚：cfd_swap_hedged→白名单+trialing；USD_UM→trialing
// （既有轨覆盖注记）；generic_yield→告警留 approved。
package oppmatch

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

func seedHypothesis(t *testing.T, s *pgstore.Store, venue, key, inst, shape, fit string) int64 {
	t.Helper()
	h := pgstore.OppHypothesis{Venue: venue, CategoryKey: key, InstType: inst, Shape: shape}
	id, _, _, err := s.UpsertHypothesis(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE fin_opp_hypothesis SET status='approved', family_fit=$2, decided_at=now() WHERE id=$1`,
		id, fit); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestS2Bridge(t *testing.T) {
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	// #505 形：cfd_swap_hedged → xasset_allow_combos 白名单
	h1 := seedHypothesis(t, s, "15032313", "cfd_swap_hedged|15032313|XAUUSD|short|binance|PAXGUSDT", "cfd_hedged", "swap_hedge", "")
	// #7 形：okx USD_UM → perp_delivery 覆盖注记
	h2 := seedHypothesis(t, s, "okx", "okx|FUTURES|USD_UM_XPERP", "FUTURES", "USD_UM_XPERP", "novel")
	// generic_yield 族未建 → 告警留 approved
	_ = seedHypothesis(t, s, "okx", "okx|EARN|NEWFIX", "EARN", "fixed", "generic_yield")

	s2Bridge(ctx, s)

	for _, id := range []int64{h1, h2} {
		var st string
		if err := s.Pool().QueryRow(ctx,
			`SELECT status FROM fin_opp_hypothesis WHERE id=$1`, id).Scan(&st); err != nil {
			t.Fatal(err)
		}
		if st != "trialing" {
			t.Fatalf("#%d status=%s want trialing", id, st)
		}
	}
	var st string
	if err := s.Pool().QueryRow(ctx,
		`SELECT status FROM fin_opp_hypothesis WHERE venue='okx' AND inst_type='EARN'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "approved" {
		t.Fatalf("generic_yield 行应留 approved: %s", st)
	}
	var combo int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM xasset_allow_combos
		WHERE canonical_buy='PAXGUSDT' AND canonical_sell='XAUUSD' AND venue_buy='binance' AND venue_sell='15032313'`).Scan(&combo); err != nil {
		t.Fatal(err)
	}
	if combo != 1 {
		t.Fatalf("白名单行未入册: %d", combo)
	}
	var warnN int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE 'S2桥：approved 假设%'`).Scan(&warnN); err != nil {
		t.Fatal(err)
	}
	if warnN < 3 {
		t.Fatalf("S2 桥告警缺: %d", warnN)
	}
	// 幂等：再跑一轮零新告警（dedup）+ 零重复白名单
	before := warnN
	s2Bridge(ctx, s)
	s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE 'S2桥：approved 假设%'`).Scan(&warnN)
	if warnN != before {
		t.Fatalf("重拍重推: %d→%d", before, warnN)
	}
	_ = time.Now // keep import if unused
}

// TestS2Bridge_LongSideLegs D-548 复审修锚：side=long 假设腿位必须反向
// （买 MT5/卖 CEX）；未知 side 拒桥不留白名单行（方向错=真金反向仓）。
func TestS2Bridge_LongSideLegs(t *testing.T) {
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	lg := seedHypothesis(t, s, "15032313", "cfd_swap_hedged|15032313|AUDJPY|long|binance|AUDJPY-PERP", "cfd_hedged", "swap_hedge", "")
	bad := seedHypothesis(t, s, "15032313", "cfd_swap_hedged|15032313|AUDUSD|flat|binance|AUDUSD-PERP", "cfd_hedged", "swap_hedge", "")

	s2Bridge(ctx, s)

	var vb, vs string
	if err := s.Pool().QueryRow(ctx, `SELECT venue_buy, venue_sell FROM xasset_allow_combos
		WHERE canonical_buy='AUDJPY' AND canonical_sell='AUDJPY-PERP'`).Scan(&vb, &vs); err != nil {
		t.Fatalf("long 向白名单行未入册: %v", err)
	}
	if vb != "15032313" || vs != "binance" {
		t.Fatalf("long 向腿位反了: buy=%s sell=%s", vb, vs)
	}
	var st2 string
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM fin_opp_hypothesis WHERE id=$1`, lg).Scan(&st2); err != nil {
		t.Fatal(err)
	}
	if st2 != "trialing" {
		t.Fatalf("long 向未入册: %s", st2)
	}
	// 未知 side：拒桥——行留 approved、白名单零行
	var st3 string
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM fin_opp_hypothesis WHERE id=$1`, bad).Scan(&st3); err != nil {
		t.Fatal(err)
	}
	if st3 != "approved" {
		t.Fatalf("未知向应留 approved: %s", st3)
	}
	var badCombo int
	s.Pool().QueryRow(ctx, `SELECT count(*) FROM xasset_allow_combos WHERE canonical_sell='AUDUSD-PERP' OR canonical_buy='AUDUSD-PERP'`).Scan(&badCombo)
	if badCombo != 0 {
		t.Fatalf("未知向错误入册 %d 行", badCombo)
	}
}

// TestS2Bridge_TradifiPerp TRADIFI_PERPETUAL 路由锚（D-623族归——xasset 变体
// 沉点）：params_proto 腿齐→xasset_allow_combos+trialing；腿缺→dedup warn 拒猜腿位。
func TestS2Bridge_TradifiPerp(t *testing.T) {
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	full := seedHypothesis(t, s, "binance", "binance|TRADIFI_PERPETUAL|USD1", "TRADIFI_PERPETUAL", "USD1", "novel")
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_opp_hypothesis SET params_proto=$2 WHERE id=$1`,
		full, `{"mt_venue":"110660304","mt_symbol":"US500Cash#","side":"short","cex_symbol":"SPCXUSD1"}`); err != nil {
		t.Fatal(err)
	}
	bare := seedHypothesis(t, s, "binance", "binance|TRADIFI_PERPETUAL|USDT", "TRADIFI_PERPETUAL", "USDT", "novel")
	// 采集轴双闸种子（D-624 前置闸消费面）：MT 腿 trade_session + binance funding
	// 各一行在 facts——缺一则拒桥（TRADIFI_PERP 采集轴未达告警）。
	if _, err := s.Pool().Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES
		('trade_session','110660304','US500Cash#',1,'mode',now(),'test'),
		('funding','binance','SPCXUSD1',1.5,'pct_annualized',now(),'test')`); err != nil {
		t.Fatal(err)
	}
	// 轴缺反例：funding 零行的 cex_symbol 变体须拦在采集轴未达告警。
	axisMiss := seedHypothesis(t, s, "binance", "binance|TRADIFI_PERPETUAL|USD2", "TRADIFI_PERPETUAL", "USD2", "novel")
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_opp_hypothesis SET params_proto=$2 WHERE id=$1`,
		axisMiss, `{"mt_venue":"110660304","mt_symbol":"US500Cash#","side":"short","cex_symbol":"SPCXUSD2"}`); err != nil {
		t.Fatal(err)
	}

	s2Bridge(ctx, s)

	var st string
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM fin_opp_hypothesis WHERE id=$1`, full).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "trialing" {
		t.Fatalf("腿齐行 status=%s want trialing", st)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM xasset_allow_combos
		WHERE canonical_buy='SPCXUSD1' AND canonical_sell='US500Cash#'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("白名单行未入册: %d", n)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM fin_opp_hypothesis WHERE id=$1`, bare).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "approved" {
		t.Fatalf("腿缺行应留 approved: %s", st)
	}
	var warnN int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts
		WHERE message LIKE '%TRADIFI_PERP 腿参数缺%'`).Scan(&warnN); err != nil {
		t.Fatal(err)
	}
	// 轴缺行：funding 零行→approved 保持+采集轴未达告警（不落白名单）。
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM fin_opp_hypothesis WHERE id=$1`, axisMiss).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "approved" {
		t.Fatalf("轴缺行应留 approved: %s", st)
	}
	var axisWarn int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts
		WHERE message LIKE '%TRADIFI_PERP 采集轴未达%'`).Scan(&axisWarn); err != nil {
		t.Fatal(err)
	}
	if axisWarn < 1 {
		t.Fatal("采集轴未达告警缺失")
	}
	if warnN < 1 {
		t.Fatalf("腿缺 dedup 告警缺: %d", warnN)
	}
}
