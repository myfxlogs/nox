// d513_test.go —— D-513 S6 验收锚 A2/A3/A4/A6（A1 在 pgstore 迁移测，A5 在 cmd/core，A7 机检）。
package explorer

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"arb/internal/platform/analyst"

	explorerv1 "arb/internal/gen/arb/explorer/v1"
	"arb/internal/store/pgstore"

	"github.com/jackc/pgx/v5"
)

// --- A2 调度锚点 ---

func TestD513_NextDelay(t *testing.T) {
	at := func(y int, m time.Month, d, h, mi int, loc *time.Location) time.Time {
		return time.Date(y, m, d, h, mi, 0, 0, loc)
	}
	// 日扫 06:47 BJ：前 1 分钟→1min；恰触发→24h；UTC 22:47（=BJ 06:47 恰界）→24h。
	d := NextDelay(at(2026, 9, 27, 6, 46, BJTime), "daily")
	if d != time.Minute {
		t.Fatalf("daily pre: %v", d)
	}
	if d := NextDelay(at(2026, 9, 27, 6, 47, BJTime), "daily"); d != 24*time.Hour {
		t.Fatalf("daily exact: %v", d)
	}
	if d := NextDelay(at(2026, 9, 26, 22, 47, time.UTC), "daily"); d != 24*time.Hour {
		t.Fatalf("daily utc boundary (16:47UTC 恰界族): %v", d)
	}
	// UTC 日界≠BJ 日界用例：UTC 2026-09-26 16:00 = BJ 9-27 00:00（次日历日）。
	if got := ScanDate(at(2026, 9, 26, 16, 30, time.UTC)); got.Format("2006-01-02") != "2026-09-27" {
		t.Fatalf("scan_date BJ 日界: %v", got)
	}
	// 周深挖 周日 07:13 BJ：周日 07:12→1min；周六→次日 07:13；周日 07:13→下周日。
	if w := NextDelay(at(2026, 9, 27, 7, 12, BJTime), "weekly"); w != time.Minute {
		t.Fatalf("weekly pre: %v", w)
	}
	if w := NextDelay(at(2026, 9, 26, 8, 0, BJTime), "weekly"); w != 23*time.Hour+13*time.Minute {
		t.Fatalf("weekly sat→sun: %v", w)
	}
	if w := NextDelay(at(2026, 9, 27, 7, 13, BJTime), "weekly"); w != 7*24*time.Hour {
		t.Fatalf("weekly exact: %v", w)
	}
}

// --- fake store ---

type fakeStore struct {
	disp      []pgstore.ExplorerDispersion
	idles     []pgstore.ExplorerIdlePool
	watchlist []pgstore.RadarWatchlistRow
	maps      []pgstore.SymbolMapping
	alerts    []pgstore.Alert
	state     map[string]json.RawMessage
	proposals []pgstore.ExplorerProposal
	// 探针提案/报告断言面。
	alertRows []string
	probeKeys map[string]bool
	probeSeq  int64
	probeRows []pgstore.ProbeProposal
	appCfg    map[string]float64
	probePos  *pgstore.ProbeRow
	legs      []pgstore.PositionLeg
	cashflows []pgstore.Cashflow
}

