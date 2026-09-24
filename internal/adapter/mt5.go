package adapter

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/safego"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// MT5Adapter 通过 mtapi.io gRPC 网关连接 MT5 经纪商（§3.2）。
// 与 MT4 严格隔离（独立 proto、独立生成代码、独立类）。
type MT5Adapter struct {
	venue  string
	host   string
	port   int32
	user   int64
	pass   string
	server string

	conn    *grpc.ClientConn
	connMgr mt5pb.ConnectionClient
	mt5     mt5pb.MT5Client
	subs    mt5pb.SubscriptionsClient
	streams mt5pb.StreamsClient
	trading mt5pb.TradingClient
	token   string

	// D-104 P2 U1：网关节点启动级 failover（默认值=现硬编码，SetGateway 覆写）。
	gateway          string
	gatewayFallbacks []string

	symMap atomic.Pointer[map[string]string] // brokerSymbol → canonical
	// emitBlocked D-383 A2.3：canonical 标签发射抑制集（多行组非 primary 行；native 照发）。
	emitBlocked atomic.Pointer[map[string]bool]

	// MarginSink margin fact 下沉（D-099 事件驱动化）：由 main 经 BrokerManager 注入
	// （SwapKick 同模式：wiring/main 注入，brokerMgr 只存字段不依赖 engine 包）。
	// nil = 未注入（测试/未接线），runMarginSource 安全 no-op。
	MarginSink func(fact.Fact)

	// OrderEventSink 订单事件下沉（D-107 P1 事件驱动二阶段）：由 main 经 BrokerManager 注入
	// （MarginSink 同模式）。nil = 未注入（测试/未接线），runOrderEventSource 安全 no-op。
	OrderEventSink func(bus.OrderEvent)

	// AlertSink 断流分级告警下沉（D-104 P2 U2，MarginSink 同模式三点注入链）。nil = 未注入（no-op）。
	AlertSink func(level, msg string)

	// D-100：订阅集镜像（mtapi 会话订阅状态在网关侧，adapter 必须镜像才能 OnQuote 流
	// 重连后恢复订阅）。subMu 保护并发 Resubscribe 与流重连的 resubscribeAll。
	subMu  sync.Mutex
	subSet map[string]bool

	// D-100：OnQuote 流重连退避档位（成功消费一帧后重置）；复用 marginReconnectBackoff 序列。
	quoteReconIdx int
	// D-104 P2 U2：封顶档 episode 告警去重（成功帧重置，连续封顶只告警一次）。
	quoteAlerted bool

	// D-432：OnQuote 零帧看门狗状态（zfMu 保护；consumeQuoteFrames 写帧时间，
	// zeroframeWatchdog 读判定+stage-2 取消当前流）。episode=一次连续无真帧区间，
	// 真实帧到达即清零（zfAlerted/zfReconnectAt/zfPersistWarned 全复位）。
	zfMu              sync.Mutex
	zfLastAnyFrameAt  time.Time          // 最后一次 Recv 成功（含心跳空帧）
	zfLastRealFrameAt time.Time          // 最后一次真实报价帧（symbol 非空且 bid/ask 非零）
	zfStartedAt       time.Time          // 看门狗基线（lastReal 为零时 realAge 的起点）
	zfAlerted         bool               // stage-1 episode 告警去重
	zfReconnectAt     time.Time          // stage-2 单发标记（非零=本 episode 已用强制重连）
	zfPersistWarned   bool               // stage-2 后仍死的 warn 去重
	zfCancelStream    context.CancelFunc // 当前流取消器（stage-2 强制重连用）

	stopCtx    context.Context
	stopCancel context.CancelFunc
	mu         sync.Mutex
}

// NewMT5Adapter 创建 MT5 适配器。
func NewMT5Adapter(venue, host, server string, port int32, user int64, pass string) *MT5Adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &MT5Adapter{
		venue:            venue,
		host:             host,
		port:             port,
		user:             user,
		pass:             pass,
		server:           server,
		gateway:          "mt5grpc3.mtapi.io:443",
		gatewayFallbacks: []string{"mt5grpc1.mtapi.io:443", "mt5grpc4.mtapi.io:443", "mt5grpc2.mtapi.io:443"},
		stopCtx:          ctx,
		stopCancel:       cancel,
	}
}

