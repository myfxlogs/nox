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

// withSessionMD 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func (a *MT4Adapter) withSessionMD(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"id": a.token}))
}

// sessionID Connect 阶段的临时会话标识。
func sessionID(user int64) string {
	return "mdgw-" + strconv.FormatInt(user, 10)
}

// Subscribe 实现 collect.QuoteSource（§3.2）。
// 返回的 channel 接收 bus.Quote（canonical symbol）。
func (a *MT4Adapter) Subscribe(symbols []string) (<-chan bus.Quote, func()) {
	ch := make(chan bus.Quote, 16)

	// 订阅（mtapi 按 id header + Id body 路由）
	ctx, cancel := context.WithCancel(a.stopCtx)
	sctx := a.withSessionMD(ctx)
	for _, sym := range symbols {
		_, err := a.subs.Subscribe(sctx, &mt4pb.SubscribeRequest{
			Id:     a.token,
			Symbol: sym,
		})
		if err != nil {
			log.Printf("[mt4:%s] subscribe %s: %v", a.venue, sym, err)
		}
	}

	// 开启 OnQuote 流
	go func() {
		defer close(ch)
		stream, err := a.streams.OnQuote(sctx, &mt4pb.OnQuoteRequest{
			Id: a.token,
		})
		if err != nil {
			log.Printf("[mt4:%s] onquote stream: %v", a.venue, err)
			return
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("[mt4:%s] onquote recv: %v", a.venue, err)
				}
				return
			}
			q := resp.GetResult()
			if q == nil {
				continue
			}
			brokerSym := q.GetSymbol()
			var ts time.Time
			if t := q.GetTime(); t != nil {
				ts = t.AsTime()
			} else {
				ts = time.Now()
			}
			ch <- bus.Quote{
				Symbol:      a.canonical(brokerSym),
				Venue:       a.venue,
				VenueSymbol: brokerSym,
				VenueType:   "mt4",
				Bid:         q.GetBid(),
				Ask:         q.GetAsk(),
				Time:        ts,
			}
		}
	}()

	return ch, cancel
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
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}