func (f *fakeStore) ExplorerTermDispersion(context.Context, time.Time) ([]pgstore.ExplorerDispersion, error) {
	return f.disp, nil
}
func (f *fakeStore) ExplorerIdleFunds(context.Context, time.Time) ([]pgstore.ExplorerIdlePool, error) {
	return f.idles, nil
}
func (f *fakeStore) ExplorerStateGet(_ context.Context, k string) (json.RawMessage, bool, error) {
	v, ok := f.state[k]
	return v, ok, nil
}
func (f *fakeStore) ExplorerStatePut(_ context.Context, k string, v json.RawMessage) error {
	f.state[k] = v
	return nil
}
func (f *fakeStore) ExplorerProposalInsert(_ context.Context, ps []pgstore.ExplorerProposal) (int, error) {
	seen := map[string]bool{}
	n := 0
	for _, p := range f.proposals {
		seen[p.ScanDate.Format("2006-01-02")+p.ScanKind+p.Canonical+p.BestPair] = true
	}
	for _, p := range ps {
		k := p.ScanDate.Format("2006-01-02") + p.ScanKind + p.Canonical + p.BestPair
		if !seen[k] {
			seen[k] = true
			f.proposals = append(f.proposals, p)
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) LoadRadarWatchlist(context.Context) ([]pgstore.RadarWatchlistRow, error) {
	return f.watchlist, nil
}
func (f *fakeStore) ListSymbolMappings(context.Context, string) ([]pgstore.SymbolMapping, error) {
	return f.maps, nil
}
func (f *fakeStore) ListAlerts(context.Context, int, int) ([]pgstore.Alert, error) {
	return f.alerts, nil
}
func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.alertRows = append(f.alertRows, level+" "+msg)
	return nil
}
func (f *fakeStore) ExplorerStatePutIfAbsent(_ context.Context, k string, v json.RawMessage) (bool, error) {
	if _, ok := f.state[k]; ok {
		return false, nil
	}
	f.state[k] = v
	return true, nil
}
func (f *fakeStore) ExplorerProbeProposalInsert(_ context.Context, c pgstore.ProbeContract, d time.Time) (int64, bool, error) {
	k := d.Format("2006-01-02") + "probe" + c.Canonical + c.VenueKey
	if f.probeKeys[k] {
		return 0, false, nil
	}
	f.probeKeys[k] = true
	f.probeSeq++
	f.probeRows = append(f.probeRows, pgstore.ProbeProposal{ID: f.probeSeq, ScanDate: d,
		Canonical: c.Canonical, Status: "proposed", Contract: c})
	return f.probeSeq, true, nil
}
func (f *fakeStore) ExplorerProbeProposalByPosition(_ context.Context, posID int64) (*pgstore.ProbeProposal, error) {
	for i := range f.probeRows {
		if f.probeRows[i].PositionID != nil && *f.probeRows[i].PositionID == posID {
			return &f.probeRows[i], nil
		}
	}
	return nil, pgstore.ErrNoProbeProposal
}
func (f *fakeStore) AppConfigFloat(_ context.Context, key string) (float64, bool, error) {
	v, ok := f.appCfg[key]
	return v, ok, nil
}
func (f *fakeStore) GetProbe(_ context.Context, posID int64) (*pgstore.ProbeRow, error) {
	if f.probePos == nil || f.probePos.ID != posID {
		return nil, pgx.ErrNoRows
	}
	return f.probePos, nil
}
func (f *fakeStore) ListPositionLegs(_ context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return f.legs, nil
}
func (f *fakeStore) ListCashflows(_ context.Context, posID int64) ([]pgstore.Cashflow, error) {
	return f.cashflows, nil
}

// fakeRun 写死 LLM 输出（Run seam 同 sysretro——OnDelta 灌文本）。
func fakeRun(out string) Runner {
	return func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, _ []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		cb.OnDelta(out)
		return analyst.LoopResult{Grounded: true}, nil
	}
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		disp: []pgstore.ExplorerDispersion{
			{Kind: "funding", Canonical: "ETHUSDT", Venues: 3, Min: 0.01, Max: 0.03,
				Range: 0.02, Mean: 0.02, HasPrev: true, PrevMean: 0.008, Delta: 0.012,
				MinVenue: "binance", MaxVenue: "gate"},
		},
		state:     map[string]json.RawMessage{},
		probeKeys: map[string]bool{},
		appCfg:    map[string]float64{},
	}
}

const goodBatch = "摘要：ETH 离散扩大。\n```explorer-proposals\n" +
	`{"narrative":"ETH dispersion widened","proposals":[` +
	`{"canonical":"ETHUSDT","best_pair":"binance×gate","direction":"watch","rationale":"range 0.02","anchor":"term_dispersion","evidence":{"range":0.02}}` +
	`]}` + "\n```\n"

// --- A3 解析锚 ---

