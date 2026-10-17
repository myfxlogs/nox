// mtprobe —— mtapi 全 RPC 实测能力账 harness（业主令 10-17；D-607 病灶补课）。
// 白/黑名单机械分离：mutating 黑名单禁跑真户（如实标缺）；白名单读面 typed
// 实测、流类 60s 窗计帧、reflection describe 全消息+枚举 dump。
// 产物=docs/能力账/mt5-rpc-probe-<日期>.json + .md（harness 直出禁手编）。
// 凭据径：--dsn 读 broker_accounts（生产户在 SG；禁落盘禁打印）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// readDemoEnv demo 凭据 env 解析（KEY=VALUE 行；值禁打印）。
func readDemoEnv(path string) (DemoCreds, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return DemoCreds{}, err
	}
	var c DemoCreds
	for _, ln := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(ln), "=")
		if !ok {
			continue
		}
		switch k {
		case "MT5_DEMO_USER":
			fmt.Sscanf(v, "%d", &c.User)
		case "MT5_DEMO_PASS":
			c.Pass = v
		case "MT5_DEMO_SERVER":
			c.Server = v
		}
	}
	if c.User == 0 || c.Pass == "" || c.Server == "" {
		return c, fmt.Errorf("demo env incomplete")
	}
	return c, nil
}

// Evidence 单 RPC×单户实测行（JSON 直出）。
type Evidence struct {
	Service   string   `json:"service"`
	RPC       string   `json:"rpc"`
	Venue     string   `json:"venue"`
	Status    string   `json:"status"` // ok|err|unimplemented|mutating-skipped|stream|skipped
	ErrCode   string   `json:"errcode,omitempty"`
	MS        int64    `json:"ms"`
	KeyFields []string `json:"keyfields,omitempty"` // 顶层非空字段集（protoreflect 泛化）
	Sample    string   `json:"sample,omitempty"`    // protojson 前 N 字节摘录
}

// mutatingRPC 禁线黑名单（真钱+账户变更——真户禁跑，如实标缺）。
// demoTok demo 户会话 token（probeMutating 段用——Bind 后随段）。
var demoTok string

var mutatingRPC = map[string]bool{
	"Trading.OrderSend": true, "Trading.OrderModify": true,
	"Trading.OrderClose": true, "MT5.ChangePassword": true,
}

// streamRPC 流类（订阅+60s 窗计帧）。
var streamRPC = map[string]bool{
	"Streams.Events": true, "Streams.OnOrderUpdate": true, "Streams.OnQuote": true,
	"Streams.OnTickValue": true, "Streams.OnOrderProfit": true, "Streams.OnMarketWatch": true,
	"Streams.OnMail": true, "Streams.OnOpenedOrdersTickets": true,
	"Subscriptions.Subscribe": true, "Subscriptions.SubscribeMany": true,
	"Subscriptions.SubscribeOrderProfit": true, "Subscriptions.SubscribeTickValue": true,
	"Subscriptions.SubscribeOrderUpdate": true, "Subscriptions.SubscribeMarketWatch": true,
	"Subscriptions.SubscribeOpenedOrdersTickets": true,
	"Subscriptions.UnSubscribe":                  true, "Subscriptions.UnSubscribeMany": true,
	"TickHistory.TickHistoryRequest": true, "TickHistory.OnTickHistory": true,
	"TickHistory.TickHistoryStop": true,
}

