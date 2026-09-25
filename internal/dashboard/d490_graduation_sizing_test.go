package dashboard

// D-490 断言锚：毕业授权资金分配 sizing（净值快照折算）。
// 覆盖：pct/usd 双输入 happy+互算回显 / 双池 min 绑定 / 六路 fail-closed /
// dry_run 零副作用 / 显式模式回归（既有 TestD403_* 续绿）。

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
	"connectrpc.com/connect"
)

// d490Fixture：gradFixture(structure) + qbus 双腿报价 + CS/step 解析器 + 池快照。
// perLot：MT 腿=50000×0.001=50；gate perp=50000×0.01=500。
// 池：1279967:mt5 $2000 USD × gate:perp $1000 USDT → minPool=1000（小池绑定）。
func d490Fixture(snaps []pgstore.VenueAccountLatest, mtStep float64) (*fakeGradDB, *Service) {
	return d490FixtureStruct("long_cfd_short_perp", snaps, mtStep)
}

func d490FixtureStruct(structure string, snaps []pgstore.VenueAccountLatest, mtStep float64) (*fakeGradDB, *Service) {
	f, s := gradFixture(structure)
	f.snaps = snaps
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "BTCUSD", Venue: "1279967", VenueType: "mt5", VenueSymbol: "BTCUSD", Bid: 49990, Ask: 50000, Time: now})
	b.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", VenueSymbol: "BTC_USDT", Bid: 49990, Ask: 50000, Time: now})
	s.qbus = b
	s.legContractSize = func(_, _ string) (float64, bool) { return 0.001, true }
	s.cexContractSize = func(_, _ string) (float64, bool) { return 0.01, true }
	s.legVolumeStep = func(_, _ string) (float64, bool) { return mtStep, true }
	return f, s
}

func d490Snaps(eqMT, eqCEX float64, ccy string, age time.Duration) []pgstore.VenueAccountLatest {
	return []pgstore.VenueAccountLatest{
		{Venue: "1279967", VenueType: "mt5", EquityRaw: &eqMT, Currency: ccy, FetchedAt: time.Now().Add(-age)},
		{Venue: "gate", VenueType: "perp", EquityRaw: &eqCEX, Currency: ccy, FetchedAt: time.Now().Add(-age)},
	}
}

func d490f(v float64) *float64 { return &v }

// TestD490_PctHappyPath pct=0.1 × minPool$1000 → alloc$100 → gross100/notional50/
// qty=50÷50×1.1=1.1 与 50÷500×1.1=0.11 写入；note 含分配审计标记；est_* 回显。
func TestD490_PctHappyPath(t *testing.T) {
	f, s := d490Fixture(d490Snaps(2000, 1000, "USD", time.Minute), 0.01)
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, FundingPct: d490f(0.1), Note: "首批",
	}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	if f.combo == nil {
		t.Fatal("combo 未写入")
	}
	if math.Abs(f.combo.MaxGrossUSD-100) > 1e-9 || f.combo.NotionalUSD == nil || math.Abs(*f.combo.NotionalUSD-50) > 1e-9 {
		t.Errorf("gross/notional 落值错: %+v", f.combo)
	}
	if math.Abs(f.combo.MaxLongQty-1.1) > 1e-6 || math.Abs(f.combo.MaxShortQty-0.11) > 1e-6 {
		t.Errorf("派生 qty 错: long=%v short=%v（want 1.1/0.11）", f.combo.MaxLongQty, f.combo.MaxShortQty)
	}
	// est_* 回显（pct 侧互算=USD 折算值）
	if math.Abs(resp.Msg.EstAllocUsd-100) > 1e-9 || math.Abs(resp.Msg.EstGrossUsd-100) > 1e-9 ||
		math.Abs(resp.Msg.EstLegNotionalUsd-50) > 1e-9 {
		t.Errorf("est_* 回显错: %+v", resp.Msg)
	}
	// note 审计标记：pct 侧「分配$X(=P%×净值池$Nperp)」（行首另有 [gaps:] 前缀）
	if !strings.Contains(f.decisionNote, "首批") || !strings.Contains(f.decisionNote, "分配$100.0(=10.0%×净值池$1000perp)") {
		t.Errorf("decisionNote 缺分配标记: %q", f.decisionNote)
	}
	if len(resp.Msg.SizingNotes) == 0 {
		t.Error("sizing_notes 应回显池口径注记")
	}
}

