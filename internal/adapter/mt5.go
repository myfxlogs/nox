package adapter

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/bus"
	"arb/internal/fact"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
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

	symMap atomic.Pointer[map[string]string] // brokerSymbol → canonical

	// MarginSink margin fact 下沉（D-099 事件驱动化）：由 main 经 BrokerManager 注入
	// （SwapKick 同模式：wiring/main 注入，brokerMgr 只存字段不依赖 engine 包）。
	// nil = 未注入（测试/未接线），runMarginSource 安全 no-op。
	MarginSink func(fact.Fact)

	stopCtx    context.Context
	stopCancel context.CancelFunc
	mu         sync.Mutex
}

// NewMT5Adapter 创建 MT5 适配器。
func NewMT5Adapter(venue, host, server string, port int32, user int64, pass string) *MT5Adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &MT5Adapter{
		venue:      venue,
		host:       host,
		port:       port,
		user:       user,
		pass:       pass,
		server:     server,
		stopCtx:    ctx,
		stopCancel: cancel,
	}
}

// SetSymbolMap 设置 brokerSymbol → canonical 映射。
func (a *MT5Adapter) SetSymbolMap(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	a.symMap.Store(&cp)
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

	gateway := "mt5grpc3.mtapi.io:443"
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	conn, err := grpc.DialContext(ctx, gateway,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return fmt.Errorf("mt5 dial: %w", err)
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
	go a.runMarginSource(a.stopCtx)
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

// withSessionMD 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func (a *MT5Adapter) withSessionMD(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"id": a.token}))
}

// Subscribe 实现 collect.QuoteSource。
func (a *MT5Adapter) Subscribe(symbols []string) (<-chan bus.Quote, func()) {
	ch := make(chan bus.Quote, 16)

	ctx, cancel := context.WithCancel(a.stopCtx)
	sctx := a.withSessionMD(ctx)
	for _, sym := range symbols {
		_, err := a.subs.Subscribe(sctx, &mt5pb.SubscribeRequest{
			Id:     a.token,
			Symbol: sym,
		})
		if err != nil {
			log.Printf("[mt5:%s] subscribe %s: %v", a.venue, sym, err)
		}
	}

	go func() {
		defer close(ch)
		stream, err := a.streams.OnQuote(sctx, &mt5pb.OnQuoteRequest{
			Id: a.token,
		})
		if err != nil {
			log.Printf("[mt5:%s] onquote stream: %v", a.venue, err)
			return
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("[mt5:%s] onquote recv: %v", a.venue, err)
				}
				return
			}
			q := resp.GetResult()
			if q == nil {
				continue
			}
			brokerSym := q.GetSymbol()
			// 实时报价流统一用本地接收时间——mtapi 网关按经纪商服务器时区编码
			// time（FxPro UTC+3 等），各经纪商偏移各异无法通用补偿；本机时钟相对
			// freshness TTL（60s）毫秒级误差可忽略。
			ts := time.Now()
			ch <- bus.Quote{
				Symbol:      a.canonical(brokerSym),
				Venue:       a.venue,
				VenueSymbol: brokerSym,
				VenueType:   "mt5",
				Bid:         q.GetBid(),
				Ask:         q.GetAsk(),
				Time:        ts,
			}
		}
	}()

	return ch, cancel
}

// Resubscribe 运行时补订阅新符号（D-042：保存映射后立即生效，无需重启/重开流）。
// 仅注册会话订阅（mtapi 幂等），不开新 OnQuote 流——会话按当前订阅集投递，已开流自动接上。
// D-047 缺陷③：映射了不重订阅 = 没报价流。
func (a *MT5Adapter) Resubscribe(symbols []string) {
	if a.token == "" {
		return
	}
	sctx := a.withSessionMD(a.stopCtx)
	for _, sym := range symbols {
		_, err := a.subs.Subscribe(sctx, &mt5pb.SubscribeRequest{Id: a.token, Symbol: sym})
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
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}