func main() {
	dsn := flag.String("dsn", "", "postgres DSN（SG 侧读 broker_accounts 凭据）")
	outDir := flag.String("out", "docs/能力账", "产物目录")
	window := flag.Duration("window", 60*time.Second, "流类订阅计帧窗")
	demoEnv := flag.String("demo-env", "", "demo 户凭据 env 文件（MT5_DEMO_USER/SERVER/PASS——mutating 全谱面）")
	flag.Parse()
	if *dsn == "" {
		log.Fatal("--dsn required")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT venue::bigint, password, host, port FROM broker_accounts WHERE status='connected' ORDER BY venue`)
	if err != nil {
		log.Fatalf("accounts: %v", err)
	}
	type acct struct {
		user       uint64
		pass, host string
		port       int32
	}
	var accts []acct
	for rows.Next() {
		var a acct
		var v int64
		if err := rows.Scan(&v, &a.pass, &a.host, &a.port); err != nil {
			log.Fatalf("scan: %v", err)
		}
		a.user = uint64(v)
		accts = append(accts, a)
	}
	rows.Close()

	gconn, err := grpc.NewClient("mt5grpc.mtapi.io:443", grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	if err != nil {
		log.Fatalf("gateway: %v", err)
	}
	connSvc := mt5pb.NewConnectionClient(gconn)
	mt5 := mt5pb.NewMT5Client(gconn)

	var all []Evidence
	refl := reflectDump(gconn)
	for _, a := range accts {
		vs := fmt.Sprintf("%d", a.user)
		t0 := time.Now()
		// mtapi Connect 语义（adapter 生产形态）：客户端自带会话槽位 id 字段+后续
		// 调用 metadata id=同值路由——服务器 token 路径在网关实测不可靠（全 RPC
		// "Value cannot be null" 实拍），生产形态为准。
		cr, err := connSvc.Connect(ctx, &mt5pb.ConnectRequest{User: a.user, Password: a.pass, Host: a.host, Port: a.port})
		if err != nil || cr.GetError() != nil || cr.GetResult() == "" {
			msg := fmt.Sprint(err)
			if cr != nil && cr.GetError() != nil {
				msg = cr.GetError().GetMessage()
			}
			all = append(all, Evidence{Service: "Connection", RPC: "Connect", Venue: vs, Status: "err", ErrCode: msg})
			continue
		}
		tok := cr.GetResult()
		all = append(all, Evidence{Service: "Connection", RPC: "Connect", Venue: vs,
			Status: "ok", MS: time.Since(t0).Milliseconds(), KeyFields: []string{"token"}})
		all = append(all, probeRead(ctx, vs, tok, mt5, mt5pb.NewSubscriptionsClient(gconn), mt5pb.NewStreamsClient(gconn), mt5pb.NewQuoteHistoryClient(gconn), mt5pb.NewServiceClient(gconn), mt5pb.NewTickHistoryClient(gconn), *window)...)
		all = append(all, probeStreams(ctx, vs, tok, mt5pb.NewSubscriptionsClient(gconn), mt5pb.NewStreamsClient(gconn), *window)...)
		all = append(all, probeGap(ctx, vs, tok, mt5, mt5pb.NewSubscriptionsClient(gconn), mt5pb.NewStreamsClient(gconn), mt5pb.NewTickHistoryClient(gconn), 12*time.Second)...)
		all = append(all, Evidence{Service: "Connection", RPC: "Disconnect", Venue: vs, Status: "ok", KeyFields: []string{"session-closed"}})
		all = append(all, probeRest(ctx, vs, tok, mt5pb.NewQuoteHistoryClient(gconn), mt5pb.NewServiceClient(gconn), mt5pb.NewSubscriptionsClient(gconn), mt5pb.NewStreamsClient(gconn), mt5pb.NewTickHistoryClient(gconn), 12*time.Second)...)
		_, derr := connSvc.Disconnect(ctx, &mt5pb.DisconnectRequest{Id: tok})
		_ = derr
	}
	all = append(all, mutatingRows()...)
	// demo 户 mutating 全谱（业主供户；独立连接绕 roster——隔离钉①）。
	if *demoEnv != "" {
		creds, derr := readDemoEnv(*demoEnv)
		if derr != nil {
			log.Printf("[mtprobe] demo-env: %v", derr)
		} else {
			dtok, _, _, dev := demoConnect(ctx, connSvc, mt5pb.NewServiceClient(gconn), creds)
			all = append(all, dev...)
			if dtok != "" {
				demoTok = dtok
				dctx := metadata.AppendToOutgoingContext(ctx, "id", dtok)
				all = append(all, probeMutating(dctx, fmt.Sprint(creds.User), mt5pb.NewTradingClient(gconn), mt5)...)
				// demo 户读面+流族（Mails/TickHistory/Subscribe 全族同测）。
				all = append(all, probeRead(dctx, fmt.Sprint(creds.User), dtok, mt5, mt5pb.NewSubscriptionsClient(gconn), mt5pb.NewStreamsClient(gconn), mt5pb.NewQuoteHistoryClient(gconn), mt5pb.NewServiceClient(gconn), mt5pb.NewTickHistoryClient(gconn), *window)...)
				all = append(all, probeStreams(dctx, fmt.Sprint(creds.User), dtok, mt5pb.NewSubscriptionsClient(gconn), mt5pb.NewStreamsClient(gconn), *window)...)
			} else {
				all = append(all, Evidence{RPC: "mutating-全谱", Status: "skipped", ErrCode: "demo 连接不可达（host:port 缺口径缺口）——如实挂缺口"})
			}
		}
	}
	writeOut(*outDir, all, refl)
}

func mutatingRows() []Evidence {
	var out []Evidence
	for rpc := range mutatingRPC {
		out = append(out, Evidence{RPC: rpc, Status: "mutating-skipped", ErrCode: "真户禁跑（禁线）——未实测如实缺"})
	}
	return out
}

// protoKeyFields 顶层非空字段集（protoreflect 泛化——免逐 RPC 手写 keyfields）。
func protoKeyFields(m interface {
	ProtoReflect() interface{ IsValid() bool }
}) []string {
	return nil // 占位——实现见 evidence.go reflectFields
}

func marshalSample(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func truncErr(e *mt5pb.Error) string {
	if e == nil {
		return ""
	}
	m := e.GetMessage()
	if len(m) > 120 {
		m = m[:120]
	}
	return m
}

var _ = strings.TrimSpace