// TestD490_UsdHappyAndEcho usd 直填 + 互算回显（est_alloc_pct）+ 双池 min 绑定小池。
func TestD490_UsdHappyAndEcho(t *testing.T) {
	f, s := d490Fixture(d490Snaps(2000, 1000, "USDT", time.Minute), 0.01)
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, FundingUsd: d490f(200),
	}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v errstr=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	if math.Abs(resp.Msg.EstAllocPct-0.2) > 1e-9 {
		t.Errorf("usd 侧互算回显 est_alloc_pct want 0.2, got %v", resp.Msg.EstAllocPct)
	}
	if math.Abs(resp.Msg.EstAllocUsd-200) > 1e-9 || math.Abs(resp.Msg.EstGrossUsd-200) > 1e-9 {
		t.Errorf("est usd/gross 错: %+v", resp.Msg)
	}
	// 双池 min：usd=200 > 小池? 1000 内 → 过；若错绑大池 2000 口径 pct 会算成 0.1。
	// usd 侧标记「直填=P%×净值池$N」
	if !strings.Contains(f.decisionNote, "分配$200.0(直填=20.0%×净值池$1000)") {
		t.Errorf("decisionNote 缺直填标记: %q", f.decisionNote)
	}
	// usd > minPool → 拒批不写库
	f2, s2 := d490Fixture(d490Snaps(2000, 1000, "USD", time.Minute), 0.01)
	resp2, _ := s2.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, FundingUsd: d490f(1100),
	}))
	if resp2.Msg.Ok || f2.combo != nil {
		t.Error("usd>minPool 应拒批（必然超档）")
	}
	if !strings.Contains(resp2.Msg.Error, "超过小池") {
		t.Errorf("err=%s", resp2.Msg.Error)
	}
	// usd/pool > crossCap(0.5) → 拒批（同安全线判据）
	f3, s3 := d490Fixture(d490Snaps(2000, 1000, "USD", time.Minute), 0.01)
	resp3, _ := s3.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, FundingUsd: d490f(600),
	}))
	if resp3.Msg.Ok || f3.combo != nil {
		t.Error("usd/pool>0.5 应拒批（超安全线）")
	}
	if !strings.Contains(resp3.Msg.Error, "安全线") {
		t.Errorf("err=%s", resp3.Msg.Error)
	}
}

// TestD490_FailClosed 六路 fail-closed：互斥×2 / 池缺 / 过期 / 币种 / minStep 死额度。
// 每路断言 resp.Ok=false 且零写库（combo nil / decision 未落 approved）。
func TestD490_FailClosed(t *testing.T) {
	type tc struct {
		name      string
		structure string
		req       *v1.ApproveCarryGraduationRequest
		snaps     []pgstore.VenueAccountLatest
		step      float64
		want      string
	}
	goodSnaps := d490Snaps(2000, 1000, "USD", time.Minute)
	staleSnaps := d490Snaps(2000, 1000, "USD", 10*time.Minute)
	jpySnaps := d490Snaps(2000, 1000, "JPY", time.Minute)
	longCfd := "long_cfd_short_perp"
	cases := []tc{
		{name: "pct与usd同传", structure: longCfd, req: &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingPct: d490f(0.1), FundingUsd: d490f(50)}, snaps: goodSnaps, step: 0.01, want: "互斥"},
		{name: "与显式cap混传", structure: longCfd, req: &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingUsd: d490f(100), MaxGrossUsd: 500}, snaps: goodSnaps, step: 0.01, want: "互斥"},
		{name: "快照缺失", structure: longCfd, req: &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingPct: d490f(0.1)}, snaps: nil, step: 0.01, want: "快照"},
		{name: "快照过期", structure: longCfd, req: &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingPct: d490f(0.1)}, snaps: staleSnaps, step: 0.01, want: "过期"},
		{name: "币种非USD系", structure: longCfd, req: &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingPct: d490f(0.1)}, snaps: jpySnaps, step: 0.01, want: "币种"},
		{name: "买腿qty低于minStep", structure: longCfd, req: &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingPct: d490f(0.1)}, snaps: goodSnaps, step: 5.0, want: "买腿"},
		// short_cfd：MT 腿在卖侧——卖腿 qty 同样必须过 minStep 预检（否则死额度漏网）。
		{name: "short_cfd卖腿qty低于minStep", structure: "short_cfd_long_perp", req: &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingPct: d490f(0.1)}, snaps: goodSnaps, step: 5.0, want: "卖腿"},
	}
	for _, c := range cases {
		f, s := d490FixtureStruct(c.structure, c.snaps, c.step)
		resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(c.req))
		if err != nil {
			t.Fatalf("%s: rpc err %v", c.name, err)
		}
		if resp.Msg.Ok {
			t.Errorf("%s: 应拒批", c.name)
		}
		if f.combo != nil || f.decision == "approved" {
			t.Errorf("%s: 拒批不得写库", c.name)
		}
		if !strings.Contains(resp.Msg.Error, c.want) {
			t.Errorf("%s: err=%q 缺关键词 %q", c.name, resp.Msg.Error, c.want)
		}
	}
}

