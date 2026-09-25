// d491_admission_test.go —— D-491 S7：证据准入评估器判据分支全覆盖 +
// owner_override 三态 + 检测器快照谓词 fail-closed + depth_want 补采集。
package detector

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
)

// fakeAdmissionStore 评估器窄接口测试替身（无 DSN——全内存证据注入）。
type fakeAdmissionStore struct {
	realMap  map[string]*bool
	legs     []FundingLegStat
	pairs    []FundingPairStat
	ticks    map[string]map[string]bool
	depth    map[string]map[string]float64
	listed   []AdmissionRow // List 返回（含 override 行）；nil 时回落 upserted
	upserted []AdmissionRow
	listErr  error
}

func (f *fakeAdmissionStore) ListVenueIsReal(context.Context) (map[string]*bool, error) {
	return f.realMap, nil
}
func (f *fakeAdmissionStore) FundingWindowLegStats(context.Context, time.Time) ([]FundingLegStat, error) {
	return f.legs, nil
}
func (f *fakeAdmissionStore) FundingWindowPairStats(context.Context, time.Time) ([]FundingPairStat, error) {
	return f.pairs, nil
}
func (f *fakeAdmissionStore) PerpTickPresence(context.Context, time.Time) (map[string]map[string]bool, error) {
	return f.ticks, nil
}
func (f *fakeAdmissionStore) LatestCexDepthUSD(context.Context) (map[string]map[string]float64, error) {
	return f.depth, nil
}
func (f *fakeAdmissionStore) UpsertPerpFundingAdmission(_ context.Context, rows []AdmissionRow) error {
	f.upserted = append([]AdmissionRow{}, rows...)
	return nil
}
func (f *fakeAdmissionStore) ListPerpFundingAdmission(context.Context) ([]AdmissionRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.listed != nil {
		return f.listed, nil
	}
	return f.upserted, nil
}

func d491Cfg() PerpFundingAdmissionConfig {
	return PerpFundingAdmissionConfig{
		MinNetAnnualized: 2.0, DirPct: 0.80, MaxSpreadSD: 15.0,
		MinSamples: 500, PinPct: 50.0, MinDepthUSD: 5000, Window: 7 * 24 * time.Hour,
	}
}

func d491RealMap() map[string]*bool {
	tr, fa := true, false
	return map[string]*bool{"binance:perp": &tr, "okx:perp": &tr, "gate:perp": &fa}
}

// d491GoodEvidence 达标证据底座（XAUUSDT binance↔okx 均值差 10pp）。
func d491GoodEvidence() *fakeAdmissionStore {
	return &fakeAdmissionStore{
		realMap: d491RealMap(),
		legs: []FundingLegStat{
			{Symbol: "XAUUSDT", Venue: "binance", Mean: 15, SD: 2, N: 600},
			{Symbol: "XAUUSDT", Venue: "okx", Mean: 5, SD: 2, N: 600},
		},
		pairs: []FundingPairStat{
			{Symbol: "XAUUSDT", VenueA: "binance", VenueB: "okx", MeanDiff: 10, SDDiff: 3, N: 600, DirPct: 0.9},
		},
		ticks: map[string]map[string]bool{"XAUUSDT": {"binance": true, "okx": true}},
		// fin_cex_depth.symbol=venue 原生形（D-481 CexSymbolKey 归一锚）。
		depth: map[string]map[string]float64{
			"binance": {"XAUUSDT": 9000},
			"okx":     {"XAU-USDT-SWAP": 8000},
		},
	}
}

func findRow(t *testing.T, rows []AdmissionRow, canonical string) AdmissionRow {
	t.Helper()
	for _, r := range rows {
		if r.Canonical == canonical {
			return r
		}
	}
	t.Fatalf("canonical %s 无行（共 %d 行）", canonical, len(rows))
	return AdmissionRow{}
}

