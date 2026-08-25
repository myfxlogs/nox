// Package adapter 实现 MT4/MT5/CEX 采集适配层（§3.2/§3.3）。
// 当前阶段仅实现 QuoteSource 接口（报价订阅），交易方法留到执行层阶段。
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

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	"arb/internal/bus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// MT4Adapter 通过 mtapi.io gRPC 网关连接 MT4 经纪商（§3.2）。
type MT4Adapter struct {
	venue  string
	host   string
	port   int32
	user   int64
	pass   string
	server string

	conn    *grpc.ClientConn
	connMgr mt4pb.ConnectionClient
	mt4     mt4pb.MT4Client
	subs    mt4pb.SubscriptionsClient
	streams mt4pb.StreamsClient
	trading mt4pb.TradingClient
	token   string

	symMap atomic.Pointer[map[string]string] // brokerSymbol → canonical

	// D-100：订阅集镜像（同 MT5，OnQuote 流重连后恢复订阅）。
	subMu  sync.Mutex
	subSet map[string]bool

	// D-100：OnQuote 流重连退避档位（复用 marginReconnectBackoff 序列）。
	quoteReconIdx int

	stopCtx    context.Context
	stopCancel context.CancelFunc
	mu         sync.Mutex
}

// NewMT4Adapter 创建 MT4 适配器。
func NewMT4Adapter(venue, host, server string, port int32, user int64, pass string) *MT4Adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &MT4Adapter{
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

// SetSymbolMap 设置 brokerSymbol → canonical 映射（§3.2）。
func (a *MT4Adapter) SetSymbolMap(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	a.symMap.Store(&cp)
}

func (a *MT4Adapter) canonical(brokerSym string) string {
	if m := a.symMap.Load(); m != nil {
		if c, ok := (*m)[brokerSym]; ok {
			return c
		}
	}
	return brokerSym
}

// Connect 连接 mtapi 网关并认证（§3.2）。
func (a *MT4Adapter) Connect(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	gateway := "mt4grpc.mtapi.io:443"
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
		return fmt.Errorf("mt4 dial: %w", err)
	}
	a.conn = conn
	a.connMgr = mt4pb.NewConnectionClient(conn)
	a.mt4 = mt4pb.NewMT4Client(conn)
	a.subs = mt4pb.NewSubscriptionsClient(conn)
	a.streams = mt4pb.NewStreamsClient(conn)
	a.trading = mt4pb.NewTradingClient(conn)

	// mtapi 网关按 id header 路由（归档实现一致）；Connect 用临时会话 id。
	sid := sessionID(a.user)
	loginCtx := metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"id": sid}))
	resp, err := a.connMgr.Connect(loginCtx, &mt4pb.ConnectRequest{
		Host:     a.host,
		Port:     a.port,
		User:     int32(a.user),
		Password: a.pass,
		Id:       &sid,
	})
	if err != nil {
		return fmt.Errorf("mt4 connect: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return fmt.Errorf("mt4 connect: %s", e.GetMessage())
	}
	token := resp.GetResult()
	if token == "" {
		return fmt.Errorf("mt4 connect: empty token")
	}
	a.token = token
	log.Printf("[mt4:%s] connected, token=%s", a.venue, maskToken(token))
	return nil
}

// getToken 持锁读 token（reconnectSession 持 a.mu 写，无锁读=CI 实抓数据竞争）。
func (a *MT4Adapter) getToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}

// withSessionMD 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func (a *MT4Adapter) withSessionMD(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"id": a.getToken()}))
}

// reconnectSession 复用现有 conn 重调 connMgr.Connect 重建 account 会话（D-100，同 MT5 S1 模式）。
// MT4 proto 差异：User 为 int32（MT5 为 uint64）。
func (a *MT4Adapter) reconnectSession(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sid := sessionID(a.user)
	loginCtx := metadata.NewOutgoingContext(rctx, metadata.New(map[string]string{"id": sid}))
	resp, err := a.connMgr.Connect(loginCtx, &mt4pb.ConnectRequest{
		Host:     a.host,
		Port:     a.port,
		User:     int32(a.user),
		Password: a.pass,
		Id:       &sid,
	})
	if err != nil {
		return fmt.Errorf("mt4 reconnect: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return fmt.Errorf("mt4 reconnect: %s", e.GetMessage())
	}
	token := resp.GetResult()
	if token == "" {
		return fmt.Errorf("mt4 reconnect: empty token")
	}
	a.token = token
	log.Printf("[mt4:%s] session reconnected, token=%s", a.venue, maskToken(token))
	return nil
}

// sessionID Connect 阶段的临时会话标识。
func sessionID(user int64) string {
	return "mdgw-" + strconv.FormatInt(user, 10)
}

// Subscribe 实现 collect.QuoteSource（§3.2）。
// D-100：OnQuote 流加重连（同 MT5 模式）——抽 runQuoteStream/consumeQuoteFrames，
// 断流后退避重开 + 重订阅（订阅集镜像 subSet）。ch 仅在 ctx 取消时关闭。
func (a *MT4Adapter) Subscribe(symbols []string) (<-chan bus.Quote, func()) {
	ch := make(chan bus.Quote, 16)
	ctx, cancel := context.WithCancel(a.stopCtx)
	a.saveSubSet(symbols)
	go a.runQuoteStream(ctx, ch)
	return ch, cancel
}

// Resubscribe 运行时补订阅新符号（D-042：保存映射后立即生效，无需重启/重开流）。
// 仅注册会话订阅（mtapi 幂等），不开新 OnQuote 流——会话按当前订阅集投递，已开流自动接上。
// D-047 缺陷③：映射了不重订阅 = 没报价流。
// D-100：同时增补订阅集镜像，OnQuote 流重连后 resubscribeAll 恢复全集。
func (a *MT4Adapter) Resubscribe(symbols []string) {
	if a.getToken() == "" {
		return
	}
	a.addSubSet(symbols)
	sctx := a.withSessionMD(a.stopCtx)
	for _, sym := range symbols {
		_, err := a.subs.Subscribe(sctx, &mt4pb.SubscribeRequest{Id: a.getToken(), Symbol: sym})
		if err != nil {
			log.Printf("[mt4:%s] resubscribe %s: %v", a.venue, sym, err)
		}
	}
}

// Stop 停止适配器。
func (a *MT4Adapter) Stop() {
	a.stopCancel()
	if a.conn != nil {
		_ = a.conn.Close()
	}
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "..." + t[len(t)-4:]
}

// Venue 返回场所标识。
func (a *MT4Adapter) Venue() string { return a.venue }

// AccountID 返回全栈唯一账号身份。
func (a *MT4Adapter) AccountID() string {
	return a.venue + ":" + strconv.FormatInt(a.user, 10)
}

func (a *MT4Adapter) TradingClient() mt4pb.TradingClient { return a.trading }
func (a *MT4Adapter) MT4Client() mt4pb.MT4Client         { return a.mt4 }
func (a *MT4Adapter) Token() string {
	return a.getToken()
}
