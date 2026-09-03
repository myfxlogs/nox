// D-211 S2 采集器测试：httptest mock TronGrid + 分类规则 + 翻页 + P2-3 不入库。
package trx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// newTestStore 创建测试 store（DSN-gated，ARB_TEST_PG_DSN）。
func newTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, tbl := range []string{"fin_trx_account", "fin_trx_txs", "fin_trx_witness"} {
		if _, err := st.Pool().Exec(context.Background(), "DELETE FROM "+tbl); err != nil {
			t.Fatalf("cleanup %s: %v", tbl, err)
		}
	}
	return st
}

// mockGetAccountJSON 模拟 getaccount 响应（D-210 实测锚值）。
const mockGetAccountJSON = `{
	"address":"TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
	"balance":97863827,
	"allowance":7717994,
	"votes":[{"vote_address":"TNeEwWHXLLUgEtfzTnYN8wtVenGxuMzZCE","vote_count":18070}],
	"frozenV2":[{"amount":800000000},{"type":"ENERGY"},{"type":"TRON_POWER"}],
	"account_resource":{"delegated_frozenV2_balance_for_energy":17270000000}
}`

const mockGetResourceJSON = `{"NetLimit":1286,"tronPowerLimit":18070}`

const mockListWitnessesJSON = `{"witnesses":[
	{"address":"418b0359acac03bac62cbf89c4b787cb10b3c3f513","voteCount":759857782,"url":"https://okcoin.jp/","isJobs":true},
	{"address":"41abc","voteCount":100,"url":"","isJobs":false}
]}`

// TestAccountCollector_Poll_Success 验证账户快照采集+frozenV2 拼合。
func TestAccountCollector_Poll_Success(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/getaccountresource":
			_, _ = w.Write([]byte(mockGetResourceJSON))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, "")
	facts, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if facts != nil {
		t.Errorf("facts = %v, want nil (P2-1)", facts)
	}
	acc, err := st.LastTRXAccount(context.Background())
	if err != nil {
		t.Fatalf("LastTRXAccount: %v", err)
	}
	// frozenV2 拼合锚（D-210 §2）
	if acc.BandwidthStaked != 800000000 {
		t.Errorf("BandwidthStaked = %d, want 800000000", acc.BandwidthStaked)
	}
	if acc.EnergyDelegated != 17270000000 {
		t.Errorf("EnergyDelegated = %d, want 17270000000", acc.EnergyDelegated)
	}
	if acc.VotesTotal != 18070 {
		t.Errorf("VotesTotal = %d, want 18070", acc.VotesTotal)
	}
	if acc.BalanceSun != 97863827 {
		t.Errorf("BalanceSun = %d, want 97863827", acc.BalanceSun)
	}
	if acc.AllowanceSun != 7717994 {
		t.Errorf("AllowanceSun = %d, want 7717994", acc.AllowanceSun)
	}
	if acc.NetLimit != 1286 {
		t.Errorf("NetLimit = %d, want 1286", acc.NetLimit)
	}
}

// TestAccountCollector_Poll_RESTFail 验证非 200 返回错误。
func TestAccountCollector_Poll_RESTFail(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := NewAccountCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, "")
	if _, err := c.Poll(context.Background()); err == nil {
		t.Fatal("Poll should fail on 500")
	}
}

