package posreview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

type fakeStore struct {
	rows       []pgstore.ClosedForReviewRow
	settlement *pgstore.SettlementRow
	settleErr  error
	upserts    []*pgstore.PositionReview
	upsertErr  map[int64]error
	warns      []string
}

func (f *fakeStore) ListClosedWithoutReview(context.Context) ([]pgstore.ClosedForReviewRow, error) {
	return f.rows, nil
}
func (f *fakeStore) GetSettlementByPos(context.Context, int64) (*pgstore.SettlementRow, error) {
	return f.settlement, f.settleErr
}
func (f *fakeStore) UpsertPositionReview(_ context.Context, r *pgstore.PositionReview) error {
	if f.upsertErr != nil {
		if e := f.upsertErr[r.PositionID]; e != nil {
			return e
		}
	}
	f.upserts = append(f.upserts, r)
	return nil
}
func (f *fakeStore) InsertAlertDedup(_ context.Context, level, msg string, _ time.Duration) error {
	f.warns = append(f.warns, level+":"+msg)
	return nil
}

func fp(v float64) *float64     { return &v }
func sp(v string) *string       { return &v }
func tp(v time.Time) *time.Time { return &v }

func row() pgstore.ClosedForReviewRow {
	return pgstore.ClosedForReviewRow{
		ID: 7, OpportunityID: "XASSET_CARRY:XAUUSD:a:b", Type: "XASSET_CARRY",
		ExpectedNetBPS: fp(12.0), NotionalUSD: fp(5000), ExitKind: sp("carry_death"),
		FeesUSD: fp(4.2), FeeQuality: sp("actual"),
		OpenedAt: tp(time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)),
		ClosedAt: tp(time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)),
	}
}

func deps(st *fakeStore, runOut string, runErr error) Deps {
	return Deps{
		Store:   st,
		Resolve: func(context.Context) (*analyst.Client, error) { return &analyst.Client{}, nil },
		Run: func(context.Context, *analyst.Client, []analyst.Message, string, analyst.LoopCallbacks) (analyst.LoopResult, error) {
			if runErr != nil {
				return analyst.LoopResult{}, runErr
			}
			return analyst.LoopResult{}, nil
		},
	}
}

// runWith 输出版 deps（OnDelta 回吐给定 JSON）。
func depsOut(st *fakeStore, out string, err error) Deps {
	d := deps(st, "", err)
	d.Run = func(_ context.Context, _ *analyst.Client, _ []analyst.Message, _ string, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		if err != nil {
			return analyst.LoopResult{}, err
		}
		cb.OnDelta(out)
		return analyst.LoopResult{}, nil
	}
	return d
}

// TestM45_ReviewOne_GoFace 钦定验收锚：数字面纯 Go——expected=bps×notional/1e4，
// actual=settlement.net_realized（quality≠actual→NULL+质量痕），cost=fees 直读。
func TestM45_ReviewOne_GoFace(t *testing.T) {
	st := &fakeStore{settlement: &pgstore.SettlementRow{NetRealizedUsd: fp(3.1), PnlQuality: "actual"}}
	d := depsOut(st, `{"review_note":"费蚀主导，carry 衰减至死亡阈","followup_action":"reduce"}`, nil)
	if err := d.ReviewOne(context.Background(), &analyst.Client{}, row()); err != nil {
		t.Fatalf("ReviewOne: %v", err)
	}
	r := st.upserts[0]
	if r.ExpectedNetUSD == nil || *r.ExpectedNetUSD != 6.0 { // 12×5000/1e4
		t.Fatalf("expected_net 错: %v", r.ExpectedNetUSD)
	}
	if r.ActualNetUSD == nil || *r.ActualNetUSD != 3.1 {
		t.Fatalf("actual_net 错: %v", r.ActualNetUSD)
	}
	if r.ActualCostUSD == nil || *r.ActualCostUSD != 4.2 {
		t.Fatalf("actual_cost 错: %v", r.ActualCostUSD)
	}
	if r.FollowupAction == nil || *r.FollowupAction != "reduce" {
		t.Fatalf("followup 错: %v", r.FollowupAction)
	}
	if r.ReviewNote == nil || !strings.Contains(*r.ReviewNote, "费蚀") {
		t.Fatalf("note 错: %v", r.ReviewNote)
	}
	if r.HypothesisID == nil || *r.HypothesisID != row().OpportunityID {
		t.Fatalf("hypothesis 错: %v", r.HypothesisID)
	}
}

