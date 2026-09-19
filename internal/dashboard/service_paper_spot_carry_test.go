// service_paper_spot_carry_test.go —— D-352：纸面轨前端卡（T1 DSN 三态+join / T2 unit TTL+数值锚）。
// 红证：基线无 ListPaperSpotCarry RPC（编译红）。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"

	"connectrpc.com/connect"
)

// paperBoardTestStore D-352 T1 fixture：共享池（D-351 S1）+ 本测试自持清表。
func paperBoardTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	if _, err := st.Pool().Exec(context.Background(),
		`TRUNCATE paper_spot_carry, fin_cex_universe RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return st
}

// seedBoardUni 插入 fin_cex_universe 一行（last_funding_ann 可空）。
func seedBoardUni(t *testing.T, st *pgstore.Store, venue, symbol string, funding *float64) {
	t.Helper()
	if _, err := st.Pool().Exec(context.Background(), `
		INSERT INTO fin_cex_universe (venue, symbol, base, status, first_seen, tier, last_funding_ann)
		VALUES ($1,$2,$3,'TRADING',now()-interval '72 hours','T3',$4)`,
		venue, symbol, symbol, funding); err != nil {
		t.Fatalf("seed uni %s: %v", symbol, err)
	}
}

// seedBoardPaper 插入 paper_spot_carry 一行（status/exit_ts 直接落，绕过状态机；exitAge 非 nil=status exited）。
func seedBoardPaper(t *testing.T, st *pgstore.Store, venue, symbol, side, status string, qty, entryFunding, accrued float64, entryAge time.Duration, exitAge *time.Duration) {
	t.Helper()
	var exitArg *time.Time
	reason := ""
	if exitAge != nil {
		tm := time.Now().Add(-*exitAge)
		exitArg = &tm
		reason = "normalized"
	}
	if _, err := st.Pool().Exec(context.Background(), `
		INSERT INTO paper_spot_carry (venue, symbol, side, entry_funding_ann, qty_usd, accrued_usd,
			entry_ts, last_accrue_ts, status, exit_ts, exit_reason)
		VALUES ($1,$2,$3,$4,$5,$6, now()-($7::double precision * interval '1 second'),
			now()-($7::double precision * interval '1 second'), $8, $9, $10)`,
		venue, symbol, side, entryFunding, qty, accrued,
		entryAge.Seconds(), status, exitArg, reason); err != nil {
		t.Fatalf("seed paper %s: %v", symbol, err)
	}
}

func boardF64(v float64) *float64 { return &v }

func connectReqEmpty() *connect.Request[v1.ListPaperSpotCarryRequest] {
	return connect.NewRequest(&v1.ListPaperSpotCarryRequest{})
}

func symbolsOf(rows []*v1.PaperSpotCarryRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Symbol)
	}
	return out
}

// TestD352_ListPaperSpotCarry_ThreeStatesAndJoin T1：active+demo+近 14d exited 返回、
// 15d 前 exited 不返回；join 取当前 funding（缺行→null 不 crash）；排序三段。
func TestD352_ListPaperSpotCarry_ThreeStatesAndJoin(t *testing.T) {
	st := paperBoardTestStore(t)
	seedBoardUni(t, st, "binance", "GUSDT", boardF64(92))    // active 高 net
	seedBoardUni(t, st, "binance", "B2USDT", boardF64(40.5)) // active 低 net
	seedBoardUni(t, st, "binance", "NEGUSDT", boardF64(-30)) // active 负 funding → 高 |net|
	seedBoardUni(t, st, "binance", "EXITA", boardF64(60))
	seedBoardUni(t, st, "binance", "EXITB2", boardF64(60))
	// DEMOUSDT：universe 缺行 → join null（不 crash 不顶替）

	seedBoardPaper(t, st, "binance", "GUSDT", "short_perp", "active", 1000, 92, 5.2, 48*time.Hour, nil)
	seedBoardPaper(t, st, "binance", "B2USDT", "short_perp", "active", 1000, 40.5, 1.1, 24*time.Hour, nil)
	seedBoardPaper(t, st, "binance", "NEGUSDT", "long_perp", "active", 1000, -30, 0.8, 36*time.Hour, nil)
	seedBoardPaper(t, st, "binance", "DEMOUSDT", "short_perp", "demo", 1000, 88, 2.0, 30*time.Hour, nil)
	d1 := 24 * time.Hour
	seedBoardPaper(t, st, "binance", "EXITA", "short_perp", "exited", 1000, 60, 3.0, 10*24*time.Hour, &d1)
	d15 := 15 * 24 * time.Hour
	seedBoardPaper(t, st, "binance", "EXITB", "short_perp", "exited", 1000, 60, 3.0, 20*24*time.Hour, &d15)
	d2 := 48 * time.Hour
	seedBoardPaper(t, st, "binance", "EXITB2", "short_perp", "exited", 1000, 60, 3.0, 12*24*time.Hour, &d2)

	svc := &Service{store: st}
	resp, err := svc.ListPaperSpotCarry(context.Background(), connectReqEmpty())
	if err != nil {
		t.Fatalf("ListPaperSpotCarry: %v", err)
	}
	rows := resp.Msg.Rows
	if len(rows) != 6 {
		t.Fatalf("rows = %d, want 6（active3+demo1+近 14d exited2；15d 前 EXITB 不返回）: %v", len(rows), symbolsOf(rows))
	}
	// 排序：active（net_now desc，null 殿后）→ demo → exited（exit_ts desc）
	// net：GUSDT=91.85 / NEGUSDT=29.85 / B2USDT=40.35 → 序 GUSDT, B2USDT, NEGUSDT
	wantOrder := []string{"GUSDT", "B2USDT", "NEGUSDT", "DEMOUSDT", "EXITA", "EXITB2"} // exited 段 exit_ts desc=最新退出在前（EXITA 1d 前 > EXITB2 2d 前）
	for i, w := range wantOrder {
		if rows[i].Symbol != w {
			t.Fatalf("rows[%d] = %s, want %s（三段排序）: 全序 %v", i, rows[i].Symbol, w, symbolsOf(rows))
		}
	}
	// join 值 + net_now = |last|−0.15
	g := rows[0]
	if g.LastFundingAnn == nil || *g.LastFundingAnn != 92 {
		t.Errorf("GUSDT last = %v, want 92（join fin_cex_universe）", g.LastFundingAnn)
	}
	if g.NetNow == nil || *g.NetNow != 91.85 {
		t.Errorf("GUSDT net_now = %v, want 91.85（|last|−0.15）", g.NetNow)
	}
	// null 传播：DEMOUSDT universe 缺行 → last/net_now 均 null，行仍在（不 crash 不顶替）
	d := rows[3]
	if d.LastFundingAnn != nil || d.NetNow != nil {
		t.Errorf("DEMOUSDT 缺 universe 行应 last/net_now 均 null: last=%v net=%v", d.LastFundingAnn, d.NetNow)
	}
	if d.Status != "demo" {
		t.Errorf("DEMOUSDT status = %s, want demo", d.Status)
	}
	// days_held 后端算（1 位小数）
	if g.DaysHeld < 1.9 || g.DaysHeld > 2.1 {
		t.Errorf("GUSDT days_held = %v, want ≈2.0（48h）", g.DaysHeld)
	}
	// exited reason 徽章数据源
	if rows[4].ExitReason != "normalized" {
		t.Errorf("EXITB2 exit_reason = %q, want normalized", rows[4].ExitReason)
	}
}

// TestD352_BuildBoard_NumericAnchors T2 数值锚：days_held 1 位小数、net_now=|last|−0.15、
// null 传播；经 fake source 驱动（与 TTL 解耦，注入固定 now）。
func TestD352_BuildBoard_NumericAnchors(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fake := &fakePaperBoardSource{rows: []pgstore.PaperSpotCarry{
		{ID: 1, Venue: "binance", Symbol: "A", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryFundingAnn: 92, EntryTs: now.Add(-36 * time.Hour), AccruedUSD: 1.23},
		{ID: 2, Venue: "binance", Symbol: "B", Side: "long_perp", Status: "demo",
			QtyUSD: 1000, EntryFundingAnn: -30, EntryTs: now.Add(-48 * time.Hour), AccruedUSD: -0.5},
	}}
	fake.uni = map[string]pgstore.CexUniverseRow{
		"binance|A": {Venue: "binance", Symbol: "A", FundingAnn: boardF64(-293.2)}, // |−293.2|−0.15=293.05
		// B 缺行
	}
	rows, err := buildPaperSpotCarryBoard(context.Background(), fake, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rows[0].DaysHeld != 1.5 {
		t.Errorf("A days_held = %v, want 1.5（36h 取整 1 位）", rows[0].DaysHeld)
	}
	if rows[1].DaysHeld != 2 {
		t.Errorf("B days_held = %v, want 2（48h）", rows[1].DaysHeld)
	}
	if rows[0].NetNow == nil || *rows[0].NetNow != 293.05 {
		t.Errorf("A net_now = %v, want 293.05", rows[0].NetNow)
	}
	if rows[1].LastFundingAnn != nil || rows[1].NetNow != nil {
		t.Errorf("B 缺 join 应 null: last=%v net=%v", rows[1].LastFundingAnn, rows[1].NetNow)
	}
}

// TestD352_ListPaperSpotCarry_TTL T2：15s 内二次调用零 DB 查询（fake source 计数）。
func TestD352_ListPaperSpotCarry_TTL(t *testing.T) {
	fake := &fakePaperBoardSource{}
	svc := &Service{paperBoardSrc: fake}
	if _, err := svc.ListPaperSpotCarry(context.Background(), connectReqEmpty()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := svc.ListPaperSpotCarry(context.Background(), connectReqEmpty()); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("TTL 15s 内二次调用应命中缓存（source 查询 1 次），实得 %d 次（红=TTL 摘除）", fake.calls)
	}
}

// fakePaperBoardSource paperBoardSource fake（调用计数驱动 TTL 锚）。
type fakePaperBoardSource struct {
	rows  []pgstore.PaperSpotCarry
	uni   map[string]pgstore.CexUniverseRow
	calls int
}

func (f *fakePaperBoardSource) ListPaperSpotCarryBoard(_ context.Context, _ time.Duration) ([]pgstore.PaperSpotCarry, error) {
	f.calls++
	return f.rows, nil
}

func (f *fakePaperBoardSource) ListCexUniverse(_ context.Context) (map[string]pgstore.CexUniverseRow, error) {
	if f.uni == nil {
		return map[string]pgstore.CexUniverseRow{}, nil
	}
	return f.uni, nil
}
