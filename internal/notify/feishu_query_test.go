// D-284 S6：飞书自然语言查询测试（10 用例）。
// 纯逻辑测试：fake store + httptest LLM SSE + httptest 飞书 API。
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/analyst"
	"arb/internal/store/pgstore"
)

// --- fakes ---

type fakeQueryStore struct {
	chatID, openID string
	trxAcc         TRXAccountSummary
	cmcPrice       float64
	cmcPriceTs     time.Time
	cmcSum         CMCAccountSummary
	cmcPos         CMCPositionSummary
	today          [3]float64
	macroEvents    []pgstore.MacroEvent // D-528
	// D-168 ack 字段
	epsAckN      int
	epByMsg      map[string]string
	alertAckN    int
	ackAllCalled bool
	ackByKeySeen string
	alertAckAll  bool
}

func (s *fakeQueryStore) FeishuTarget(context.Context) (string, string, error) {
	return s.chatID, s.openID, nil
}
func (s *fakeQueryStore) LastTRXAccount(context.Context) (TRXAccountSummary, error) {
	return s.trxAcc, nil
}
func (s *fakeQueryStore) LastCMCPriceMid(context.Context, string) (float64, time.Time, error) {
	return s.cmcPrice, s.cmcPriceTs, nil
}
func (s *fakeQueryStore) LastCMCAccountSummary(context.Context) (CMCAccountSummary, error) {
	return s.cmcSum, nil
}
func (s *fakeQueryStore) LastCMCPosition(context.Context, string) (CMCPositionSummary, error) {
	return s.cmcPos, nil
}
func (s *fakeQueryStore) SumTodayIncome(context.Context) (float64, float64, float64, error) {
	return s.today[0], s.today[1], s.today[2], nil
}
func (s *fakeQueryStore) SumTRXTxRentalAll(context.Context) (float64, error)    { return 0, nil }
func (s *fakeQueryStore) SumTRXTxVoteClaimAll(context.Context) (float64, error) { return 0, nil }

// D-168 ack 拦截 fake 面。
func (s *fakeQueryStore) AckAllAlertEpisodes(context.Context) (int, error) {
	s.ackAllCalled = true
	return s.epsAckN, nil
}
func (s *fakeQueryStore) AckAlertEpisodeByPushMsg(_ context.Context, msgID string) (string, bool, error) {
	k, ok := s.epByMsg[msgID]
	return k, ok, nil
}
func (s *fakeQueryStore) AckUnackedAlertsByKey(_ context.Context, key string) (int, error) {
	s.ackByKeySeen = key
	return s.alertAckN, nil
}
func (s *fakeQueryStore) AckAll(context.Context) error {
	s.alertAckAll = true
	return nil
}

type fakeAISettings struct{ enabled bool }

func (f *fakeAISettings) GetAiSettings(context.Context) (*pgstore.AiSettings, error) {
	return &pgstore.AiSettings{Enabled: f.enabled}, nil
}

