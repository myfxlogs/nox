// service_paper_spot_carry_test.go —— D-352：纸面轨前端卡（T1 DSN 三态+join / T2 unit TTL+数值锚）。
// 红证：基线无 ListPaperSpotCarry RPC（编译红）。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"

	"connectrpc.com/connect"
)

// paperBoardTestStore D-352 T1 fixture：共享池（D-351 S1）+ 本测试自持清表。
func paperBoardTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	if _, err := st.Pool().Exec(context.Background(),
		`TRUNCATE paper_spot_carry, fin_cex_universe, carry_graduations RESTART IDENTITY`); err != nil {
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
		INSERT INTO paper_spot_carry (venue_a, venue_b, symbol, side, entry_funding_ann, qty_usd, accrued_usd,
			entry_ts, last_accrue_ts, status, exit_ts, exit_reason)
		VALUES (split_part($1,'×',1),split_part($1,'×',2),$2,$3,$4,$5,$6, now()-($7::double precision * interval '1 second'),
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
		{ID: 1, VenueA: "binance", Symbol: "A", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryFundingAnn: 92, EntryTs: now.Add(-36 * time.Hour), AccruedUSD: 1.23},
		{ID: 2, VenueA: "binance", Symbol: "B", Side: "long_perp", Status: "demo",
			QtyUSD: 1000, EntryFundingAnn: -30, EntryTs: now.Add(-48 * time.Hour), AccruedUSD: -0.5},
	}}
	fake.uni = map[string]pgstore.CexUniverseRow{
		"binance|A": {Venue: "binance", Symbol: "A", FundingAnn: boardF64(-293.2)}, // |−293.2|−0.15=293.05
		// B 缺行
	}
	resp, err := buildPaperSpotCarryBoard(context.Background(), fake, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rows := resp.Rows
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
	rows    []pgstore.PaperSpotCarry
	uni     map[string]pgstore.CexUniverseRow
	cfdNets map[string]float64 // D-060①：CFD 结构行 net_now 读口
	calls   int
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

// D-060①：CFD 结构行 net_now 读口（nil→空 map，与生产读口零行语义一致）。
func (f *fakePaperBoardSource) ListCfdCarryNetNow(_ context.Context) (map[string]float64, error) {
	if f.cfdNets == nil {
		return map[string]float64{}, nil
	}
	return f.cfdNets, nil
}

// TestD354_PnlNumbers D-354 T3：收益率三数+净浮盈——short 基差回落=正/long 反号、
// 净浮盈=accrued+basis−qty×0.3%（$1000→−3 精确锚）、okx/NULL 行「—」路径、实收年化 47.45 量级锚。
func TestD354_PnlNumbers(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	fake := &fakePaperBoardSource{rows: []pgstore.PaperSpotCarry{
		{ID: 1, VenueA: "binance", Symbol: "SHORTOK", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryFundingAnn: 92, EntryTs: now.Add(-2 * 24 * time.Hour), AccruedUSD: 2.6,
			EntryBasisBps: boardF64(50), LastBasisBps: boardF64(30)}, // 基差回落 50→30 → +$2
		{ID: 2, VenueA: "binance", Symbol: "LONGOK", Side: "long_perp", Status: "active",
			QtyUSD: 1000, EntryFundingAnn: -30, EntryTs: now.Add(-2 * 24 * time.Hour), AccruedUSD: 2.6,
			EntryBasisBps: boardF64(50), LastBasisBps: boardF64(30)}, // 多侧基差回落 50→30：short 语义=+2，long 反号=−2（亏损——变异摘符号则变 +2 可观测）
		{ID: 3, VenueA: "okx", Symbol: "AKE", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryFundingAnn: 25, EntryTs: now.Add(-2 * 24 * time.Hour), AccruedUSD: 1.0}, // 无基差
	}}
	resp, err := buildPaperSpotCarryBoard(context.Background(), fake, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	bySym := map[string]*v1.PaperSpotCarryRow{}
	for _, r := range resp.Rows {
		bySym[r.Symbol] = r
	}
	// short：basisPnl=1000×(50−30)/1e4=+2；净浮盈=2.6+2−3=1.6
	s := bySym["SHORTOK"]
	if s.BasisPnlUsd == nil || *s.BasisPnlUsd != 2 {
		t.Errorf("SHORTOK basis_pnl = %v, want 2（基差回落=正）", s.BasisPnlUsd)
	}
	if s.TotalPnlUsd == nil || math.Abs(*s.TotalPnlUsd-1.6) > 1e-9 {
		t.Errorf("SHORTOK total_pnl = %v, want 1.6（accrued+basis−3）", *s.TotalPnlUsd)
	}
	// long：basisPnl=−1000×(50−30)/1e4=−2（多侧基差回落=亏损——反号语义）
	l := bySym["LONGOK"]
	if l.BasisPnlUsd == nil || *l.BasisPnlUsd != -2 {
		t.Errorf("LONGOK basis_pnl = %v, want -2（long_perp 反号：基差回落=负）", l.BasisPnlUsd)
	}
	// okx 无基差：三数「—」路径（nil）
	a := bySym["AKE"]
	if a.BasisPnlUsd != nil || a.TotalPnlUsd != nil {
		t.Errorf("无基差行 basis/total 应 nil（诚实「—」）: %v/%v", a.BasisPnlUsd, a.TotalPnlUsd)
	}
	// 三率：ret=2.6/1000×100=0.26；实收年化=2.6/1000×365/2×100=47.45
	if s.RetPct < 0.25 || s.RetPct > 0.27 {
		t.Errorf("ret_pct = %v, want ≈0.26", s.RetPct)
	}
	if s.RealizedAnnPct == nil || *s.RealizedAnnPct < 47 || *s.RealizedAnnPct > 48 {
		t.Errorf("realized_ann_pct = %v, want ≈47.45（2 天 accrued $2.6）", s.RealizedAnnPct)
	}
	if s.EntryBasisBps == nil || *s.EntryBasisBps != 50 || s.LastBasisBps == nil || *s.LastBasisBps != 30 {
		t.Errorf("基差两列回传: entry=%v last=%v", s.EntryBasisBps, s.LastBasisBps)
	}
}
