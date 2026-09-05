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
)

// mockWitnessJSON 模拟 TRONSCAN /api/witness 响应（3 个 SR，2 producer）。
// SR-A: annualizedRate=3.10, producer=true → max 候选
// SR-B: annualizedRate=2.50, producer=true
// SR-C: annualizedRate=1.00, producer=false → max 不取（非 producer）
// SR-D: annualizedRate="" → 畸形行跳过
const mockWitnessJSON = `{"data":[
	{"address":"SR_A_ADDR","name":"SR-A","annualizedRate":"3.10","producer":true,"votes":100000},
	{"address":"SR_B_ADDR","name":"SR-B","annualizedRate":"2.50","producer":true,"votes":50000},
	{"address":"SR_C_ADDR","name":"SR-C","annualizedRate":"1.00","producer":false,"votes":1000},
	{"address":"SR_D_ADDR","name":"SR-D","annualizedRate":"","producer":true,"votes":100}
]}`

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
