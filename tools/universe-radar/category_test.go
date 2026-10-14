package main

// 新品类自发现 S1 锚测：信号→假设行幂等→LLM memo→info 告警→重跑零重推。

import (
	"context"
	"testing"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

// fakeSrc 测试用品类源（Scan 空行，Categories 返回固定信号）。
type fakeSrc struct {
	sigs []CategorySignal
}

func (f fakeSrc) Venue() string                       { return "okx" }
func (f fakeSrc) Scan(context.Context) ([]Row, error) { return nil, nil }
func (f fakeSrc) Categories(context.Context) ([]CategorySignal, error) {
	return f.sigs, nil
}

func TestCategoryPipeline(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	sig := CategorySignal{
		CategoryKey: "okx|FUTURES|USD", Venue: "okx", InstType: "FUTURES",
		Shape: "USD", Count: 8, Sample: "BTC-USD-250321", Detail: "币本位逆向",
	}
	src := fakeSrc{sigs: []CategorySignal{sig}}
	llm := func(context.Context, string, string) (string, error) {
		return `{"family_fit":"novel","params_proto":{},"memo":"币本位逆向交割，装不进 generic_yield，建议立票评估"}`, nil
	}

	// 首轮：行落+memo+告警（非 bootstrap）
	processCategories(ctx, st, []Source{src}, llm, false)
	var family, memo, status string
	var proto []byte
	if err := st.Pool().QueryRow(ctx,
		`SELECT family_fit, memo, status, params_proto FROM fin_opp_hypothesis WHERE category_key='okx|FUTURES|USD'`).
		Scan(&family, &memo, &status, &proto); err != nil {
		t.Fatalf("假设行未落: %v", err)
	}
	if status != "proposed" || family != "novel" || memo == "" {
		t.Fatalf("假设行破：status=%s family=%s memo=%q", status, family, memo)
	}
	var alertCnt int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='info' AND message LIKE '%okx|FUTURES|USD%'`).
		Scan(&alertCnt); err != nil {
		t.Fatal(err)
	}
	if alertCnt != 1 {
		t.Fatalf("info 告警应 1 条：%d", alertCnt)
	}

	// 次轮：同 category_key 零重推零新告警
	processCategories(ctx, st, []Source{src}, llm, false)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='info' AND message LIKE '%okx|FUTURES|USD%'`).
		Scan(&alertCnt); err != nil {
		t.Fatal(err)
	}
	if alertCnt != 1 {
		t.Fatalf("同键重推破（弃件记忆）：%d", alertCnt)
	}

	// rejected 行抑重推：手翻 rejected 后新信号源同键→零告警
	if _, err := st.Pool().Exec(ctx,
		`UPDATE fin_opp_hypothesis SET status='rejected' WHERE category_key='okx|FUTURES|USD'`); err != nil {
		t.Fatal(err)
	}
	processCategories(ctx, st, []Source{src}, llm, false)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='info' AND message LIKE '%okx|FUTURES|USD%'`).
		Scan(&alertCnt); err != nil {
		t.Fatal(err)
	}
	if alertCnt != 1 {
		t.Fatalf("rejected 弃件记忆破：%d", alertCnt)
	}
	// 清理锚行
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM fin_opp_hypothesis WHERE category_key='okx|FUTURES|USD'`); err != nil {
		t.Fatal(err)
	}
}

// LLM 违式降级：family_fit 越域→novel、无 JSON→占位行照落。
func TestCategoryMemoSchemaGate(t *testing.T) {
	badLLM := func(context.Context, string, string) (string, error) {
		return `{"family_fit":"garbage","memo":"x"}`, nil
	}
	ff, _, _ := catMemo(context.Background(), badLLM, CategorySignal{CategoryKey: "t|x"})
	if ff != "novel" {
		t.Fatalf("越域 family_fit 应降 novel：%s", ff)
	}
	noJSON := func(context.Context, string, string) (string, error) {
		return `not json at all`, nil
	}
	ff, memo, _ := catMemo(context.Background(), noJSON, CategorySignal{CategoryKey: "t|y"})
	if ff != "novel" || memo != "" {
		t.Fatalf("无 JSON 应占位：%s %q", ff, memo)
	}
}

// bootstrap 抑制：首轮假设行照落但零告警。
func TestCategoryBootstrapSilent(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	sig := CategorySignal{
		CategoryKey: "binance|CURRENT_MONTH|USDT", Venue: "binance",
		InstType: "CURRENT_MONTH", Shape: "USDT", Count: 4, Sample: "BTCUSDT_250131",
	}
	processCategories(ctx, st, []Source{fakeSrc{sigs: []CategorySignal{sig}}}, nil, true)
	var cnt int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_opp_hypothesis WHERE category_key='binance|CURRENT_MONTH|USDT'`).
		Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("bootstrap 假设行应照落：%d", cnt)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '%binance|CURRENT_MONTH%'`).
		Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("bootstrap 告警应抑制：%d", cnt)
	}
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM fin_opp_hypothesis WHERE category_key='binance|CURRENT_MONTH|USDT'`); err != nil {
		t.Fatal(err)
	}
}

// UpsertHypothesis 幂等锚（xmax=0 判插入）。pgstore 层同测。
var _ = pgstore.OppHypothesis{} // 引用保 import