// TestM45_QualityNull 质量痕锚：settlement.pnl_quality=estimated→actual NULL+痕入 note；
// expected 参数缺→expected NULL 禁外推。
func TestM45_QualityNull(t *testing.T) {
	st := &fakeStore{settlement: &pgstore.SettlementRow{NetRealizedUsd: fp(9.9), PnlQuality: "estimated"}}
	d := depsOut(st, `{"review_note":"x","followup_action":"continue"}`, nil)
	rw := row()
	rw.ExpectedNetBPS = nil // 缺预期源
	if err := d.ReviewOne(context.Background(), &analyst.Client{}, rw); err != nil {
		t.Fatalf("ReviewOne: %v", err)
	}
	r := st.upserts[0]
	if r.ActualNetUSD != nil {
		t.Fatalf("estimated 实绩应 NULL: %v", *r.ActualNetUSD)
	}
	if r.ExpectedNetUSD != nil {
		t.Fatalf("缺源预期应 NULL: %v", *r.ExpectedNetUSD)
	}
	if !strings.Contains(*r.ReviewNote, "实绩质量 estimated") {
		t.Fatalf("质量痕缺: %v", *r.ReviewNote)
	}
}

// TestM45_LLMFailed LLM 违式/败调锚：两次违式→note=llm_failed+warn alert+action NULL。
func TestM45_LLMFailed(t *testing.T) {
	st := &fakeStore{settlement: &pgstore.SettlementRow{PnlQuality: "actual", NetRealizedUsd: fp(1)}}
	d := depsOut(st, `{"review_note":"","followup_action":"bogus"}`, nil)
	if err := d.ReviewOne(context.Background(), &analyst.Client{}, row()); err != nil {
		t.Fatalf("ReviewOne: %v", err)
	}
	r := st.upserts[0]
	if !strings.HasPrefix(*r.ReviewNote, "llm_failed") {
		t.Fatalf("note 应标 llm_failed: %v", *r.ReviewNote)
	}
	if r.FollowupAction != nil {
		t.Fatalf("违式不该落 action: %v", *r.FollowupAction)
	}
	if len(st.warns) != 1 || !strings.Contains(st.warns[0], "warn") {
		t.Fatalf("warn alert 缺: %v", st.warns)
	}
}

// TestM45_NoAI AI 未配锚：nil client→机器 note 落行（纯数字面不虚报叙事）。
func TestM45_NoAI(t *testing.T) {
	st := &fakeStore{settlement: &pgstore.SettlementRow{PnlQuality: "actual", NetRealizedUsd: fp(3.1)}}
	d := deps(st, "", nil)
	d.Resolve = func(context.Context) (*analyst.Client, error) { return nil, errors.New("AI 未配置") }
	c, _ := d.Resolve(context.Background())
	if err := d.ReviewOne(context.Background(), c, row()); err != nil {
		t.Fatalf("ReviewOne: %v", err)
	}
	r := st.upserts[0]
	if !strings.Contains(*r.ReviewNote, "无 AI") {
		t.Fatalf("机器 note 缺标: %v", *r.ReviewNote)
	}
	if r.FollowupAction != nil {
		t.Fatal("无 AI 不落 followup")
	}
}

// TestM45_SweepFailOpen 单仓败 fail-open 续跑锚。
func TestM45_SweepFailOpen(t *testing.T) {
	r1, r2 := row(), row()
	r1.ID, r2.ID = 1, 2
	st := &fakeStore{
		rows:       []pgstore.ClosedForReviewRow{r1, r2},
		settlement: &pgstore.SettlementRow{PnlQuality: "actual", NetRealizedUsd: fp(1)},
		upsertErr:  map[int64]error{1: errors.New("db err")},
	}
	d := depsOut(st, `{"review_note":"n","followup_action":"continue"}`, nil)
	d.Sweep(context.Background())
	if len(st.upserts) != 1 || st.upserts[0].PositionID != 2 {
		t.Fatalf("fail-open 续跑锚破: %+v", st.upserts)
	}
}

// TestM45_PgRoundtrip 真实测试库读写面：CLOSED 无 review→入缺口集→upsert→出集。
func TestM45_PgRoundtrip(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO positions
		(opportunity_id, type, status, expected_net_bps, notional_usd, exit_kind, fees_usd, opened_at, closed_at)
		VALUES ('M45:TEST:a:b','XASSET_CARRY','CLOSED',10,1000,'carry_death',2.5,NOW(),NOW())`); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM position_reviews WHERE hypothesis_id='M45:TEST:a:b'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM positions WHERE opportunity_id='M45:TEST:a:b'`)
	}()
	rows, err := st.ListClosedWithoutReview(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *pgstore.ClosedForReviewRow
	for i := range rows {
		if rows[i].OpportunityID == "M45:TEST:a:b" {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatal("CLOSED 无 review 未入缺口集")
	}
	if err := st.UpsertPositionReview(ctx, &pgstore.PositionReview{
		PositionID: found.ID, HypothesisID: &found.OpportunityID,
		ReviewNote: sp("t"), ReviewedAt: time.Now(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows2, _ := st.ListClosedWithoutReview(ctx)
	for _, r := range rows2 {
		if r.ID == found.ID {
			t.Fatal("upsert 后仍入缺口集")
		}
	}
}
