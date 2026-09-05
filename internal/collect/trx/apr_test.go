// D-231 TRX 投票收益率采集器测试（SSOT §验收标准 2）。
// 5 条先红后绿单测：①apr 解析（annualizedRate 字符串→float，畸形行跳过）
// ②聚合行正确（max 取 producer=true；current=票数加权平均，含"候选不在 witness 列表跳过+计数"）
// ③1 行/轮落库节流 ④卡片三字段透传（在 dashboard 包 service_hedge_vote_apr_test.go）
// ⑤trx_account 1min Interval 断言。
package trx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockWitnessJSON 模拟 TRONSCAN /api/witness 响应：顶层裸数组，不是 {"data": [...]} 信封。
// 首行是 2026-09-05 实抓响应的裁剪样本（证据：docs/经验库/证据/tronscan-witness-toplevel-array-2026-09-05.log）：
// address/name/url/url_level/producer/latestBlockNumber/latestSlotNumber/missedTotal 字段名逐字保留。
// 其余 SR 行是本测试的最小聚合样本：
// SR-A: annualizedRate=3.10, producer=true → max 候选
// SR-B: annualizedRate=2.50, producer=true
// SR-C: annualizedRate=1.00, producer=false → max 不取（非 producer）
// SR-D: annualizedRate="" → 畸形行跳过
const mockWitnessJSON = `[
	{"address":"TJvaAeFb8Lykt9RQcVyyTFN2iDvGMuyD4M","name":"Poloniex","url":"https://poloniex.com/","url_level":1,"producer":true,"latestBlockNumber":85976160,"latestSlotNumber":596200068,"missedTotal":369},
	{"address":"SR_A_ADDR","name":"SR-A","annualizedRate":"3.10","producer":true,"votes":100000},
	{"address":"SR_B_ADDR","name":"SR-B","annualizedRate":"2.50","producer":true,"votes":50000},
	{"address":"SR_C_ADDR","name":"SR-C","annualizedRate":"1.00","producer":false,"votes":1000},
	{"address":"SR_D_ADDR","name":"SR-D","annualizedRate":"","producer":true,"votes":100}
]`

// mockVoteJSON 模拟 TRONSCAN /api/vote?voter={addr} 响应（3 个候选分布）。
// SR-A: 10000 票 × 3.10% = 31000
// SR-B: 5000 票 × 2.50% = 12500
// SR-UNKNOWN: 2000 票 → 不在 witness 列表 → 跳过+计数
// 加权平均 = (31000+12500)/(10000+5000) = 43500/15000 = 2.90%
const mockVoteJSON = `{"totalVotes":17000,"data":[
	{"candidateAddress":"SR_A_ADDR","candidateName":"SR-A","votes":10000,"timestamp":1700000000000},
	{"candidateAddress":"SR_B_ADDR","candidateName":"SR-B","votes":5000,"timestamp":1700000000000},
	{"candidateAddress":"SR_UNKNOWN_ADDR","candidateName":"SR-UNKNOWN","votes":2000,"timestamp":1700000000000}
]}`

// TestAprCollector_Poll_WitnessTopLevelArray 是 R1 真实形状红测：
// witness 顶层裸数组必须成功解析并落库一行；旧的 data 信封类型会在 Poll 前置解析处失败。
func TestAprCollector_Poll_WitnessTopLevelArray(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/api/witness":
			_, _ = w.Write([]byte(mockWitnessJSON))
		case "/api/vote":
			_, _ = w.Write([]byte(mockVoteJSON))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewAprCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL)
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll with witness top-level array: %v", err)
	}
	var count int
	if err := st.Pool().QueryRow(context.Background(), "SELECT count(*) FROM fin_trx_vote_apr").Scan(&count); err != nil {
		t.Fatalf("count fin_trx_vote_apr: %v", err)
	}
	if count != 1 {
		t.Fatalf("fin_trx_vote_apr rows = %d, want 1", count)
	}
}

// TestAprCollector_Poll_Aggregation 验证 ①apr 解析 + ②聚合行正确：
// annualizedRate 字符串→float；畸形行（""）跳过；max 取 producer=true 降序第一；
// current=票数加权平均，候选不在 witness 列表跳过并计数。
func TestAprCollector_Poll_Aggregation(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/api/witness":
			_, _ = w.Write([]byte(mockWitnessJSON))
		case "/api/vote":
			_, _ = w.Write([]byte(mockVoteJSON))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := NewAprCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL)
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	apr, err := st.LastTRXVoteApr(context.Background())
	if err != nil {
		t.Fatalf("LastTRXVoteApr: %v", err)
	}
	// ① max_rate=3.10（producer=true 降序第一；SR-C producer=false 不取；SR-D 畸形跳过）
	if apr.MaxRate != 3.10 {
		t.Errorf("max_rate = %v, want 3.10 (producer=true 降序第一)", apr.MaxRate)
	}
	if apr.MaxSr == nil || *apr.MaxSr != "SR-A" {
		t.Errorf("max_sr = %v, want SR-A", apr.MaxSr)
	}
	// ② current_rate=票数加权平均 = 2.90（SR-UNKNOWN 跳过）
	const wantCurrent = (10000*3.10 + 5000*2.50) / (10000 + 5000)
	if apr.CurrentRate < wantCurrent-0.01 || apr.CurrentRate > wantCurrent+0.01 {
		t.Errorf("current_rate = %v, want ~%.2f (票数加权平均，SR-UNKNOWN 跳过)", apr.CurrentRate, wantCurrent)
	}
	// current_sr=票数最多者=SR-A
	if apr.CurrentSr == nil || *apr.CurrentSr != "SR-A" {
		t.Errorf("current_sr = %v, want SR-A (票数最多)", apr.CurrentSr)
	}
	// spread = max − current
	wantSpread := 3.10 - wantCurrent
	if apr.Spread < wantSpread-0.01 || apr.Spread > wantSpread+0.01 {
		t.Errorf("spread = %v, want ~%.2f", apr.Spread, wantSpread)
	}
}

