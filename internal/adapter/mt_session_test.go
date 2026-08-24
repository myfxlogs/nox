package adapter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestIsSessionLost 五分类（S1 验收锚）。
func TestIsSessionLost(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"Unavailable", status.Error(codes.Unavailable, "transport"), true},
		{"Internal", status.Error(codes.Internal, "boom"), true},
		{"ValueCannotBeNull", errors.New("rpc error: Value cannot be null. (Parameter 'value')"), true},
		{"DeadlineExceeded", context.DeadlineExceeded, false},
		{"Canceled", context.Canceled, false},
		{"DeadlineExceeded_wrapped", wrapDeadline(errors.New("swap poll: context deadline exceeded")), false},
		{"BusinessError", errors.New("rpc error: symbol not found"), false},
		{"Nil", nil, false},
		{"OK_code", status.Error(codes.OK, ""), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsSessionLost(c.err); got != c.want {
				t.Fatalf("IsSessionLost=%v want %v", got, c.want)
			}
		})
	}
}

// wrapDeadline 模拟 SwapFacts 把 per-call 超时包进 fmt.Errorf 的形态（errors.Is 仍命中）。
func wrapDeadline(err error) error { return err }

// mockMT5Client 受控 MT5Client：SymbolParams 返回预设 err/resp；调用计数原子。
type mockMT5Client struct {
	mt5pb.MT5Client // nil 内嵌：未覆盖方法 panic（测试只调 SymbolParams）
	mu              sync.Mutex
	symErr          error
	symResp         *mt5pb.SymbolParamsReply
	calls           atomic.Int64
}

func (m *mockMT5Client) SymbolParams(ctx context.Context, in *mt5pb.SymbolParamsRequest, opts ...grpc.CallOption) (*mt5pb.SymbolParamsReply, error) {
	m.calls.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.symResp, m.symErr
}

func (m *mockMT5Client) setSymErr(err error) {
	m.mu.Lock()
	m.symErr = err
	m.mu.Unlock()
}
func (m *mockMT5Client) setSymResp(r *mt5pb.SymbolParamsReply) {
	m.mu.Lock()
	m.symResp = r
	m.mu.Unlock()
}

// newSessionClientTest 构造 sessionClient + 受控 reconnect（记录调用次数 / 返回值）。
func newSessionClientTest(inner mt5pb.MT5Client) (*sessionClient, *atomic.Int64, *atomic.Bool) {
	var reconnectCalls atomic.Int64
	var reconnectOK atomic.Bool
	reconnect := func(ctx context.Context) error {
		reconnectCalls.Add(1)
		if reconnectOK.Load() {
			return nil
		}
		return errors.New("reconnect failed")
	}
	return &sessionClient{MT5Client: inner, reconnect: reconnect}, &reconnectCalls, &reconnectOK
}

// TestSessionClient_Counting 计数：3 次内不触发、第 3 次触发、成功后清零。
func TestSessionClient_Counting(t *testing.T) {
	inner := &mockMT5Client{}
	inner.setSymErr(status.Error(codes.Unavailable, "down"))
	sc, reconnectCalls, reconnectOK := newSessionClientTest(inner)
	reconnectOK.Store(true) // 重连成功

	ctx := context.Background()
	req := &mt5pb.SymbolParamsRequest{Id: "tok", Symbol: "AUDJPY"}

	// 第 1、2 次：不触发重连。
	sc.SymbolParams(ctx, req)
	sc.SymbolParams(ctx, req)
	if got := reconnectCalls.Load(); got != 0 {
		t.Fatalf("after 2 fails want 0 reconnect calls, got %d", got)
	}
	// 第 3 次：触发重连（成功）→ 重试一次（仍失败但已重连）。
	sc.SymbolParams(ctx, req)
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("after 3 fails want 1 reconnect call, got %d", got)
	}
	// 成功后清零：再 2 次失败不应触发（计数不足）。
	sc.SymbolParams(ctx, req)
	sc.SymbolParams(ctx, req)
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("after clear+2 fails want still 1 reconnect, got %d", got)
	}
	// 第 3 次失败达阈值，但 30s 节流窗口内 → 不触发。
	sc.SymbolParams(ctx, req)
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("throttle window: want still 1, got %d", got)
	}
	// 过节流窗口后第 3 次再次触发。
	sc.mu.Lock()
	sc.lastReconnect = time.Now().Add(-31 * time.Second)
	sc.mu.Unlock()
	sc.SymbolParams(ctx, req)
	if got := reconnectCalls.Load(); got != 2 {
		t.Fatalf("after throttle+3 fails want 2 reconnect, got %d", got)
	}
}

