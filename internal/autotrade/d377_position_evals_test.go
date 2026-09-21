// d377_position_evals_test.go —— D-377 S4：XASSET 退出评估观测轨锚（先红后绿）。
// 五锚：hold 行齐字段/stop 行平仓前落库/quote 缺 fail_closed:no_basis/非 XASSET 零行/写失败 fail-open。
package autotrade

import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

type d377EvalRow struct {
	Decision string
	Basis    *float64
	Anchor   *float64
	Fill     *float64
	Detail   string
}

// d377SeedXAsset 插入 XASSET_CARRY 仓：锚 −9.0、noise 6.9，OPEN FILLED 双腿
// （buy 100.05/sell 100.00 → fillBps=+5.0）。返回 store/posID/引擎（quotes 空 bus）。
func d377SeedXAsset(t *testing.T) (*pgstore.Store, int64, *Engine) {
	t.Helper()
	st := testAccumStore(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE facts, positions, position_legs, position_evals CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical) VALUES ('binance','PAXGUSDT','PAXGUSDT') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed symbol map: %v", err)
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, expected_entry_spread_bps, entry_noise_bps)
		 VALUES ('XASSET_CARRY:XAUUSD:a:b','XASSET_CARRY','OPEN',0,-9.0,6.9) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, account, symbol, side, qty, price_expected, price_actual, status)
		 VALUES ($1,1,'OPEN','277842155:mt5','a','XAUUSD','BUY',1,100,100.05,'FILLED'),
		        ($1,2,'OPEN','binance:perp','b','PAXGUSDT','SELL',1,100,100.00,'FILLED')`, posID); err != nil {
		t.Fatalf("seed legs: %v", err)
	}
	return st, posID, &Engine{store: st, quotes: bus.New(), cfg: Config{BasisStopBps: 6.9}}
}

// d377OpenPos 按引擎退出链同源读仓（ListPositions→scanPositions，含 anchor/noise 列）。
func d377OpenPos(t *testing.T, st *pgstore.Store, posID int64) *pgstore.Position {
	t.Helper()
	rows, err := st.ListPositions(context.Background(), "OPEN", 50, 0)
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	for i := range rows {
		if rows[i].ID == posID {
			return &rows[i]
		}
	}
	t.Fatalf("position %d not found", posID)
	return nil
}

func d377Evals(t *testing.T, st *pgstore.Store, posID int64) []d377EvalRow {
	t.Helper()
	rows, err := st.Pool().Query(context.Background(),
		`SELECT decision, basis_bps, anchor_bps, fill_bps, detail FROM position_evals WHERE position_id=$1 ORDER BY id`, posID)
	if err != nil {
		t.Fatalf("read evals: %v", err)
	}
	defer rows.Close()
	var out []d377EvalRow
	for rows.Next() {
		var r d377EvalRow
		if err := rows.Scan(&r.Decision, &r.Basis, &r.Anchor, &r.Fill, &r.Detail); err != nil {
			t.Fatalf("scan eval: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func d377PublishQuotes(qb *bus.QuoteBus, buyBid, buyAsk float64) {
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: buyBid, Ask: buyAsk, Time: now})
	qb.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 100.05, Ask: 100.10, Time: now})
}

// TestD377_EvalHold_RowLanded 锚1：hold → 一行落库且 basis/anchor/fill 三值齐
// （basis=(100.00−100.10)/100.02×1e4≈−9.998；anchor=−9；fill=+5.0）。
func TestD377_EvalHold_RowLanded(t *testing.T) {
	st, posID, e := d377SeedXAsset(t)
	d377PublishQuotes(e.quotes, 100.00, 100.02)
	should, reason := e.checkExitCondition(context.Background(), d377OpenPos(t, st, posID))
	if should {
		t.Fatalf("锚1: basis −9.998 vs stop 6.9 应 hold, got %q", reason)
	}
	rows := d377Evals(t, st, posID)
	if len(rows) != 1 {
		t.Fatalf("锚1: want 1 eval row, got %d", len(rows))
	}
	r := rows[0]
	if r.Decision != "hold" {
		t.Fatalf("锚1: decision = %q, want hold", r.Decision)
	}
	if r.Basis == nil || math.Abs(*r.Basis-(-9.998)) > 0.01 {
		t.Fatalf("锚1: basis = %v, want ≈−9.998", r.Basis)
	}
	if r.Anchor == nil || *r.Anchor != -9.0 {
		t.Fatalf("锚1: anchor = %v, want −9.0", r.Anchor)
	}
	if r.Fill == nil || math.Abs(*r.Fill-5.0) > 0.01 {
		t.Fatalf("锚1: fill = %v, want ≈+5.0", r.Fill)
	}
}

// TestD377_EvalStop_RowBeforeClose 锚2：基差走坏触 stop → decision=stop 且行在返回前已写。
func TestD377_EvalStop_RowBeforeClose(t *testing.T) {
	st, posID, e := d377SeedXAsset(t)
	d377PublishQuotes(e.quotes, 90.00, 90.02) // basis≈−1122 → PnL=−1113 ≪ −6.9
	should, reason := e.checkExitCondition(context.Background(), d377OpenPos(t, st, posID))
	if !should {
		t.Fatalf("锚2: basis 走坏应触发 stop, got %q", reason)
	}
	rows := d377Evals(t, st, posID)
	if len(rows) != 1 || rows[0].Decision != "stop" {
		t.Fatalf("锚2: want 1 row decision=stop, got %+v", rows)
	}
}

// TestD377_EvalFailClosedNoBasis 锚3：quote 缺失 → fail_closed:no_basis 行（评估跑了但无法判）。
func TestD377_EvalFailClosedNoBasis(t *testing.T) {
	st, posID, e := d377SeedXAsset(t)
	should, _ := e.checkExitCondition(context.Background(), d377OpenPos(t, st, posID))
	if should {
		t.Fatal("锚3: fail-closed 不应触发平仓")
	}
	rows := d377Evals(t, st, posID)
	if len(rows) != 1 || rows[0].Decision != "fail_closed:no_basis" {
		t.Fatalf("锚3: want fail_closed:no_basis, got %+v", rows)
	}
}

// TestD377_EvalNonXASSET_NoRows 锚4：非 XASSET_CARRY 仓评估 → 零行（scope 闸）。
func TestD377_EvalNonXASSET_NoRows(t *testing.T) {
	st, posID, e := d377SeedXAsset(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `UPDATE positions SET type='SWAPARB' WHERE id=$1`, posID); err != nil {
		t.Fatalf("retype: %v", err)
	}
	e.checkExitCondition(ctx, d377OpenPos(t, st, posID))
	if rows := d377Evals(t, st, posID); len(rows) != 0 {
		t.Fatalf("锚4: 非 XASSET 零行, got %+v", rows)
	}
}

// TestD377_EvalFailOpen_InsertError 锚5：写行失败（池关）→ 评估照常返回（fail-open 不阻塞判定）。
func TestD377_EvalFailOpen_InsertError(t *testing.T) {
	st, posID, e := d377SeedXAsset(t)
	d377PublishQuotes(e.quotes, 100.00, 100.02)
	p := d377OpenPos(t, st, posID) // 池关前读仓（被测=评估链，非读取路径）
	e.store.Close()
	should, reason := e.checkExitCondition(context.Background(), p)
	if should || reason != "" {
		t.Fatalf("锚5: fail-open 下评估应照常 hold 返回, got %v/%q", should, reason)
	}
}