// SetGateway 覆写网关与 fallback 列表（D-104 P2 U1，Connect 前由 brokerMgr 调用）。
func (a *MT5Adapter) SetGateway(g string, fb []string) {
	a.gateway = g
	a.gatewayFallbacks = fb
}

// SetSymbolMap 设置 brokerSymbol → canonical 映射。
func (a *MT5Adapter) SetSymbolMap(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	a.symMap.Store(&cp)
}

// SetCanonicalEmitBlocked D-383 A2.3：设置 canonical 标签发射抑制集（多行组非 primary 行）。
// 抑制行 native/variant fact 照发（D-325 语义保留），仅 canonical 标签停发；MT4 无 native
// → 抑制行整行跳过（canonical 即唯一值）。
func (a *MT5Adapter) SetCanonicalEmitBlocked(blocked map[string]bool) {
	cp := make(map[string]bool, len(blocked))
	for k, v := range blocked {
		cp[k] = v
	}
	a.emitBlocked.Store(&cp)
}

// emitBlockedSet 原子快照（SwapFacts 单次读取，与 SetSymbolMap 同并发风格）。
func (a *MT5Adapter) emitBlockedSet() map[string]bool {
	if p := a.emitBlocked.Load(); p != nil {
		return *p
	}
	return nil
}

func (a *MT5Adapter) canonical(brokerSym string) string {
	if m := a.symMap.Load(); m != nil {
		if c, ok := (*m)[brokerSym]; ok {
			return c
		}
	}
	return brokerSym
}

// Connect 连接 mtapi 网关并认证。
func (a *MT5Adapter) Connect(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	targets := append([]string{a.gateway}, a.gatewayFallbacks...)
	conn, err := dialGateways(ctx, "mt5", a.venue, targets)
	if err != nil {
		return err
	}
	a.conn = conn
	a.connMgr = mt5pb.NewConnectionClient(conn)
	a.mt5 = mt5pb.NewMT5Client(conn)
	a.subs = mt5pb.NewSubscriptionsClient(conn)
	a.streams = mt5pb.NewStreamsClient(conn)
	a.trading = mt5pb.NewTradingClient(conn)

	// mtapi 网关按 id header 路由（归档实现一致）；Connect 用临时会话 id。
	sid := sessionID(a.user)
	loginCtx := metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"id": sid}))
	resp, err := a.connMgr.Connect(loginCtx, &mt5pb.ConnectRequest{
		Host:     a.host,
		Port:     a.port,
		User:     uint64(a.user),
		Password: a.pass,
		Id:       &sid,
	})
	if err != nil {
		return fmt.Errorf("mt5 connect: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return fmt.Errorf("mt5 connect: %s", e.GetMessage())
	}
	token := resp.GetResult()
	if token == "" {
		return fmt.Errorf("mt5 connect: empty token")
	}
	a.token = token
	// S1：包装 mt5 client 加会话失效自动重连（reconcile.MT5Querier 共享同一 client，自动受益）。
	a.mt5 = &sessionClient{MT5Client: a.mt5, reconnect: a.reconnectSession}
	log.Printf("[mt5:%s] connected, token=%s", a.venue, maskToken(token))
	// D-099：attach 完成 → 启动 margin 事件驱动采集（MarginSink==nil 时 no-op）。
	// stopCtx 取消时 Run 自然退出（Stop 调 stopCancel）。a.mu 仍持有：goroutine 异步启动，
	// 其 a.mu 操作阻塞至 Connect 返回释放锁，无死锁。
	safego.Go("mt5:marginSource", func() { a.runMarginSource(a.stopCtx) })
	// D-107 P1：attach 完成 → 启动订单事件流消费（OrderEventSink==nil 时 no-op）。
	safego.Go("mt5:orderEventSource", func() { a.runOrderEventSource(a.stopCtx) })
	return nil
}