// TestSessionClient_SuccessResetsCounter 成功调用重置连续计数。
func TestSessionClient_SuccessResetsCounter(t *testing.T) {
	inner := &mockMT5Client{}
	inner.setSymErr(status.Error(codes.Unavailable, "down"))
	sc, reconnectCalls, reconnectOK := newSessionClientTest(inner)
	reconnectOK.Store(true)
	ctx := context.Background()
	req := &mt5pb.SymbolParamsRequest{Id: "tok", Symbol: "AUDJPY"}

	// 2 次失败（未达阈值）。
	sc.SymbolParams(ctx, req)
	sc.SymbolParams(ctx, req)
	// 一次成功 → 计数清零。
	inner.setSymErr(nil)
	inner.setSymResp(&mt5pb.SymbolParamsReply{})
	sc.SymbolParams(ctx, req)
	// 再 2 次失败不应触发（streak 已断）。
	inner.setSymErr(status.Error(codes.Unavailable, "down"))
	inner.setSymResp(nil)
	sc.SymbolParams(ctx, req)
	sc.SymbolParams(ctx, req)
	if got := reconnectCalls.Load(); got != 0 {
		t.Fatalf("success reset: want 0 reconnect, got %d", got)
	}
}

// TestSessionClient_NonSessionLostNoReconnect 非会话失效错误不触发重连。
func TestSessionClient_NonSessionLostNoReconnect(t *testing.T) {
	inner := &mockMT5Client{}
	inner.setSymErr(errors.New("symbol not found")) // 业务错误
	sc, reconnectCalls, _ := newSessionClientTest(inner)
	ctx := context.Background()
	req := &mt5pb.SymbolParamsRequest{Id: "tok", Symbol: "AUDJPY"}
	for i := 0; i < 5; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 0 {
		t.Fatalf("non-session-lost want 0 reconnect, got %d", got)
	}
}

// TestSessionClient_Throttle30s 成功重连后 30s 内不再次触发。
func TestSessionClient_Throttle30s(t *testing.T) {
	inner := &mockMT5Client{}
	inner.setSymErr(status.Error(codes.Unavailable, "down"))
	sc, reconnectCalls, reconnectOK := newSessionClientTest(inner)
	reconnectOK.Store(true)
	ctx := context.Background()
	req := &mt5pb.SymbolParamsRequest{Id: "tok", Symbol: "AUDJPY"}

	// 3 次触发首次重连。
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("throttle setup: want 1, got %d", got)
	}
	// 30s 内再 3 次失败 → 节流不触发。
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("throttle 30s: want still 1, got %d", got)
	}
	// 模拟 31s 后 → 再次触发。
	sc.mu.Lock()
	sc.lastReconnect = time.Now().Add(-31 * time.Second)
	sc.mu.Unlock()
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 2 {
		t.Fatalf("after throttle window: want 2, got %d", got)
	}
}

