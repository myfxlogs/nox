// probe_mutating.go —— demo 户 mutating 全谱（业主供 demo 277842155/Exness-MT5Trial5；
// 真户禁跑不变）。周期=OrderSend 0.01 市价→OrderModify SL/TP→OrderClose；
// Pending=BuyLimit 挂→Modify 改价→Close 撤。demo 数据禁写生产表（证据直出）。
// host:port 缺口径=Search(公司) 实解析（权威源 Service.Search 语义）。
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// DemoCreds demo 凭据（--demo-env 读入；禁落盘禁打印）。
type DemoCreds struct {
	User   uint64
	Pass   string
	Server string
}

// probeMutating demo 户 mutating 全谱。
func probeMutating(ctx context.Context, venue string, trading mt5pb.TradingClient, mt5 mt5pb.MT5Client) []Evidence {
	var out []Evidence
	// 前置：demo 户可交易符号实读（券商方言——XAUUSD 可能带后缀）。
	tradeSym := "XAUUSD"
	if lst, err := mt5.SymbolList(ctx, &mt5pb.SymbolListRequest{Id: demoTok}); err == nil {
		for _, s := range lst.GetResult() {
			if strings.HasPrefix(strings.ToUpper(s), "XAUUSD") {
				tradeSym = s
				break
			}
		}
	}
	var lastTicket int64

	// ① OrderSend 市价 0.01 BUY。
	t0 := time.Now()
	sl, tp := 0.0, 0.0
	send, err := trading.OrderSend(ctx, &mt5pb.OrderSendRequest{
		Id: demoTok, Symbol: tradeSym, Operation: mt5pb.OrderType_OrderType_Buy, Volume: 0.01,
		Stoploss: &sl, Takeprofit: &tp, Comment: strPtr("mtprobe-mutating"),
	})
	if err != nil || send.GetError().GetMessage() != "" {
		msg := fmt.Sprint(err)
		if send != nil && send.GetError() != nil {
			msg = send.GetError().GetMessage()
		}
		out = append(out, Evidence{Service: "Trading", RPC: "OrderSend", Venue: venue, Status: "err", ErrCode: truncStr(msg), MS: time.Since(t0).Milliseconds()})
		// 市价败 → pending 谱照测（限价单不依赖市价成交）。
		out = append(out, probePending(ctx, venue, trading, tradeSym)...)
		return out
	}
	lastTicket = send.GetResult().GetTicket()
	out = append(out, Evidence{Service: "Trading", RPC: "OrderSend", Venue: venue, Status: "ok",
		MS: time.Since(t0).Milliseconds(), KeyFields: []string{"ticket=" + fmt.Sprint(lastTicket), "volume=0.01", tradeSym},
		Sample: "OrderSend 0.01 BUY " + tradeSym})

	// ② OrderModify SL/TP（离价 10% 防即时触发）。
	if lastTicket != 0 {
		t0 = time.Now()
		mod, err := trading.OrderModify(ctx, &mt5pb.OrderModifyRequest{
			Id: demoTok, Ticket: lastTicket, Stoploss: 1.0, Takeprofit: 99999.0,
		})
		st := "ok"
		msg := ""
		if err != nil {
			st, msg = "err", truncStr(err.Error())
		} else if mod.GetError().GetMessage() != "" {
			st, msg = "err", truncStr(mod.GetError().GetMessage())
		}
		out = append(out, Evidence{Service: "Trading", RPC: "OrderModify", Venue: venue, Status: st,
			ErrCode: msg, MS: time.Since(t0).Milliseconds(), KeyFields: []string{fmt.Sprintf("ticket=%d", lastTicket)}})
	}

	// ③ OrderClose 平仓（全周期闭环）。
	if lastTicket != 0 {
		t0 = time.Now()
		cl, err := trading.OrderClose(ctx, &mt5pb.OrderCloseRequest{Id: demoTok, Ticket: lastTicket})
		st := "ok"
		msg := ""
		if err != nil {
			st, msg = "err", truncStr(err.Error())
		} else if cl.GetError().GetMessage() != "" {
			st, msg = truncStr(cl.GetError().GetMessage()), ""
			st = "err"
		}
		out = append(out, Evidence{Service: "Trading", RPC: "OrderClose", Venue: venue, Status: st,
			ErrCode: msg, MS: time.Since(t0).Milliseconds(), KeyFields: []string{fmt.Sprintf("ticket=%d", lastTicket)}})
	}

	// ④ Pending 谱：BuyLimit 挂→Modify 改价→Close 撤。
	out = append(out, probePending(ctx, venue, trading, tradeSym)...)
	return out
}