// TestD491_Evaluate_Admitted A1 主锚：达标+深度够 → admitted + 检测器快照放行 + want 集在册。
func TestD491_Evaluate_Admitted(t *testing.T) {
	st := d491GoodEvidence()
	d := &PerpFundingDetector{}
	e := NewPerpFundingAdmissionEvaluator(d491Cfg(), st, d)

	if err := e.Evaluate(context.Background()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	row := findRow(t, st.upserted, "XAUUSDT")
	if row.Status != AdmissionAdmitted {
		t.Fatalf("status = %q, want admitted（note=%s）", row.Status, row.Note)
	}
	if row.BestPair != "binance↔okx" {
		t.Errorf("best_pair = %q", row.BestPair)
	}
	if row.DepthMinUSD == nil || *row.DepthMinUSD != 8000 {
		t.Errorf("depth_min_usd = %v, want 8000", row.DepthMinUSD)
	}
	if !d.admitted("XAUUSDT") {
		t.Error("admitted 后检测器快照应放行 XAUUSDT")
	}
	// depth_want：admitted 持续采深（证据保活——admitted 离集则深度老化失新）。
	found := false
	for _, b := range e.DepthWantedBases() {
		if b == "XAU" {
			found = true
		}
	}
	if !found {
		t.Error("admitted canonical 应在 depth_want（去 USDT 尾得 XAU）")
	}
}

// TestD491_Evaluate_DeniedBranches A1 判据分支：σ 超/方向翻转/样本不足/均值差不足/无报价腿。
func TestD491_Evaluate_DeniedBranches(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*fakeAdmissionStore)
		note string
	}{
		{"σ超上限", func(s *fakeAdmissionStore) { s.pairs[0].SDDiff = 20 }, "σ"},
		{"方向翻转", func(s *fakeAdmissionStore) { s.pairs[0].DirPct = 0.5 }, "方向一致性"},
		{"样本不足", func(s *fakeAdmissionStore) { s.legs[1].N = 50 }, "样本不足"},
		{"均值差不足", func(s *fakeAdmissionStore) { s.pairs[0].MeanDiff = 1.0 }, "均值差"},
		{"无报价腿", func(s *fakeAdmissionStore) {
			s.ticks = map[string]map[string]bool{"XAUUSDT": {"binance": true}}
		}, "无报价腿"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := d491GoodEvidence()
			tc.mut(st)
			d := &PerpFundingDetector{}
			e := NewPerpFundingAdmissionEvaluator(d491Cfg(), st, d)
			if err := e.Evaluate(context.Background()); err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			row := findRow(t, st.upserted, "XAUUSDT")
			if row.Status != AdmissionDenied {
				t.Fatalf("status = %q, want denied", row.Status)
			}
			if !strings.Contains(row.Note, tc.note) {
				t.Errorf("note = %q, 应含 %q", row.Note, tc.note)
			}
			if d.admitted("XAUUSDT") {
				t.Error("denied 不应进检测器快照")
			}
		})
	}
}

// TestD491_Evaluate_ReviewBranches 钉顶/平线 → review（极端值不自动放行）。
func TestD491_Evaluate_ReviewBranches(t *testing.T) {
	t.Run("钉顶", func(t *testing.T) {
		st := d491GoodEvidence()
		st.legs[0].Mean = 60 // |均值| ≥ 50 → review
		e := NewPerpFundingAdmissionEvaluator(d491Cfg(), st, &PerpFundingDetector{})
		if err := e.Evaluate(context.Background()); err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		row := findRow(t, st.upserted, "XAUUSDT")
		if row.Status != AdmissionReview || !strings.Contains(row.Note, "钉顶") {
			t.Fatalf("status=%q note=%q, want review/钉顶", row.Status, row.Note)
		}
	})
	t.Run("平线腿", func(t *testing.T) {
		st := d491GoodEvidence()
		st.legs[1] = FundingLegStat{Symbol: "XAUUSDT", Venue: "okx", Mean: 0.1, SD: 0, N: 874}
		e := NewPerpFundingAdmissionEvaluator(d491Cfg(), st, &PerpFundingDetector{})
		if err := e.Evaluate(context.Background()); err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		row := findRow(t, st.upserted, "XAUUSDT")
		if row.Status != AdmissionReview || !strings.Contains(row.Note, "平线") {
			t.Fatalf("status=%q note=%q, want review/平线", row.Status, row.Note)
		}
	})
}