func TestD513_ProposalParse(t *testing.T) {
	known := map[string]bool{"ETHUSDT": true}
	b, _, ok := parseBatch(goodBatch)
	if !ok || len(b.Proposals) != 1 {
		t.Fatalf("good batch parse: ok=%v", ok)
	}
	rows, drop := validateProposals(b, known)
	if len(rows) != 1 || drop != 0 || rows[0].BestPair != "binance×gate" {
		t.Fatalf("valid rows: %d drop=%d", len(rows), drop)
	}
	// 红测：大小写碰撞 bestpair≠bestPair → protojson 严格拒收整批。
	if _, _, ok := parseBatch(strings.Replace(goodBatch, `"best_pair"`, `"bestpair"`, 1)); ok {
		t.Fatal("lowercase bestpair should be rejected by strict protojson")
	}
	if _, _, ok := parseBatch(strings.Replace(goodBatch, `"best_pair"`, `"bestPair"`, 1)); !ok {
		t.Fatal("lowerCamel bestPair should parse (protojson name)")
	}
	// 未知字段拒收。
	if _, _, ok := parseBatch(strings.Replace(goodBatch, `"narrative"`, `"narrativ"`, 1)); ok {
		t.Fatal("unknown field should be rejected")
	}
	// 截断 JSON（流中断——未闭合 fence）→ 批解析失败。
	if _, _, ok := parseBatch("摘要\n```explorer-proposals\n{\"narrative\":\"x\",\"proposals\":[{\"canonical\":\"ETH"); ok {
		t.Fatal("truncated batch should fail")
	}
	// 非法 canonical（不在已知集）→ 行丢弃+drop。
	rows, drop = validateProposals(b, map[string]bool{"BTCUSDT": true})
	if len(rows) != 0 || drop != 1 {
		t.Fatalf("unknown canonical: rows=%d drop=%d", len(rows), drop)
	}
	mustParse := func(out string) *explorerv1.ProposalBatch {
		bb, _, ok := parseBatch(out)
		if !ok {
			t.Fatalf("fixture parse failed")
		}
		return bb
	}
	// 键形不符 best_pair → drop。
	bad := mustParse(strings.Replace(goodBatch, "binance×gate", "binance-gate", 1))
	if rows, drop = validateProposals(bad, known); len(rows) != 0 || drop != 1 {
		t.Fatalf("bad pair shape: rows=%d drop=%d", len(rows), drop)
	}
	// direction >16 字 → drop。
	bad2 := mustParse(strings.Replace(goodBatch, `"watch"`, `"`+strings.Repeat("长", 17)+`"`, 1))
	if rows, drop = validateProposals(bad2, known); len(rows) != 0 || drop != 1 {
		t.Fatalf("long direction: rows=%d drop=%d", len(rows), drop)
	}
	// >10 条 → D-078 起截断留前 10（原整批拒收对良性超限过脆，双模型实锤）。
	bb, trunc, ok := parseBatch(nProposalBatch(11))
	if !ok || len(bb.GetProposals()) != maxBatchProposals || trunc != 1 {
		t.Fatalf(">10 proposals should truncate to 10: ok=%v n=%d trunc=%d", ok, len(bb.GetProposals()), trunc)
	}
	// 10 条边界不变（不截断、零截断计数）。
	bb, trunc, ok = parseBatch(nProposalBatch(maxBatchProposals))
	if !ok || len(bb.GetProposals()) != maxBatchProposals || trunc != 0 {
		t.Fatalf("10 proposals boundary: ok=%v n=%d trunc=%d", ok, len(bb.GetProposals()), trunc)
	}
}

// --- A4+A6 端到端 ---

func TestD513_EndToEndAndIdempotent(t *testing.T) {
	fs := newFakeStore()
	dir := t.TempDir()
	deps := Deps{Store: fs, Client: analyst.NewClient("http://x", "k", "m"),
		Run: fakeRun(goodBatch), Now: func() time.Time {
			return time.Date(2026, 9, 27, 6, 47, 0, 0, BJTime) // 触发点=BJ 9-27
		}, InboxPath: dir + "/inbox.md", ResearchDir: dir + "/res"}
	res, err := RunDaily(context.Background(), deps)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if res.Proposals != 1 || res.Drop != 0 || len(fs.proposals) != 1 {
		t.Fatalf("proposals=%d drop=%d rows=%d", res.Proposals, res.Drop, len(fs.proposals))
	}
	if fs.proposals[0].ScanDate.Format("2006-01-02") != "2026-09-27" {
		t.Fatalf("scan_date: %v", fs.proposals[0].ScanDate) // BJ 日界=27 非 UTC 26
	}
	inbox, _ := readFileString(deps.InboxPath)
	if !strings.Contains(inbox, "ETHUSDT") {
		t.Fatalf("inbox row missing: %s", inbox)
	}
	// 同日重跑→dedupe 零重复（A4）。
	res2, err := RunDaily(context.Background(), deps)
	if err != nil || res2.Proposals != 0 || len(fs.proposals) != 1 {
		t.Fatalf("rerun: n=%d rows=%d err=%v", res2.Proposals, len(fs.proposals), err)
	}
	// 截断 JSON → 整批不落库。
	deps.Run = fakeRun("{\"narrative\":…\n```explorer-proposals\n{\"proposals\":[{\"canonical\":\"ETH")
	res3, err := RunDaily(context.Background(), deps)
	if err != nil || res3.Proposals != 0 || len(fs.proposals) != 1 || res3.Drop == 0 {
		t.Fatalf("truncated: %+v rows=%d err=%v", res3, len(fs.proposals), err)
	}
	// 周深挖→研究纪要落 tmpdir。
	wr, err := RunWeekly(context.Background(), deps)
	if err != nil || wr.Path == "" {
		t.Fatalf("weekly: %v %v", wr, err)
	}
	body, _ := readFileString(wr.Path)
	if !strings.Contains(body, "EXPLORER 周深挖") {
		t.Fatalf("weekly file: %s", body)
	}
}

func readFileString(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