// feishuMockServer 模拟飞书 API（token + messages），返回捕获消息的 channel。
func feishuMockServer(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	msgCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/auth/v3/tenant_access_token") {
			w.Write([]byte(`{"code":0,"tenant_access_token":"test","expire":3600}`))
			return
		}
		if strings.Contains(r.URL.Path, "/im/v1/messages") {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Content string `json:"content"`
			}
			json.Unmarshal(body, &req)
			var c struct {
				Text string `json:"text"`
			}
			json.Unmarshal([]byte(req.Content), &c)
			select {
			case msgCh <- c.Text:
			default:
			}
			w.Write([]byte(`{"code":0,"data":{"message_id":"om_test_1"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, msgCh
}

// llmToolTextServer 模拟 LLM：第一轮 tool_use，第二轮 text_delta + end_turn。
func llmToolTextServer(t *testing.T, toolName, finalText string) *httptest.Server {
	t.Helper()
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&count, 1) == 1 {
			fmt.Fprintf(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":%q}}\n\n", toolName)
			fl.Flush()
			fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n")
			fl.Flush()
			fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fl.Flush()
			fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n")
			fl.Flush()
			fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
			fl.Flush()
		} else {
			fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fl.Flush()
			fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", finalText)
			fl.Flush()
			fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fl.Flush()
			fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
			fl.Flush()
			fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestHandler 构造测试用 handler（feishuBase 已指向 mock）。
func newTestHandler(store FeishuQueryStore, llmURL string, queryTimeout time.Duration) *FeishuQueryHandler {
	h := NewFeishuQueryHandler(store, &fakeAISettings{enabled: true}, NewFeishuClient("app", "secret"),
		llmURL, "test-model", "test-key", analyst.NewRateLimiter(0))
	h.queryTimeout = queryTimeout
	return h
}

func textEvent(text string) ReplyEvent {
	content, _ := json.Marshal(map[string]string{"text": text})
	return ReplyEvent{ChatID: "chat1", SenderOpenID: "user1", MessageType: "text", Content: string(content)}
}

// waitForMsg 等待异步 SendText 结果，2s 超时。
func waitForMsg(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for feishu message")
		return ""
	}
}

// --- 10 用例 ---

func TestQuery_TRXPrice(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1", cmcPrice: 0.33585, cmcPriceTs: time.Now()}
	feishuSrv, msgCh := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	llmSrv := llmToolTextServer(t, "query_trx_price", "TRX 当前价格 0.33585 USDT")
	h := newTestHandler(fs, llmSrv.URL, 10*time.Second)
	reply, err := h.HandleReply(context.Background(), textEvent("TRX多少钱"))
	if err != nil || reply != "" {
		t.Fatalf("HandleReply: reply=%q err=%v", reply, err)
	}
	msg := waitForMsg(t, msgCh)
	if !strings.Contains(msg, "0.33585") {
		t.Errorf("msg=%q, want contains 0.33585", msg)
	}
}

func TestQuery_PendingReward(t *testing.T) {
	fs := &fakeQueryStore{
		chatID: "chat1", openID: "user1",
		trxAcc: TRXAccountSummary{AllowanceTRX: 0, PendingRewardTRX: 7.236, BalanceTRX: 1000, TronPowerTRX: 107332, VotesTotal: 78714},
	}
	feishuSrv, msgCh := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	llmSrv := llmToolTextServer(t, "query_trx_account", "待领取 7.236 TRX")
	h := newTestHandler(fs, llmSrv.URL, 10*time.Second)
	h.HandleReply(context.Background(), textEvent("投票奖励多少未提"))
	msg := waitForMsg(t, msgCh)
	if !strings.Contains(msg, "7.23") {
		t.Errorf("msg=%q, want contains ~7.23", msg)
	}
}

func TestQuery_CMCPosition(t *testing.T) {
	fs := &fakeQueryStore{
		chatID: "chat1", openID: "user1",
		cmcPos: CMCPositionSummary{InstrumentID: "X-AFQUL", Direction: "short", Quantity: 18100, EntryPrice: 0.33875, UnrealisedPnL: -123.45},
		cmcSum: CMCAccountSummary{CloseoutPrice: 0.45, DistanceToCloseoutPct: 25.5},
	}
	feishuSrv, msgCh := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	llmSrv := llmToolTextServer(t, "query_cmc_position", "CMC 空单 18100 TRX")
	h := newTestHandler(fs, llmSrv.URL, 10*time.Second)
	h.HandleReply(context.Background(), textEvent("空单持仓"))
	msg := waitForMsg(t, msgCh)
	if !strings.Contains(msg, "18100") {
		t.Errorf("msg=%q, want contains 18100", msg)
	}
}

func TestQuery_TodayIncome(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1", today: [3]float64{1.23, 4.56, 7.89}}
	feishuSrv, msgCh := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	llmSrv := llmToolTextServer(t, "query_today_income", "今日收益 13.68 USD")
	h := newTestHandler(fs, llmSrv.URL, 10*time.Second)
	h.HandleReply(context.Background(), textEvent("今日收益"))
	msg := waitForMsg(t, msgCh)
	if !strings.Contains(msg, "13.68") {
		t.Errorf("msg=%q, want contains 13.68", msg)
	}
}

func TestQuery_LLMError(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1"}
	feishuSrv, msgCh := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	t.Cleanup(llmSrv.Close)
	h := newTestHandler(fs, llmSrv.URL, 10*time.Second)
	h.HandleReply(context.Background(), textEvent("TRX价格"))
	msg := waitForMsg(t, msgCh)
	if !strings.Contains(msg, "查询失败") {
		t.Errorf("msg=%q, want contains 查询失败", msg)
	}
}

func TestQuery_LLMTimeout(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1"}
	feishuSrv, msgCh := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	t.Cleanup(llmSrv.Close)
	h := newTestHandler(fs, llmSrv.URL, 500*time.Millisecond)
	h.HandleReply(context.Background(), textEvent("TRX价格"))
	msg := waitForMsg(t, msgCh)
	if !strings.Contains(msg, "查询超时") {
		t.Errorf("msg=%q, want contains 查询超时", msg)
	}
}

func TestQuery_UnauthorizedSender(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "owner"}
	h := newTestHandler(fs, "http://unused", 10*time.Second)
	ev := textEvent("TRX价格")
	ev.SenderOpenID = "intruder"
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if reply != "非授权发送者" {
		t.Errorf("reply=%q, want 非授权发送者", reply)
	}
}

func TestQuery_RateLimited(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1"}
	h := NewFeishuQueryHandler(fs, &fakeAISettings{enabled: true}, NewFeishuClient("app", "secret"),
		"http://unused", "m", "k", analyst.NewRateLimiter(1))
	h.queryTimeout = 10 * time.Second
	h.rateLim.Allow() // 耗尽配额
	reply, err := h.HandleReply(context.Background(), textEvent("TRX价格"))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(reply, "已达每小时查询上限") {
		t.Errorf("reply=%q, want contains 已达每小时查询上限", reply)
	}
}