// probePending pending 挂/改/撤三拍。
func probePending(ctx context.Context, venue string, trading mt5pb.TradingClient, tradeSym string) []Evidence {
	var out []Evidence
	op := mt5pb.OrderType_OrderType_BuyLimit
	lowPrice := 1.0 // 深价挂单（不成交）
	t0 := time.Now()
	send, err := trading.OrderSend(ctx, &mt5pb.OrderSendRequest{
		Id: demoTok, Symbol: tradeSym, Operation: op, Volume: 0.01, Price: &lowPrice,
		Comment: strPtr("mtprobe-pending"),
	})
	if err != nil || send.GetError().GetMessage() != "" {
		msg := fmt.Sprint(err)
		if send != nil && send.GetError().GetMessage() != "" {
			msg = send.GetError().GetMessage()
		}
		return append(out, Evidence{Service: "Trading", RPC: "OrderSend(pending)", Venue: venue, Status: "err", ErrCode: truncStr(msg), MS: time.Since(t0).Milliseconds()})
	}
	ticket := send.GetResult().GetTicket()
	out = append(out, Evidence{Service: "Trading", RPC: "OrderSend(pending)", Venue: venue, Status: "ok",
		MS: time.Since(t0).Milliseconds(), KeyFields: []string{fmt.Sprintf("ticket=%d", ticket), "BuyLimit@1.0"}})
	if ticket != 0 {
		t0 = time.Now()
		mod, err := trading.OrderModify(ctx, &mt5pb.OrderModifyRequest{Id: demoTok, Ticket: ticket, Stoploss: 0, Takeprofit: 0, Price: &lowPrice})
		st, msg := "ok", ""
		if err != nil {
			st, msg = "err", truncStr(err.Error())
		} else if mod.GetError().GetMessage() != "" {
			st, msg = "err", truncStr(mod.GetError().GetMessage())
		}
		out = append(out, Evidence{Service: "Trading", RPC: "OrderModify(pending)", Venue: venue, Status: st, ErrCode: msg, MS: time.Since(t0).Milliseconds()})
		t0 = time.Now()
		cl, err := trading.OrderClose(ctx, &mt5pb.OrderCloseRequest{Id: demoTok, Ticket: ticket})
		st, msg = "ok", ""
		if err != nil {
			st, msg = "err", truncStr(err.Error())
		} else if cl.GetError().GetMessage() != "" {
			st, msg = "err", truncStr(cl.GetError().GetMessage())
		}
		out = append(out, Evidence{Service: "Trading", RPC: "OrderClose(pending delete)", Venue: venue, Status: st, ErrCode: msg, MS: time.Since(t0).Milliseconds()})
	}
	return out
}

func strPtr(s string) *string { return &s }

// demoConnect demo 户连接（独立连接绕 roster；host:port=Search 实解析或在册端点试）。
func demoConnect(ctx context.Context, connSvc mt5pb.ConnectionClient, svc mt5pb.ServiceClient,
	creds DemoCreds) (tok string, host string, port int32, ev []Evidence) {
	// 路径①：Search 实解析 server→host 列表。
	sr, err := svc.Search(ctx, &mt5pb.SearchRequest{Company: strings.Split(creds.Server, "-")[0]})
	if err == nil && sr.GetError().GetMessage() == "" {
		for _, c := range sr.GetResult() {
			for _, r := range c.GetResults() {
				// Result.Name=server 名；Access=host:port 列表。
				if strings.EqualFold(r.GetName(), creds.Server) && len(r.GetAccess()) > 0 {
					host = r.GetAccess()[0]
				}
			}
		}
	}
	// 路径②：Search 无果 → 在册三端点握手试（只 Connect 探测，禁动真户 session）。
	candidates := []struct {
		host string
		port int32
	}{{host, 443}}
	for _, h := range []string{"94.130.2.58", "48.222.251.98", "35.157.110.232"} {
		candidates = append(candidates, struct {
			host string
			port int32
		}{h, 443})
	}
	for _, cand := range candidates {
		if cand.host == "" {
			continue
		}
		t0 := time.Now()
		cr, cerr := connSvc.Connect(ctx, &mt5pb.ConnectRequest{
			User: creds.User, Password: creds.Pass, Host: cand.host, Port: cand.port,
		})
		if cerr == nil && cr.GetError() == nil && cr.GetResult() != "" {
			ev = append(ev, Evidence{Service: "Connection", RPC: "Connect(demo)", Venue: fmt.Sprint(creds.User),
				Status: "ok", MS: time.Since(t0).Milliseconds(),
				KeyFields: []string{fmt.Sprintf("endpoint=%s:%d", cand.host, cand.port)}})
			return cr.GetResult(), cand.host, cand.port, ev
		}
		msg := fmt.Sprint(cerr)
		if cr != nil && cr.GetError() != nil {
			msg = cr.GetError().GetMessage()
		}
		ev = append(ev, Evidence{Service: "Connection", RPC: "Connect(demo)", Venue: fmt.Sprint(creds.User),
			Status: "err", ErrCode: truncStr(msg) + fmt.Sprintf(" (endpoint %s:%d)", cand.host, cand.port), MS: time.Since(t0).Milliseconds()})
	}
	return "", "", 0, ev
}