// TestD491_Evaluate_PendingDepth 深度缺行 → pending_depth + depth_want 登记补采。
func TestD491_Evaluate_PendingDepth(t *testing.T) {
	st := d491GoodEvidence()
	st.depth = map[string]map[string]float64{} // 全缺行
	d := &PerpFundingDetector{}
	e := NewPerpFundingAdmissionEvaluator(d491Cfg(), st, d)
	if err := e.Evaluate(context.Background()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	row := findRow(t, st.upserted, "XAUUSDT")
	if row.Status != AdmissionPendingDepth {
		t.Fatalf("status = %q, want pending_depth", row.Status)
	}
	if d.admitted("XAUUSDT") {
		t.Error("pending_depth 不放行")
	}
	found := false
	for _, b := range e.DepthWantedBases() {
		if b == "XAU" {
			found = true
		}
	}
	if !found {
		t.Error("pending_depth 应登记 depth_want 基名 XAU")
	}

	// 浅深度（有行但 <5000）同样 pending_depth（续采观察，非死拒）。
	st2 := d491GoodEvidence()
	st2.depth["okx"]["XAU-USDT-SWAP"] = 1000
	e2 := NewPerpFundingAdmissionEvaluator(d491Cfg(), st2, &PerpFundingDetector{})
	if err := e2.Evaluate(context.Background()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	row2 := findRow(t, st2.upserted, "XAUUSDT")
	if row2.Status != AdmissionPendingDepth || !strings.Contains(row2.Note, "深度不足") {
		t.Fatalf("status=%q note=%q, want pending_depth/深度不足", row2.Status, row2.Note)
	}
}

// TestD491_Override_ThreeState owner_override 三态优先：pinned→放行 / denied→终拒 / ”→status 裁决。
func TestD491_Override_ThreeState(t *testing.T) {
	d := &PerpFundingDetector{}
	st := &fakeAdmissionStore{listed: []AdmissionRow{
		{Canonical: "AAAUSDT", Status: AdmissionDenied, OwnerOverride: OverridePinned},   // 钉死压过 denied
		{Canonical: "BBBUSDT", Status: AdmissionAdmitted, OwnerOverride: OverrideDenied}, // 终拒压过 admitted
		{Canonical: "CCCUSDT", Status: AdmissionAdmitted, OwnerOverride: ""},             // 无钉看裁决
		{Canonical: "DDDUSDT", Status: AdmissionDenied, OwnerOverride: ""},
	}}
	e := NewPerpFundingAdmissionEvaluator(d491Cfg(), st, d)
	if err := e.RefreshSnapshot(context.Background()); err != nil {
		t.Fatalf("RefreshSnapshot: %v", err)
	}
	if !d.admitted("AAAUSDT") {
		t.Error("pinned 应放行（压过 denied 裁决）")
	}
	if d.admitted("BBBUSDT") {
		t.Error("owner denied 应终拒（压过 admitted 裁决）")
	}
	if !d.admitted("CCCUSDT") {
		t.Error("无 override + admitted 应放行")
	}
	if d.admitted("DDDUSDT") {
		t.Error("无 override + denied 应拒")
	}
	// depth_want：admitted 的 CCC 在册；业主终拒的 BBB 不进（终拒不再耗补采配额）。
	want := map[string]bool{}
	for _, b := range e.DepthWantedBases() {
		want[b] = true
	}
	if !want["CCC"] || want["BBB"] {
		t.Errorf("depth_want = %v, 应含 CCC 不含 BBB", e.DepthWantedBases())
	}
}

// TestD491_DetectorFailClosed nil/空快照 → deny（fail-closed 不变式锚）。
func TestD491_DetectorFailClosed(t *testing.T) {
	d := &PerpFundingDetector{}
	if d.admitted("BTCUSDT") {
		t.Error("nil 快照应拒一切 canonical（含旧白名单 BTC）")
	}
	d.SetAdmissionSnapshot(map[string]bool{})
	if d.admitted("BTCUSDT") {
		t.Error("空快照应拒一切")
	}
	d.SetAdmissionSnapshot(map[string]bool{"ONDOUSDT": true})
	if !d.admitted("ONDOUSDT") || d.admitted("BTCUSDT") {
		t.Error("快照只放行在册 canonical——BTC 无特权")
	}
}

// TestD491_ScanRegression A2：非白名单 admitted canonical 产候选（ONDO 先例闭环）；
// 未准入 real 对不产；sim×sim 旁路不变。
func TestD491_ScanRegression(t *testing.T) {
	now := time.Now()
	funding := map[string]map[string]float64{"ONDOUSDT": {"binance": 11.0, "gate": -16.2}}
	snap := map[string]map[string]bus.Quote{
		"ONDOUSDT": {
			"binance:perp": {Symbol: "ONDOUSDT", Venue: "binance", VenueType: "perp", Bid: 1.0, Ask: 1.001, Time: now},
			"gate:perp":    {Symbol: "ONDOUSDT", Venue: "gate", VenueType: "perp", Bid: 0.99, Ask: 1.0, Time: now},
		},
	}
	tr, fa := true, false

	// 未准入 + real×real → 0 候选（fail-closed，等价旧白名单拒绝）
	d := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: 30 * time.Second}
	fp := &fakeFactProvider{funding: funding, isReal: map[string]*bool{"binance:perp": &tr, "gate:perp": &tr}}
	if c := d.Scan(context.Background(), snap, fp); len(c) != 0 {
		t.Fatalf("未准入 real 对应零候选，got %d", len(c))
	}

	// 评估器 admitted → real×real 产候选（D-151 白名单语义由证据准入接管）
	d.SetAdmissionSnapshot(map[string]bool{"ONDOUSDT": true})
	if c := d.Scan(context.Background(), snap, fp); len(c) != 1 {
		t.Fatalf("admitted 后应产 1 候选，got %d", len(c))
	}

	// sim×sim 旁路不依赖准入（D-369 demo 轨保留）
	d2 := &PerpFundingDetector{MinNetAnnualized: 2.0, FreshnessTTL: 30 * time.Second}
	fpSim := &fakeFactProvider{funding: funding, isReal: map[string]*bool{"binance:perp": &fa, "gate:perp": &fa}}
	if c := d2.Scan(context.Background(), snap, fpSim); len(c) != 1 {
		t.Fatalf("sim×sim 旁路应照产候选（无准入快照也放行），got %d", len(c))
	}
}