// TestTxsCollector_Poll_Classify 验证 7 类分类+env 命中+P2-3 不入库。
func TestTxsCollector_Poll_Classify(t *testing.T) {
	st := newTestStore(t)
	rentalPayer := "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"
	monitorAddr := "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"

	// 构造 7 类合约交易样本
	txs := []string{
		// 1. rental_income（转入+付款方命中）
		makeTransferTx("tx1", monitorAddr, rentalPayer, 5320000),
		// 2. deposit（转入+其他对手方）
		makeTransferTx("tx2", monitorAddr, "TOtherAddr111111111111111111111", 1000000),
		// 3. withdraw（转出）
		makeTransferTx("tx3", "TOtherAddr111111111111111111111", monitorAddr, 2000000),
		// 4. vote_claim（WithdrawBalanceContract，amount=0）
		makeContractTx("tx4", "WithdrawBalanceContract"),
		// 5. stake（FreezeBalanceV2）
		makeContractTx("tx5", "FreezeBalanceV2Contract"),
		// 6. unstake（UnfreezeBalanceV2）
		makeContractTx("tx6", "UnfreezeBalanceV2Contract"),
		// 7. DelegateResource（P2-3 不入库）
		makeContractTx("tx7", "DelegateResourceContract"),
		// 8. VoteWitness（P2-3 不入库）
		makeContractTx("tx8", "VoteWitnessContract"),
	}
	resp := `{"data":[` + joinJSON(txs) + `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	c := NewTxsCollector(monitorAddr, st, srv.URL, "", []string{rentalPayer})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rows, err := st.ListTRXTxs(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListTRXTxs: %v", err)
	}
	// 8 笔交易 - 2 笔不入库（Delegate/VoteWitness）= 6 笔
	if len(rows) != 6 {
		t.Fatalf("got %d txs, want 6 (P2-3: Delegate/VoteWitness 不入库)", len(rows))
	}
	// 验证分类
	kinds := map[string]bool{}
	for _, r := range rows {
		kinds[r.Kind] = true
	}
	for _, want := range []string{"rental_income", "deposit", "withdraw", "vote_claim", "stake", "unstake"} {
		if !kinds[want] {
			t.Errorf("missing kind %q", want)
		}
	}
	// vote_claim amount=0（P2-2）
	for _, r := range rows {
		if r.Kind == "vote_claim" && r.AmountSun != 0 {
			t.Errorf("vote_claim AmountSun = %d, want 0 (P2-2)", r.AmountSun)
		}
	}
}

// TestTxsCollector_Poll_Pagination 验证翻页（≤10 页，每页 200 条）。
func TestTxsCollector_Poll_Pagination(t *testing.T) {
	st := newTestStore(t)
	monitorAddr := "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"
	// 构造 250 条（2 页：200+50）
	var txs []string
	for i := 0; i < 250; i++ {
		txs = append(txs, makeTransferTx(
			fmtTxID(i), "TOtherAddr111111111111111111111", monitorAddr, 1000000))
	}
	// 服务端按 fingerprint 分页（止血4 真实语义）：首页 fp=P1，带 fingerprint=P1 返回次页 fp 空
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fp := r.URL.Query().Get("fingerprint")
		var page []string
		var meta string
		if fp == "" {
			page = txs[:200]
			meta = `,"meta":{"fingerprint":"P1"}`
		} else {
			page = txs[200:]
			meta = `,"meta":{"fingerprint":""}`
		}
		_, _ = w.Write([]byte(`{"data":[` + joinJSON(page) + `]` + meta + `}`))
	}))
	defer srv2.Close()

	c := NewTxsCollector(monitorAddr, st, srv2.URL, "", nil)
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rows, err := st.ListTRXTxs(context.Background(), 300)
	if err != nil {
		t.Fatalf("ListTRXTxs: %v", err)
	}
	if len(rows) != 250 {
		t.Errorf("got %d txs, want 250 (2 页翻页)", len(rows))
	}
}

// TestTxsCollector_Poll_Idempotent 验证 tx_id 幂等去重（重复 poll 不重复写入）。
func TestTxsCollector_Poll_Idempotent(t *testing.T) {
	st := newTestStore(t)
	monitorAddr := "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"
	tx := makeTransferTx("dup1", "TOtherAddr111111111111111111111", monitorAddr, 1000000)
	resp := `{"data":[` + tx + `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	c := NewTxsCollector(monitorAddr, st, srv.URL, "", nil)
	// 第一次 poll
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 1: %v", err)
	}
	// 第二次 poll（同一条 tx）——max_timestamp 已推进，但 mock 仍返回同一条
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 2: %v", err)
	}
	rows, err := st.ListTRXTxs(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListTRXTxs: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("got %d txs, want 1 (tx_id 幂等)", len(rows))
	}
}