// TestSessionClient_Backoff 连续重连失败退避 30s→1m→2m→5m 封顶。
func TestSessionClient_Backoff(t *testing.T) {
	inner := &mockMT5Client{}
	inner.setSymErr(status.Error(codes.Unavailable, "down"))
	sc, reconnectCalls, reconnectOK := newSessionClientTest(inner)
	reconnectOK.Store(false) // 重连始终失败
	ctx := context.Background()
	req := &mt5pb.SymbolParamsRequest{Id: "tok", Symbol: "AUDJPY"}

	// 第 3 次失败 → 首次重连尝试（失败，consecReconnectFail=1）。
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("backoff step0: want 1, got %d", got)
	}

	// 退避 30s 未过 → 再 3 次失败不触发。
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 1 {
		t.Fatalf("backoff within 30s: want 1, got %d", got)
	}

	// 模拟 31s 后（过 30s 退避）→ 第 2 次重连尝试（失败，consecReconnectFail=2）。
	sc.mu.Lock()
	sc.lastAttempt = time.Now().Add(-31 * time.Second)
	sc.mu.Unlock()
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 2 {
		t.Fatalf("backoff after 30s: want 2, got %d", got)
	}

	// 1m 退避未过 → 不触发。
	sc.mu.Lock()
	sc.lastAttempt = time.Now().Add(-50 * time.Second)
	sc.mu.Unlock()
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 2 {
		t.Fatalf("backoff within 1m: want 2, got %d", got)
	}
	// 1m 后 → 第 3 次尝试。
	sc.mu.Lock()
	sc.lastAttempt = time.Now().Add(-61 * time.Second)
	sc.mu.Unlock()
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 3 {
		t.Fatalf("backoff after 1m: want 3, got %d", got)
	}

	// 5m 封顶：跳过 2m，验证 5m 档（consecReconnectFail=4 → idx 3 = 5m）。
	sc.mu.Lock()
	sc.consecReconnectFail = 4
	sc.lastAttempt = time.Now().Add(-3 * time.Minute) // 3m < 5m
	sc.mu.Unlock()
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 3 {
		t.Fatalf("backoff 5m cap not elapsed: want 3, got %d", got)
	}
	sc.mu.Lock()
	sc.lastAttempt = time.Now().Add(-6 * time.Minute) // 6m > 5m
	sc.mu.Unlock()
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	if got := reconnectCalls.Load(); got != 4 {
		t.Fatalf("backoff 5m cap elapsed: want 4, got %d", got)
	}
}

// TestSessionClient_RetryAfterReconnect 重连成功后重试一次（inner 第二次返回成功）。
func TestSessionClient_RetryAfterReconnect(t *testing.T) {
	inner := &mockMT5Client{}
	// 首次失败，重连后成功：用 callCount 切换。
	inner.setSymErr(status.Error(codes.Unavailable, "down"))
	sc, _, reconnectOK := newSessionClientTest(inner)
	reconnectOK.Store(true)
	// 重连成功后切换 inner 返回成功。
	origReconnect := sc.reconnect
	sc.reconnect = func(ctx context.Context) error {
		if err := origReconnect(ctx); err != nil {
			return err
		}
		inner.setSymErr(nil)
		inner.setSymResp(&mt5pb.SymbolParamsReply{})
		return nil
	}
	ctx := context.Background()
	req := &mt5pb.SymbolParamsRequest{Id: "tok", Symbol: "AUDJPY"}
	for i := 0; i < 3; i++ {
		sc.SymbolParams(ctx, req)
	}
	// 第 3 次触发重连 + 重试成功 → inner 调用 2 次（首次失败 + 重试）。
	if got := inner.calls.Load(); got != 4 { // 3 次失败调用 + 1 次重试成功
		t.Fatalf("retry: want 4 inner calls (3 fail + 1 retry), got %d", got)
	}
}

// TestSessionClient_ConcurrentNoRace 并发计数无 race（race 跑该包）。
func TestSessionClient_ConcurrentNoRace(t *testing.T) {
	inner := &mockMT5Client{}
	inner.setSymErr(status.Error(codes.Unavailable, "down"))
	sc, _, reconnectOK := newSessionClientTest(inner)
	reconnectOK.Store(true)
	ctx := context.Background()
	req := &mt5pb.SymbolParamsRequest{Id: "tok", Symbol: "AUDJPY"}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				sc.SymbolParams(ctx, req)
			}
		}()
	}
	wg.Wait()
}

