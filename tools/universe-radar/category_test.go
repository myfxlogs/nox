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
	// 共享库残留清基线（上轮跑次的告警/假设行不归本拍计数）
	for _, q := range []string{
		`DELETE FROM alerts WHERE message LIKE '%okx|FUTURES|USD%'`,
		`DELETE FROM fin_opp_hypothesis WHERE category_key='okx|FUTURES|USD'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
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
	if string(proto) != "{}" {
		t.Fatalf("params_proto 应落 {}：%q", proto)
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

	// memo 写败死行重试：置 family_fit/memo/params_proto NULL 模拟上轮写败，
	// 重跑应回填 memo（memoPending 锚）且不产新告警
	if _, err := st.Pool().Exec(ctx,
		`UPDATE fin_opp_hypothesis SET family_fit=NULL, memo=NULL, params_proto=NULL
		 WHERE category_key='okx|FUTURES|USD'`); err != nil {
		t.Fatal(err)
	}
	processCategories(ctx, st, []Source{src}, llm, false)
	if err := st.Pool().QueryRow(ctx,
		`SELECT COALESCE(memo,''), COALESCE(params_proto::text,'') FROM fin_opp_hypothesis
		 WHERE category_key='okx|FUTURES|USD'`).Scan(&memo, &proto); err != nil {
		t.Fatal(err)
	}
	if memo == "" || string(proto) != "{}" {
		t.Fatalf("死行 memo 应回填：memo=%q proto=%q", memo, proto)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='info' AND message LIKE '%okx|FUTURES|USD%'`).
		Scan(&alertCnt); err != nil {
		t.Fatal(err)
	}
	if alertCnt != 1 {
		t.Fatalf("死行回填不产告警：%d", alertCnt)
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

// TestCategoryMemoBackfill D-623③：memo 径归一——直写径（swapScan/hedgeScan 模拟
// Upsert 直写）产 proposed 行，下拍 radar 回填 family_fit/memo 非空。
func TestCategoryMemoBackfill(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	key := "d623|swap|usd"
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_opp_hypothesis WHERE category_key=$1`, key); err != nil {
		t.Fatal(err)
	}
	// 直写径产行（family_fit NULL——swapScan 同形态）。
	if _, _, _, err := st.UpsertHypothesis(ctx, pgstore.OppHypothesis{
		Venue: "3001157", CategoryKey: key, InstType: "swap", Shape: "usd",
		Sample: "XAUUSD", Detail: "正息面入榜",
	}); err != nil {
		t.Fatalf("直写产行: %v", err)
	}
	llm := func(context.Context, string, string) (string, error) {
		return `{"family_fit":"generic_yield","params_proto":{"product":"swap"},"memo":"装得进 generic_yield 参数面"}`, nil
	}
	// 下一拍（零品类信号源——纯回填径）。
	processCategories(ctx, st, nil, llm, false)
	var ff, memo string
	if err := st.Pool().QueryRow(ctx,
		`SELECT COALESCE(family_fit,''), COALESCE(memo,'') FROM fin_opp_hypothesis WHERE category_key=$1`, key).
		Scan(&ff, &memo); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if ff == "" || memo == "" {
		t.Fatalf("回填后 family_fit/memo 应非空: ff=%q memo=%q", ff, memo)
	}
}