// TestTxsCollector_Poll_R5GapBackfill D-211 R5：缺口兜底——库内 oldest > 2024-01-01
// 说明 429 断点后 lastTs 推进致早期段缺 → forceFullBackfill=true，加 min_timestamp=0
// + 禁用增量停止，重写早期段。
// 锚：预插 1 条 2025-03 tx（oldest=2025-03 > 2024-01-01 → 触发兜底），
// mock 返回 2 条（2025-03 已有 + 2023-06 早期缺），Poll 应拉入 2023-06 早期段。
func TestTxsCollector_Poll_R5GapBackfill(t *testing.T) {
	st := newTestStore(t)
	monitorAddr := "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"
	// 预插 1 条 2025-03 tx（模拟 429 断点后 lastTs 推进到 2025-03，早期段缺）
	if err := st.UpsertTRXTx(context.Background(), pgstore.TRXTx{
		TxID: "existing_2025", Ts: time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC),
		Kind: "deposit", AmountSun: 1000000,
	}); err != nil {
		t.Fatalf("UpsertTRXTx existing: %v", err)
	}
	// mock 返回 2 条：2025-03（已有）+ 2023-06（早期缺）
	earlyTs := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	existingTs := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	resp := `{"data":[` +
		`{"block_timestamp":` + itoa(existingTs) + `,"txID":"existing_2025","raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"owner_address":"TOther","to_address":"` + monitorAddr + `","amount":1000000}}}]}}` +
		`,` +
		`{"block_timestamp":` + itoa(earlyTs) + `,"txID":"early_2023","raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"owner_address":"TOther","to_address":"` + monitorAddr + `","amount":2000000}}}]}}` +
		`]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		// R5 锚：forceFullBackfill 时应带 min_timestamp=0
		if r.URL.Query().Get("min_timestamp") != "0" {
			t.Errorf("R5 gap backfill: expected min_timestamp=0, got %q", r.URL.Query().Get("min_timestamp"))
		}
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	c := NewTxsCollector(monitorAddr, st, srv.URL, "", nil)
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rows, err := st.ListTRXTxs(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListTRXTxs: %v", err)
	}
	// 应有 2 条：existing_2025 + early_2023（R5 缺口兜底拉入早期段）
	if len(rows) != 2 {
		t.Fatalf("R5 gap backfill: got %d txs, want 2 (existing + early)", len(rows))
	}
	hasEarly := false
	for _, r := range rows {
		if r.TxID == "early_2023" {
			hasEarly = true
		}
	}
	if !hasEarly {
		t.Errorf("R5 gap backfill: early_2023 (2023-06) 未拉入——缺口兜底未生效")
	}
}

// TestWitnessCollector_Poll_Success 验证见证人快照只存 isJobs 行。
func TestWitnessCollector_Poll_Success(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		switch r.URL.Path {
		case "/wallet/getaccount":
			_, _ = w.Write([]byte(mockGetAccountJSON))
		case "/wallet/listwitnesses":
			_, _ = w.Write([]byte(mockListWitnessesJSON))
		}
	}))
	defer srv.Close()

	c := NewWitnessCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, srv.URL, "")
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// listwitnesses 返回 2 条（1 isJobs + 1 非 isJobs），只存 isJobs 行
	ws, err := st.ListTRXWitnesses(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListTRXWitnesses: %v", err)
	}
	if len(ws) != 1 {
		t.Fatalf("got %d witnesses, want 1 (only isJobs)", len(ws))
	}
	if ws[0].URL == nil || *ws[0].URL != "https://okcoin.jp/" {
		t.Errorf("URL = %v, want https://okcoin.jp/", ws[0].URL)
	}
}

// TestClassifyTxRentalEnvHit 验证 TRX_RENTAL_PAYERS env 命中/未命中两分支。
func TestClassifyTxRentalEnvHit(t *testing.T) {
	rentalPayer := "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"
	monitorAddr := "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"
	rentalSet := map[string]bool{rentalPayer: true}

	// 转入+付款方命中 → rental_income（from=付款方, to=监控地址）
	tx1 := makeTransferTxJSON("tx1", monitorAddr, rentalPayer, 5320000)
	rec1, ok1 := classifyTx(tx1, monitorAddr, rentalSet)
	if !ok1 || rec1.Kind != "rental_income" {
		t.Errorf("rental hit: ok=%v kind=%q, want rental_income", ok1, rec1.Kind)
	}
	if rec1.AmountSun != 5320000 {
		t.Errorf("rental AmountSun = %d, want 5320000", rec1.AmountSun)
	}

	// 转入+未命中 → deposit
	tx2 := makeTransferTxJSON("tx2", monitorAddr, "TOther", 1000000)
	rec2, ok2 := classifyTx(tx2, monitorAddr, rentalSet)
	if !ok2 || rec2.Kind != "deposit" {
		t.Errorf("rental miss: ok=%v kind=%q, want deposit", ok2, rec2.Kind)
	}

	// 转出 → withdraw（from=监控地址, to=其他）
	tx3 := makeTransferTxJSON("tx3", "TOther", monitorAddr, 2000000)
	rec3, ok3 := classifyTx(tx3, monitorAddr, rentalSet)
	if !ok3 || rec3.Kind != "withdraw" {
		t.Errorf("outgoing: ok=%v kind=%q, want withdraw", ok3, rec3.Kind)
	}
}

// TestClassifyTxNonFundingNotStored P2-3：非资金移动合约不入库。
func TestClassifyTxNonFundingNotStored(t *testing.T) {
	for _, ct := range []string{"DelegateResourceContract", "UnDelegateResourceContract", "VoteWitnessContract"} {
		tx := makeContractTxJSON("tx_"+ct, ct)
		_, ok := classifyTx(tx, "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", nil)
		if ok {
			t.Errorf("%s: ok=true, want false (P2-3 不入库)", ct)
		}
	}
}

// TestClassifyTxVoteClaimAmountZero P2-2：vote_claim amount=0。
func TestClassifyTxVoteClaimAmountZero(t *testing.T) {
	tx := makeContractTxJSON("tx_vote", "WithdrawBalanceContract")
	rec, ok := classifyTx(tx, "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", nil)
	if !ok || rec.Kind != "vote_claim" {
		t.Fatalf("ok=%v kind=%q, want vote_claim", ok, rec.Kind)
	}
	if rec.AmountSun != 0 {
		t.Errorf("vote_claim AmountSun = %d, want 0 (P2-2)", rec.AmountSun)
	}
}

// ---------- 辅助函数 ----------

func makeTransferTxJSON(txID, to, from string, amount int64) txJSON {
	return txJSON{
		BlockTimestamp: time.Now().UnixMilli(),
		TxID:           txID,
		RawData: rawDataContractJSON{
			Contract: []contractEntryJSON{{
				Type: "TransferContract",
				Parameter: parameterJSON{
					Value: []byte(`{"owner_address":"` + from + `","to_address":"` + to + `","amount":` + itoa(amount) + `}`),
				},
			}},
		},
	}
}

func makeContractTxJSON(txID, contractType string) txJSON {
	return txJSON{
		BlockTimestamp: time.Now().UnixMilli(),
		TxID:           txID,
		RawData: rawDataContractJSON{
			Contract: []contractEntryJSON{{
				Type:      contractType,
				Parameter: parameterJSON{Value: []byte(`{}`)},
			}},
		},
	}
}

func makeTransferTx(txID, to, from string, amount int64) string {
	return `{"block_timestamp":` + itoa(time.Now().UnixMilli()) + `,"txID":"` + txID + `","raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"owner_address":"` + from + `","to_address":"` + to + `","amount":` + itoa(amount) + `}}}]}}`
}