// reconnectSession 复用现有 conn 重调 connMgr.Connect 重建 account 会话（S1）。
// 禁止调用 a.Connect()——它每次 grpc.DialContext 新建 conn（泄漏）且替换 a.mt5（破坏包装器挂接）。
// 成功仅更新 a.token；sid 由 sessionID(a.user) 确定性生成（网关同 id → 同 token，重连后旧 ctx 仍有效）。
func (a *MT5Adapter) reconnectSession(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sid := sessionID(a.user)
	loginCtx := metadata.NewOutgoingContext(rctx, metadata.New(map[string]string{"id": sid}))
	resp, err := a.connMgr.Connect(loginCtx, &mt5pb.ConnectRequest{
		Host:     a.host,
		Port:     a.port,
		User:     uint64(a.user),
		Password: a.pass,
		Id:       &sid,
	})
	if err != nil {
		return fmt.Errorf("mt5 reconnect: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return fmt.Errorf("mt5 reconnect: %s", e.GetMessage())
	}
	token := resp.GetResult()
	if token == "" {
		return fmt.Errorf("mt5 reconnect: empty token")
	}
	a.token = token
	log.Printf("[mt5:%s] session reconnected, token=%s", a.venue, maskToken(token))
	return nil
}

// getToken 持锁读 token（reconnectSession 持 a.mu 写，无锁读=CI 实抓数据竞争）。
func (a *MT5Adapter) getToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}

// withSessionMD 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func (a *MT5Adapter) withSessionMD(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"id": a.getToken()}))
}

// Subscribe 实现 collect.QuoteSource。
// D-100：OnQuote 流加重连——抽 runQuoteStream/consumeQuoteFrames，断流后退避重开 +
// 重订阅（订阅集镜像 subSet）。ch 仅在 ctx 取消时关闭（重连不关 ch，避免下游断流）。
func (a *MT5Adapter) Subscribe(symbols []string) (<-chan bus.Quote, func()) {
	ch := make(chan bus.Quote, 16)
	ctx, cancel := context.WithCancel(a.stopCtx)
	a.saveSubSet(symbols)
	safego.Go("mt5:quoteStream", func() { a.runQuoteStream(ctx, ch) })
	return ch, cancel
}

// Resubscribe 运行时补订阅新符号（D-042：保存映射后立即生效，无需重启/重开流）。
// 仅注册会话订阅（mtapi 幂等），不开新 OnQuote 流——会话按当前订阅集投递，已开流自动接上。
// D-047 缺陷③：映射了不重订阅 = 没报价流。
// D-100：同时增补订阅集镜像，OnQuote 流重连后 resubscribeAll 恢复全集。
func (a *MT5Adapter) Resubscribe(symbols []string) {
	if a.getToken() == "" {
		return
	}
	a.addSubSet(symbols)
	sctx := a.withSessionMD(a.stopCtx)
	for _, sym := range symbols {
		_, err := a.subs.Subscribe(sctx, &mt5pb.SubscribeRequest{Id: a.getToken(), Symbol: sym})
		if err != nil {
			log.Printf("[mt5:%s] resubscribe %s: %v", a.venue, sym, err)
		}
	}
}

// Stop 停止适配器。
func (a *MT5Adapter) Stop() {
	a.stopCancel()
	if a.conn != nil {
		_ = a.conn.Close()
	}
}

// Venue 返回场所标识。
func (a *MT5Adapter) Venue() string { return a.venue }

// AccountID 返回全栈唯一账号身份。
func (a *MT5Adapter) AccountID() string {
	return a.venue + ":" + strconv.FormatInt(a.user, 10)
}

func (a *MT5Adapter) TradingClient() mt5pb.TradingClient { return a.trading }
func (a *MT5Adapter) MT5Client() mt5pb.MT5Client         { return a.mt5 }
func (a *MT5Adapter) Token() string {
	return a.getToken()
}
