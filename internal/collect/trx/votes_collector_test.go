// D-233 TRX 待领取实时化测试（SSOT §验收标准 1：S1 快照/S2 边界与积分）。
package trx

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"arb/internal/store/pgstore"
)

// mockWitnessVotesJSON 模拟 TRONSCAN /api/witness 响应（含 annualizedRate 供 Vi 计算）。
const mockWitnessVotesJSON = `[
	{"address":"SR_A_ADDR","name":"SR-A","annualizedRate":"3.29","producer":true,"realTimeVotes":100000,"votes":100000,"producedTotal":5000},
	{"address":"SR_B_ADDR","name":"SR-B","annualizedRate":"2.50","producer":true,"realTimeVotes":50000,"votes":50000,"producedTotal":3000}
]`

// mockVoteVotesJSON 模拟 /api/vote?voter={addr} 响应（用户投 2 SR）。
const mockVoteVotesJSON = `{"totalVotes":18070,"data":[
	{"candidateAddress":"SR_A_ADDR","candidateName":"SR-A","votes":10000,"timestamp":"1700000000000"},
	{"candidateAddress":"SR_B_ADDR","candidateName":"SR-B","votes":8070,"timestamp":"1700000000000"}
]}`

// mockGetAccountVotesJSON 模拟 getaccount 响应（含 allowance 供步进检测）。
const mockGetAccountVotesJSON = `{
	"address":"TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
	"balance":97863827,
	"allowance":7717994,
	"votes":[{"vote_address":"SR_A_ADDR","vote_count":10000},{"vote_address":"SR_B_ADDR","vote_count":8070}],
	"frozenV2":[{"amount":800000000},{"type":"ENERGY"},{"type":"TRON_POWER"}],
	"account_resource":{"delegated_frozenV2_balance_for_energy":17270000000}
}`

// mockGetResourceVotesJSON 模拟 getaccountresource 响应。
const mockGetResourceVotesJSON = `{"NetLimit":1286,"tronPowerLimit":18070}`

// mockGetChainParamsJSON 模拟 getchainparameters 响应。
const mockGetChainParamsJSON = `{"chainParameter":[
	{"key":"getMaintenanceTimeSlot","value":21600000},
	{"key":"getChainBlockReward","value":16000000},
	{"key":"getChainVoteReward","value":160000000}
]}`

// TestVotesCollector_SampleAndStore_ViAccumulation S1+S2：witness 采样→Vi 累积→fin_trx_cycle_vi 落库。
// 验证：两轮 SampleAndStore 后 Vi > 0（annualizedRate 积分生效），user_votes 正确落库。
func TestVotesCollector_SampleAndStore_ViAccumulation(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/api/witness":
			_, _ = w.Write([]byte(mockWitnessVotesJSON))
		case "/api/vote":
			_, _ = w.Write([]byte(mockVoteVotesJSON))
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountVotesJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceVotesJSON))
		case "/wallet/getchainparameters":
			_, _ = w.Write([]byte(mockGetChainParamsJSON))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c := NewVotesCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, "")
	// 第一轮 SampleAndStore（lastSampleAt=零值→不积分，仅设 lastSampleAt）
	if err := c.SampleAndStore(context.Background()); err != nil {
		t.Fatalf("first SampleAndStore: %v", err)
	}
	// 第二轮 SampleAndStore（dt>0→积分 deltaVi）
	if err := c.SampleAndStore(context.Background()); err != nil {
		t.Fatalf("second SampleAndStore: %v", err)
	}

	// 验证 fin_trx_cycle_vi 有行且 Vi > 0
	viRows, err := st.LatestTRXCycleVi(context.Background())
	if err != nil {
		t.Fatalf("LatestTRXCycleVi: %v", err)
	}
	if len(viRows) == 0 {
		t.Fatal("fin_trx_cycle_vi empty after 2 SampleAndStore calls")
	}
	// 应有 2 SR 行（SR_A_ADDR + SR_B_ADDR）
	if len(viRows) != 2 {
		t.Errorf("viRows count = %d, want 2", len(viRows))
	}
	// 至少一个 SR 的 Vi > 0
	hasPositiveVi := false
	for _, r := range viRows {
		if r.Vi != nil && r.Vi.Sign() > 0 {
			hasPositiveVi = true
		}
		// user_votes 应匹配 vote 分布
		if r.SRAddress == "SR_A_ADDR" && r.UserVotes != 10000 {
			t.Errorf("SR_A user_votes = %d, want 10000", r.UserVotes)
		}
		if r.SRAddress == "SR_B_ADDR" && r.UserVotes != 8070 {
			t.Errorf("SR_B user_votes = %d, want 8070", r.UserVotes)
		}
	}
	if !hasPositiveVi {
		t.Error("no SR has positive Vi after 2 SampleAndStore calls (deltaVi integration not working)")
	}
}