// TestAprCollector_Poll_OneRowPerPoll 验证 ③1 行/轮落库节流：
// 连续 2 次 Poll → fin_trx_vote_apr 恰好 2 行（每轮聚合一行，不洪水）。
func TestAprCollector_Poll_OneRowPerPoll(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(mockWitnessJSON))
	}))
	defer srv.Close()

	c := NewAprCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL)
	for i := 0; i < 2; i++ {
		if _, err := c.Poll(context.Background()); err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
	}
	var count int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_trx_vote_apr").Scan(&count)
	if count != 2 {
		t.Errorf("fin_trx_vote_apr rows = %d, want 2 (1 行/轮，不洪水)", count)
	}
}

// TestWireTrx_AccountIntervalOneMinute 见 cmd/core/wire_trx_test.go（buildTrxCollectors 在 main 包）。

// ---------- D-231 S5 实时性测试（⑥⑦⑧） ----------

// TestComputeAccountFingerprint ⑥指纹检测：votes 分布变化→不同指纹、不变→同指纹、排序确定性。
// 纯函数测试（无需 DSN）：验证 computeAccountFingerprint 对 votes 顺序无关 + balance/staked 参与。
func TestComputeAccountFingerprint(t *testing.T) {
	votesA := []voteJSON{
		{VoteAddress: "SR_A", VoteCount: 10000},
		{VoteAddress: "SR_B", VoteCount: 5000},
	}
	votesAShuffled := []voteJSON{
		{VoteAddress: "SR_B", VoteCount: 5000},
		{VoteAddress: "SR_A", VoteCount: 10000},
	}
	// 顺序无关 → 同指纹
	fp1 := computeAccountFingerprint(votesA, 100, 200)
	fp2 := computeAccountFingerprint(votesAShuffled, 100, 200)
	if fp1 != fp2 {
		t.Errorf("指纹非排序确定：A=%q shuffled=%q（应相同）", fp1, fp2)
	}
	// votes 分布变化 → 不同指纹
	votesChanged := []voteJSON{
		{VoteAddress: "SR_A", VoteCount: 10000},
		{VoteAddress: "SR_C", VoteCount: 5000}, // SR_B → SR_C
	}
	fp3 := computeAccountFingerprint(votesChanged, 100, 200)
	if fp1 == fp3 {
		t.Errorf("votes 变化未反映到指纹：both=%q（应不同）", fp1)
	}
	// balance 变化 → 不同指纹
	fp4 := computeAccountFingerprint(votesA, 999, 200)
	if fp1 == fp4 {
		t.Errorf("balance 变化未反映到指纹：both=%q（应不同）", fp1)
	}
	// staked 变化 → 不同指纹
	fp5 := computeAccountFingerprint(votesA, 100, 999)
	if fp1 == fp5 {
		t.Errorf("staked 变化未反映到指纹：both=%q（应不同）", fp1)
	}
}

// TestAccountCollector_FingerprintOnChange ⑥指纹检测集成：votes 分布变化→onChange 触发、不变→不触发、重启首轮不触发。
// DSN-gated：Poll 写 fin_trx_account 表。
func TestAccountCollector_FingerprintOnChange(t *testing.T) {
	st := newTestStore(t)
	var onChangeCalled bool
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		}
	}))
	defer srv.Close()

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, "")
	c.SetOnChange(func() { onChangeCalled = true; callCount++ })

	// 首轮：firstPoll=true → 不触发（指纹未知，下轮自然恢复比较）
	onChangeCalled = false
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 1 (first): %v", err)
	}
	if onChangeCalled {
		t.Error("首轮 onChange 被触发（应 firstPoll=true 不触发）")
	}

	// 第二轮：同指纹 → 不触发
	onChangeCalled = false
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 2 (same): %v", err)
	}
	if onChangeCalled {
		t.Error("指纹未变但 onChange 被触发（应不触发）")
	}

	// 第三轮：换票 → 触发（mock 改 votes）
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(`{"address":"TWsbXR6SPvvXPxUtPMQvEj7AMH55555555","balance":97863827,"votes":[{"vote_address":"TNewSRAddr","vote_count":18070}],"frozenV2":[{"amount":800000000}],"account_resource":{"delegated_frozenV2_balance_for_energy":17270000000}}`))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		}
	})
	onChangeCalled = false
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 3 (changed): %v", err)
	}
	if !onChangeCalled {
		t.Error("votes 变化但 onChange 未触发（应触发）")
	}
	if callCount != 1 {
		t.Errorf("onChange 调用次数 = %d, want 1（仅第三轮触发）", callCount)
	}
}