func makeContractTx(txID, contractType string) string {
	return `{"block_timestamp":` + itoa(time.Now().UnixMilli()) + `,"txID":"` + txID + `","raw_data":{"contract":[{"type":"` + contractType + `","parameter":{"value":{}}}]}}`
}

func joinJSON(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func itoa(n int64) string {
	return time.Duration(n).String()[:0] + intToStr(n)
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func fmtTxID(i int) string {
	return "tx" + intToStr(int64(i))
}

// ---------- D-211 返修测试（R1-R3，fixture 逐字照抄附录真实样本形状） ----------

// 真实样本形状（curl 原文 2026-09-03 12:1x 复核）：txID + block_timestamp 下划线——原附录为复审方手写重建，两字段名均错，本 fixture 已对拍真身 + raw_data.contract[0].type
// + parameter.value.{amount,owner_address,to_address}。R2 修复后 visible=true → 地址 base58（T 前缀）。
// 本 fixture 照抄附录形状，地址用 base58（模拟 R2 后响应），验证字段名对拍 + 方向分类。
const appendixFixtureShape = `{"block_timestamp":1696936089000,"txID":"7c1fabcdef","raw_data":{"contract":[{"type":"TransferContract","parameter":{"value":{"amount":3000000,"owner_address":"TWsbXR6SPvvXPxUtPMQvEj7AMH55555555","to_address":"TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"}}}]}}`

// TestR1_FieldMappingAppendixSample R1：附录样本字段名对拍（blockTimestamp 驼峰能解析）。
func TestR1_FieldMappingAppendixSample(t *testing.T) {
	var resp txsResponseJSON
	if err := json.Unmarshal([]byte(`{"data":[`+appendixFixtureShape+`]}`), &resp); err != nil {
		t.Fatalf("unmarshal appendix fixture: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len = %d, want 1", len(resp.Data))
	}
	tx := resp.Data[0]
	// R1：block_timestamp 解析（真实字段名，防再漂移）
	if tx.BlockTimestamp != 1696936089000 {
		t.Errorf("BlockTimestamp = %d, want 1696936089000 (R1 驼峰对拍)", tx.BlockTimestamp)
	}
	if tx.TxID != "7c1fabcdef" {
		t.Errorf("TxID = %q, want 7c1fabcdef (transaction_id 对拍)", tx.TxID)
	}
	if tx.SmartContractType() != "TransferContract" {
		t.Errorf("contract type = %q, want TransferContract", tx.SmartContractType())
	}
	// R2：visible=true 后地址 base58，能正确读出
	if tx.TransferFrom() != "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555" {
		t.Errorf("owner_address = %q, want TWsb…555 (R2 base58)", tx.TransferFrom())
	}
	if tx.TransferTo() != "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555" {
		t.Errorf("to_address = %q, want TBtvnRB… (R2 base58)", tx.TransferTo())
	}
	if tx.TransferAmount() != 3000000 {
		t.Errorf("amount = %d, want 3000000", tx.TransferAmount())
	}
}

// TestR2_VisibleBase58_Classify51TransferContracts R2+R4：51 笔 TransferContract 分类（visible=true 后 base58 地址）。
// 照抄附录形状，51 笔不同方向：20 withdraw / 15 rental_income / 16 deposit。
func TestR2_VisibleBase58_Classify51TransferContracts(t *testing.T) {
	monitorAddr := "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555"
	rentalPayer := "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"
	otherAddr := "TOtherAddr111111111111111111111"
	rentalSet := map[string]bool{rentalPayer: true}

	wantWithdraw, wantRental, wantDeposit := 20, 15, 16
	gotWithdraw, gotRental, gotDeposit := 0, 0, 0
	for i := 0; i < 51; i++ {
		txID := "7c1f_" + intToStr(int64(i))
		var txJSONStr string
		switch {
		case i < wantWithdraw:
			// owner=monitor → withdraw
			txJSONStr = makeTransferTx(txID, otherAddr, monitorAddr, 1000000)
		case i < wantWithdraw+wantRental:
			// to=monitor + from=rentalPayer → rental_income
			txJSONStr = makeTransferTx(txID, monitorAddr, rentalPayer, 5320000)
		default:
			// to=monitor + from=other → deposit
			txJSONStr = makeTransferTx(txID, monitorAddr, otherAddr, 2000000)
		}
		var resp txsResponseJSON
		if err := json.Unmarshal([]byte(`{"data":[`+txJSONStr+`]}`), &resp); err != nil {
			t.Fatalf("unmarshal tx %d: %v", i, err)
		}
		rec, ok := classifyTx(resp.Data[0], monitorAddr, rentalSet)
		if !ok {
			t.Fatalf("tx %d: ok=false, want true (TransferContract 入库)", i)
		}
		switch rec.Kind {
		case "withdraw":
			gotWithdraw++
		case "rental_income":
			gotRental++
		case "deposit":
			gotDeposit++
		default:
			t.Errorf("tx %d: kind = %q, want withdraw/rental_income/deposit", i, rec.Kind)
		}
	}
	if gotWithdraw != wantWithdraw {
		t.Errorf("withdraw = %d, want %d", gotWithdraw, wantWithdraw)
	}
	if gotRental != wantRental {
		t.Errorf("rental_income = %d, want %d", gotRental, wantRental)
	}
	if gotDeposit != wantDeposit {
		t.Errorf("deposit = %d, want %d", gotDeposit, wantDeposit)
	}
}

// TestR3_UpsertTRXTxEmptyTxIDLoudFail R3：空 tx_id 拒绝（loud fail，不静默坍缩成一行）。
func TestR3_UpsertTRXTxEmptyTxIDLoudFail(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	err := st.UpsertTRXTx(ctx, pgstore.TRXTx{TxID: "", Kind: "other"})
	if err == nil {
		t.Fatal("UpsertTRXTx(empty tx_id) should fail (R3 loud fail)")
	}
	if !strings.Contains(err.Error(), "empty tx_id") {
		t.Errorf("err = %q, want contains 'empty tx_id'", err.Error())
	}
	// 非空 tx_id 正常写入
	if err := st.UpsertTRXTx(ctx, pgstore.TRXTx{TxID: "valid_tx_1", Kind: "other", Ts: time.Now()}); err != nil {
		t.Fatalf("UpsertTRXTx(valid): %v", err)
	}
}