// TestD490_DryRunNoSideEffects dry_run：回 est_*+sizing_notes+venue_gaps，
// 零写库零热重载零状态变更（combo/decision/reload/alert 全零）。
func TestD490_DryRunNoSideEffects(t *testing.T) {
	f, s := d490Fixture(d490Snaps(2000, 1000, "USD", time.Minute), 0.01)
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, FundingUsd: d490f(200), DryRun: true,
	}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("dry_run ok=%v errstr=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	if math.Abs(resp.Msg.EstAllocUsd-200) > 1e-9 || math.Abs(resp.Msg.EstAllocPct-0.2) > 1e-9 {
		t.Errorf("dry_run 互算回显错: %+v", resp.Msg)
	}
	if len(resp.Msg.SizingNotes) == 0 {
		t.Error("dry_run 应回 sizing_notes")
	}
	if resp.Msg.ComboId != nil {
		t.Error("dry_run 不得回 combo_id")
	}
	if f.combo != nil || f.decision != "" || f.alerts != 0 {
		t.Errorf("dry_run 零副作用破坏: combo=%v decision=%q alerts=%d", f.combo, f.decision, f.alerts)
	}
	if rl, ok := s.xassetReloader.(*fakeReloader); !ok || rl.calls != 0 {
		t.Error("dry_run 不得触发热重载")
	}
}

// TestD490_ExplicitModeDryRun 显式模式 + dry_run：回显原值不写库（逃生口试算口径一致）。
func TestD490_ExplicitModeDryRun(t *testing.T) {
	f, s := d490Fixture(d490Snaps(2000, 1000, "USD", time.Minute), 0.01)
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, MaxLongQty: 0.01, MaxShortQty: 1000, MaxGrossUsd: 5000, DryRun: true,
	}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("dry_run ok=%v errstr=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	if resp.Msg.EstGrossUsd != 5000 || resp.Msg.EstLongQty != 0.01 || resp.Msg.EstShortQty != 1000 {
		t.Errorf("显式 dry_run 应回显原值: %+v", resp.Msg)
	}
	if f.combo != nil || f.decision != "" {
		t.Error("dry_run 零副作用")
	}
}

// TestD490_PerpPerpFundingRejected perp_perp 无组合闸——资金分配模式不适用（显式拒，不静默吞）。
func TestD490_PerpPerpFundingRejected(t *testing.T) {
	fee, entryB, lastB := 0.5, 10.0, 12.0
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 6, PaperID: 43, Venue: "gate×okx", Symbol: "BTC", Structure: "perp_perp", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 43, Venue: "gate×okx", Structure: "perp_perp",
			Status: "active", AccruedUSD: 5.0, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB},
	}
	s := &Service{gradStore: f, xassetReloader: &fakeReloader{}}
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 6, FundingPct: d490f(0.1),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "不适用") {
		t.Errorf("perp_perp 资金分配应显式拒, ok=%v err=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	if f.decision == "approved" {
		t.Error("拒批不得落账")
	}
}
