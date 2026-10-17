// probe_read.go —— 白名单读面 typed 实测（A 档全量：MT5/QuoteHistory/Service 22 RPC）
// + Subscriptions/TickHistory 动作面。逐条 Evidence（ok/err/unimplemented+keyfields）。
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// rec 统一记录器：typed reply → status/keyfields/sample。
func rec(svc, rpc, venue string, t0 time.Time, reply proto.Message, rerr error, ep *mt5pb.Error) Evidence {
	e := Evidence{Service: svc, RPC: rpc, Venue: venue, MS: time.Since(t0).Milliseconds()}
	if rerr != nil {
		e.Status, e.ErrCode = "err", truncStr(rerr.Error())
		return e
	}
	if ep != nil && ep.GetMessage() != "" {
		e.Status, e.ErrCode = "err", truncStr(ep.GetMessage())
		if strings.Contains(e.ErrCode, "Unimplemented") || strings.Contains(e.ErrCode, "not implemented") {
			e.Status = "unimplemented"
		}
		return e
	}
	e.Status = "ok"
	e.KeyFields = keyFields(reply)
	e.Sample = truncStr(protojsonish(reply))
	recordRaw(e.Service+"."+e.RPC+"@"+venue, reply)
	return e
}

func truncStr(s string) string {
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

// probeRead 白名单读面全量（token 前置于 mctx）。
func probeRead(ctx context.Context, venue, tok string, mt5 mt5pb.MT5Client,
	sub mt5pb.SubscriptionsClient, streams mt5pb.StreamsClient,
	qh mt5pb.QuoteHistoryClient, svc mt5pb.ServiceClient, th mt5pb.TickHistoryClient,
	window time.Duration) []Evidence {
	var out []Evidence
	call := func(svcName, rpc string, fn func() (proto.Message, *mt5pb.Error, error)) {
		t0 := time.Now()
		reply, ep, err := fn()
		out = append(out, rec(svcName, rpc, venue, t0, reply, err, ep))
	}
	sym := "XAUUSD"

	call("MT5", "Account", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.Account(ctx, &mt5pb.AccountRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "AccountSummary", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.AccountSummary(ctx, &mt5pb.AccountSummaryRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "OpenedOrders", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.OpenedOrders(ctx, &mt5pb.OpenedOrdersRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "OpenedOrdersTickets", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.OpenedOrdersTickets(ctx, &mt5pb.OpenedOrdersTicketsRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "OrderHistory", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.OrderHistory(ctx, &mt5pb.OrderHistoryRequest{Id: tok, From: "2026-09-17T00:00:00", To: "2026-10-17T00:00:00"})
		return r, r.GetError(), err
	})
	call("MT5", "PendingOrderHistory", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.PendingOrderHistory(ctx, &mt5pb.PendingOrderHistoryRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "OrderHistoryPagination", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.OrderHistoryPagination(ctx, &mt5pb.OrderHistoryPaginationRequest{Id: tok, From: "2026-09-17T00:00:00", To: "2026-10-17T00:00:00", OrdersPerPage: 10, PageNumber: 0})
		return r, r.GetError(), err
	})
	call("MT5", "Symbols", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.Symbols(ctx, &mt5pb.SymbolsRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "SymbolList", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.SymbolList(ctx, &mt5pb.SymbolListRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "GetQuote", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.GetQuote(ctx, &mt5pb.GetQuoteRequest{Id: tok, Symbol: sym})
		return r, r.GetError(), err
	})
	call("MT5", "GetQuoteMany", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.GetQuoteMany(ctx, &mt5pb.GetQuoteManyRequest{Id: tok, Symbols: []string{sym}})
		return r, r.GetError(), err
	})
	call("MT5", "MarketWatchMany", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.MarketWatchMany(ctx, &mt5pb.MarketWatchManyRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "SymbolParams", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.SymbolParams(ctx, &mt5pb.SymbolParamsRequest{Id: tok, Symbol: sym})
		return r, r.GetError(), err
	})
	call("MT5", "SymbolParamsMany", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.SymbolParamsMany(ctx, &mt5pb.SymbolParamsManyRequest{Id: tok, Symbol: []string{sym}})
		return r, r.GetError(), err
	})
	call("MT5", "ServerTimezone", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.ServerTimezone(ctx, &mt5pb.ServerTimezoneRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "IsTradeSession", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.IsTradeSession(ctx, &mt5pb.IsTradeSessionRequest{Id: tok, Symbol: sym})
		return r, r.GetError(), err
	})
	call("MT5", "IsQuoteSession", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.IsQuoteSession(ctx, &mt5pb.IsQuoteSessionRequest{Id: tok, Symbol: sym})
		return r, r.GetError(), err
	})
	call("MT5", "GetTickValueMany", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.GetTickValueMany(ctx, &mt5pb.GetTickValueManyRequest{Id: tok, Symbol: []string{sym}})
		return r, r.GetError(), err
	})
	call("MT5", "Mails", func() (proto.Message, *mt5pb.Error, error) {
		r, err := mt5.Mails(ctx, &mt5pb.MailsRequest{Id: tok})
		return r, r.GetError(), err
	})
	call("MT5", "RequiredMargin", func() (proto.Message, *mt5pb.Error, error) {
		ty := mt5pb.DealType_DealType_DealBuy
		pr := 0.0
		r, err := mt5.RequiredMargin(ctx, &mt5pb.RequiredMarginRequest{Id: tok, Symbol: sym, Lots: 1, Type: &ty, Price: &pr})
		return r, r.GetError(), err
	})
	call("QuoteHistory", "PriceHistory", func() (proto.Message, *mt5pb.Error, error) {
		r, err := qh.PriceHistory(ctx, &mt5pb.PriceHistoryRequest{Id: tok, Symbol: sym, From: "2026-10-16T00:00:00", To: "2026-10-17T00:00:00", TimeFrame: 16408})
		return r, r.GetError(), err
	})
	call("QuoteHistory", "PriceHistoryToday", func() (proto.Message, *mt5pb.Error, error) {
		r, err := qh.PriceHistoryToday(ctx, &mt5pb.PriceHistoryTodayRequest{Id: tok, Symbol: sym, TimeFrame: 16408})
		return r, r.GetError(), err
	})
	call("Service", "Ping", func() (proto.Message, *mt5pb.Error, error) {
		r, err := svc.Ping(ctx, &mt5pb.PingRequest{})
		return r, r.GetError(), err
	})
	call("Service", "Health", func() (proto.Message, *mt5pb.Error, error) {
		r, err := svc.Health(ctx, &mt5pb.HealthRequest{})
		return r, r.GetError(), err
	})
	call("Service", "Version", func() (proto.Message, *mt5pb.Error, error) {
		r, err := svc.Version(ctx, &mt5pb.VersionRequest{})
		return r, r.GetError(), err
	})
	call("Service", "Search", func() (proto.Message, *mt5pb.Error, error) {
		r, err := svc.Search(ctx, &mt5pb.SearchRequest{Company: "XM"})
		return r, r.GetError(), err
	})
	_ = fmt.Sprintf
	_ = streams
	_ = sub
	_ = th
	_ = window
	return out
}