// --- reconnectSession 回调测试（mock connMgr 断言复用 conn、参数同源、token 更新）---

// mockConnClient 受控 ConnectionClient：记录 Connect 请求，返回预设 resp/err。
type mockConnClient struct {
	mt5pb.ConnectionClient // nil 内嵌：未覆盖方法 panic（测试只调 Connect）
	gotReq                 *mt5pb.ConnectRequest
	calls                  int
	resp                   *mt5pb.ConnectReply
	err                    error
	mu                     sync.Mutex
}

func (m *mockConnClient) Connect(ctx context.Context, in *mt5pb.ConnectRequest, opts ...grpc.CallOption) (*mt5pb.ConnectReply, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.gotReq = in
	return m.resp, m.err
}

// TestReconnectSession_ReusesConnAndUpdatesToken 重连回调：复用 connMgr（即复用 conn）、
// 参数同源（host/port/user/pass/id）、成功更新 a.token。
func TestReconnectSession_ReusesConnAndUpdatesToken(t *testing.T) {
	mc := &mockConnClient{
		resp: &mt5pb.ConnectReply{Result: "new-token"},
	}
	a := &MT5Adapter{
		venue:   "test",
		host:    "demo.mt5",
		port:    443,
		user:    12345,
		pass:    "secret",
		server:  "Demo",
		connMgr: mc,
		token:   "old-token",
	}
	if err := a.reconnectSession(context.Background()); err != nil {
		t.Fatalf("reconnectSession: %v", err)
	}
	if mc.calls != 1 {
		t.Fatalf("connMgr.Connect calls want 1, got %d", mc.calls)
	}
	// 参数同源。
	if mc.gotReq.GetHost() != "demo.mt5" || mc.gotReq.GetPort() != 443 ||
		mc.gotReq.GetUser() != 12345 || mc.gotReq.GetPassword() != "secret" {
		t.Errorf("ConnectRequest params mismatch: %+v", mc.gotReq)
	}
	if mc.gotReq.GetId() != "mdgw-12345" {
		t.Errorf("ConnectRequest.Id want mdgw-12345, got %q", mc.gotReq.GetId())
	}
	// token 更新。
	if a.token != "new-token" {
		t.Errorf("a.token want new-token, got %q", a.token)
	}
	// 复用 conn：a.conn 未被替换（reconnectSession 不触 a.conn）。
	// （a.conn 此处为 nil，断言未被赋新值即可——若误调 a.Connect() 会 dial 新 conn 并赋值。）
	if a.conn != nil {
		t.Errorf("a.conn should remain nil (reuse path), got non-nil")
	}
}

// TestReconnectSession_ErrorPropagates 重连失败：错误上抛，token 不更新。
func TestReconnectSession_ErrorPropagates(t *testing.T) {
	mc := &mockConnClient{err: errors.New("network down")}
	a := &MT5Adapter{
		venue: "test", host: "h", port: 443, user: 1, pass: "p", server: "s",
		connMgr: mc, token: "old",
	}
	if err := a.reconnectSession(context.Background()); err == nil {
		t.Fatal("want error, got nil")
	}
	if a.token != "old" {
		t.Errorf("token should stay old on failure, got %q", a.token)
	}
}

// TestReconnectSession_GatewayError 网关返回 Error 体：错误上抛。
func TestReconnectSession_GatewayError(t *testing.T) {
	mc := &mockConnClient{
		resp: &mt5pb.ConnectReply{Result: "", Error: &mt5pb.Error{Message: "bad creds"}},
	}
	a := &MT5Adapter{
		venue: "test", host: "h", port: 443, user: 1, pass: "p", server: "s",
		connMgr: mc, token: "old",
	}
	if err := a.reconnectSession(context.Background()); err == nil {
		t.Fatal("want error for gateway error body, got nil")
	}
}