// TestAprCollector_TriggerNowDebounce ⑦触发去抖 ≥60s：首次触发→Poll；去抖窗内再触发→不 Poll。
// 用 SetDebounce(100ms) 缩短去抖窗测试；首次 TriggerNow 调 Poll（DSN-gated），第二次立即调应被去抖。
func TestAprCollector_TriggerNowDebounce(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/api/witness":
			_, _ = w.Write([]byte(mockWitnessJSON))
		case "/api/vote":
			_, _ = w.Write([]byte(mockVoteJSON))
		}
	}))
	defer srv.Close()

	c := NewAprCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL)
	c.SetDebounce(100 * time.Millisecond) // 测试用短去抖窗

	// 首次 TriggerNow → 触发 Poll（落库一行）
	c.TriggerNow(context.Background())
	var count int
	if err := st.Pool().QueryRow(context.Background(), "SELECT count(*) FROM fin_trx_vote_apr").Scan(&count); err != nil {
		t.Fatalf("count after first trigger: %v", err)
	}
	if count != 1 {
		t.Fatalf("首次 TriggerNow 后 fin_trx_vote_apr rows = %d, want 1（应触发 Poll）", count)
	}

	// 立即第二次 TriggerNow → 去抖（不触发 Poll，行数不变）
	c.TriggerNow(context.Background())
	if err := st.Pool().QueryRow(context.Background(), "SELECT count(*) FROM fin_trx_vote_apr").Scan(&count); err != nil {
		t.Fatalf("count after second trigger: %v", err)
	}
	if count != 1 {
		t.Errorf("去抖窗内第二次 TriggerNow 后 rows = %d, want 1（应去抖不触发）", count)
	}

	// 等待去抖窗过期 → 第三次 TriggerNow → 触发 Poll
	time.Sleep(150 * time.Millisecond)
	c.TriggerNow(context.Background())
	if err := st.Pool().QueryRow(context.Background(), "SELECT count(*) FROM fin_trx_vote_apr").Scan(&count); err != nil {
		t.Fatalf("count after third trigger: %v", err)
	}
	if count != 2 {
		t.Errorf("去抖窗过期后第三次 TriggerNow 后 rows = %d, want 2（应触发 Poll）", count)
	}
}

// TestAprCollector_HedgeHookOnChangeOnly ⑧聚合值无变化不发布/有变化发布：
// 首次 Poll→发布（无上轮=变化）；第二次同值→不发布；第三次改值→发布。
// DSN-gated：Poll 写 fin_trx_vote_apr 表。
func TestAprCollector_HedgeHookOnChangeOnly(t *testing.T) {
	st := newTestStore(t)
	var publishCount int
	var currentWitness = mockWitnessJSON
	var currentVote = mockVoteJSON
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/api/witness":
			_, _ = w.Write([]byte(currentWitness))
		case "/api/vote":
			_, _ = w.Write([]byte(currentVote))
		}
	}))
	defer srv.Close()

	c := NewAprCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL)
	c.SetHedgeHook(func() { publishCount++ })

	// 首次 Poll → 发布（hasLastValues=false=变化）
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 1: %v", err)
	}
	if publishCount != 1 {
		t.Errorf("首次 Poll 后 publishCount = %d, want 1（无上轮=变化→发布）", publishCount)
	}

	// 第二次同值 → 不发布
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 2: %v", err)
	}
	if publishCount != 1 {
		t.Errorf("同值第二次 Poll 后 publishCount = %d, want 1（无变化不发布）", publishCount)
	}

	// 第三次改值（SR-A rate 3.10→4.10）→ 发布
	currentWitness = `[
	{"address":"TJvaAeFb8Lykt9RQcVyyTFN2iDvGMuyD4M","name":"Poloniex","url":"https://poloniex.com/","url_level":1,"producer":true,"latestBlockNumber":85976160,"latestSlotNumber":596200068,"missedTotal":369},
	{"address":"SR_A_ADDR","name":"SR-A","annualizedRate":"4.10","producer":true,"votes":100000},
	{"address":"SR_B_ADDR","name":"SR-B","annualizedRate":"2.50","producer":true,"votes":50000},
	{"address":"SR_C_ADDR","name":"SR-C","annualizedRate":"1.00","producer":false,"votes":1000},
	{"address":"SR_D_ADDR","name":"SR-D","annualizedRate":"","producer":true,"votes":100}
]`
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 3: %v", err)
	}
	if publishCount != 2 {
		t.Errorf("改值第三次 Poll 后 publishCount = %d, want 2（有变化→发布）", publishCount)
	}
}
