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

	"arb/internal/bus"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// MT5Adapter 通过 mtapi.io gRPC 网关连接 MT5 经纪商（§3.2）。
// 与 MT4 严格隔离（独立 proto、独立生成代码、独立类）。
type MT5Adapter struct {
	venue   string
	host    string
	port    int32
	user    int64
	pass    string
	server  string

	conn    *grpc.ClientConn
	connMgr mt5pb.ConnectionClient
	mt5     mt5pb.MT5Client
	subs    mt5pb.SubscriptionsClient
	streams mt5pb.StreamsClient

	symMap atomic.Pointer[map[string]string] // brokerSymbol → canonical

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

	resp, err := a.connMgr.Connect(ctx, &mt5pb.ConnectRequest{
		Host:     a.host,
		Port:     a.port,
		User:     uint64(a.user),
		Password: a.pass,
	})
	if err != nil {
		return fmt.Errorf("mt5 connect: %w", err)
	}
	token := resp.GetResult()
	log.Printf("[mt5:%s] connected, token=%s", a.venue, maskToken(token))

	a.stopCtx = metadata.AppendToOutgoingContext(a.stopCtx, "token", token)
	return nil
}

// Subscribe 实现 collect.QuoteSource。
func (a *MT5Adapter) Subscribe(symbols []string) (<-chan bus.Quote, func()) {
	ch := make(chan bus.Quote, 16)

	ctx, cancel := context.WithCancel(a.stopCtx)
	for _, sym := range symbols {
		_, err := a.subs.Subscribe(ctx, &mt5pb.SubscribeRequest{
			Id:     a.venue,
			Symbol: sym,
		})
		if err != nil {
			log.Printf("[mt5:%s] subscribe %s: %v", a.venue, sym, err)
		}
	}

	go func() {
		defer close(ch)
		stream, err := a.streams.OnQuote(ctx, &mt5pb.OnQuoteRequest{
			Id: a.venue,
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
				VenueType:   "mt5",
				Bid:         q.GetBid(),
				Ask:         q.GetAsk(),
				Time:        ts,
			}
		}
	}()

	return ch, cancel
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