// TestVotesCollector_AllowanceStep_CycleBoundary S2：allowance 步进→NotifyAllowanceStep→cycle 边界→Vi checkpoint+cycle+1。
// R-D1 修复：步进检测单一路径=AccountCollector onChange→NotifyAllowanceStep（SampleAndStore 不再自查）。
func TestVotesCollector_AllowanceStep_CycleBoundary(t *testing.T) {
	st := newTestStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/api/witness":
			_, _ = w.Write([]byte(mockWitnessVotesJSON))
		case "/api/vote":
			_, _ = w.Write([]byte(mockVoteVotesJSON))
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountVotesJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceVotesJSON))
		case "/wallet/getchainparameters":
			_, _ = w.Write([]byte(mockGetChainParamsJSON))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c := NewVotesCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, srv.URL, "")
	// 两轮 SampleAndStore 积累 Vi
	_ = c.SampleAndStore(context.Background())
	_ = c.SampleAndStore(context.Background())

	cycleBefore := c.currentCycle

	// 模拟 allowance 步进：直接调 NotifyAllowanceStep（R-D1 单一路径=account onChange 触发）
	c.NotifyAllowanceStep(context.Background())

	if c.currentCycle != cycleBefore+1 {
		t.Errorf("after allowance step: cycle = %d, want %d (cycleBefore+1)", c.currentCycle, cycleBefore+1)
	}
	// cycle 边界后 NotifyAllowanceStep 已 checkpoint 旧 cycle Vi 到 DB 并重置 viBySR。
	// 验证 DB 有旧 cycle（cycleBefore）的 checkpoint 行。
	maxCycle, err := st.MaxTRXCycleViCycle(context.Background())
	if err != nil {
		t.Fatalf("MaxTRXCycleViCycle: %v", err)
	}
	if maxCycle < cycleBefore {
		t.Errorf("DB max cycle = %d, want >= %d (checkpoint written)", maxCycle, cycleBefore)
	}
}

// TestVotesCollector_RestoreCycle S2：重启后从 DB 恢复 currentCycle。
func TestVotesCollector_RestoreCycle(t *testing.T) {
	st := newTestStore(t)
	// 预写一条 fin_trx_cycle_vi（cycle=5）
	st.UpsertTRXCycleVi(context.Background(), pgstore.TRXCycleVi{
		Cycle:     5,
		SRAddress: "SR_A_ADDR",
		Vi:        big.NewInt(1000000),
		UserVotes: 10000,
	})

	c := NewVotesCollector("addr", st, "", "", "")
	// 首次 SampleAndStore → 从 DB 恢复 currentCycle=5
	// 用一个返回空 witness/vote 的 mock server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c.tronscanBase = srv.URL
	c.trongridBase = srv.URL
	_ = c.SampleAndStore(context.Background())

	if c.currentCycle != 5 {
		t.Errorf("restored cycle = %d, want 5", c.currentCycle)
	}
}
