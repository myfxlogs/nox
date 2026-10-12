package adapter

// MT5 订单事件流消费（D-107 P1，2026-08-25）。
//
// 设计（施工提示词 §3 P1 定稿，勿改）：
//   - 事件源：每 venue 1 条 OnOrderUpdate server-streaming 流（SubscribeOrderUpdate 注册 +
//     OnOrderUpdate 消费）。帧 = OrderUpdateSummary，Update（*OrderUpdate）== nil → skip。
//   - 事件最小化：只透传 {Venue, Ticket}（Ticket 从 Update.Order/OrderInternal 提取；
//     不解析业务语义——消费端收到通知后全量回读，前端零信任）。
//   - 无首采补偿：事件的消费方都在事件后做全量回读，无需重放历史（与 D-099 margin 不同，
//     margin 是静态数据需要首采兜底；订单事件本身就是数据）。
//   - 无防抖：每帧都是真实的订单状态变更，不应合并丢弃（与 D-099 margin 防抖不同，
//     margin 是静态数据防事件洪峰重复查库）。
//   - sink 守卫：sink panic 时 recover + 日志（事件丢失可接受——各消费端有兜底）。
//   - 流重连：复用 D-101 模式（外层 for + Recv + IsSessionLost/reconnectSession +
//     marginReconnectBackoff 复用，1s/5s/30s 有界）。

import (
	"context"
	"log"
	"time"

	"arb/internal/data/bus"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
)

// AcctFrameSink D-445 S0：OrderUpdateSummary 余额六字段帧下沉回调。
// venueKey=adapter venue（"acct:mt5" 与 autotrade_allow_rules.value 同构）；
// frameTime=收帧时刻（proto 无服务端时间戳，mt5.pb OrderUpdateSummary 无 ts 字段已核）。
type AcctFrameSink func(venueKey string, balance, equity, margin, freeMargin, profit, marginLevel float64, currency string, frameTime time.Time)

// OrderEventSource OnOrderUpdate 流消费源（D-107 P1）。
// 每 venue 一个实例，由 MT5Adapter.runOrderEventSource 启动；OrderEventSink 由 main 经
// BrokerManager 注入（MarginSink 同模式）。复用 a.streams / a.subs / a.withSessionMD /
// IsSessionLost / reconnectSession / marginReconnectBackoff，不新建 gRPC conn。
type OrderEventSource struct {
	a *MT5Adapter

	reconIdx int // 流重连退避档位（成功消费一帧后重置）

	acctSink AcctFrameSink // D-445 S0：余额帧下沉（nil=不接）
}

// SetAccountSink 注入余额帧 sink（可选；每帧含 Update==nil 纯余额帧都回调）。
func (s *OrderEventSource) SetAccountSink(f AcctFrameSink) { s.acctSink = f }

// Run 阻塞运行：OnOrderUpdate 事件流消费循环。ctx 取消退出。
func (s *OrderEventSource) Run(ctx context.Context, sink func(bus.OrderEvent)) {
	for {
		if ctx.Err() != nil {
			return
		}
		s.consumeStream(ctx, sink)
	}
}

// consumeStream 注册订阅 → 开流 → 消费事件帧循环。流错误 → 会话失效重连 + 退避后返回（外层重开）。
func (s *OrderEventSource) consumeStream(ctx context.Context, sink func(bus.OrderEvent)) {
	a := s.a
	sctx := a.withSessionMD(ctx)
	// SubscribeOrderUpdate 注册（幂等，重连后重订阅；同 D-099 SubscribeOrderProfit 模式，
	// 仅 Id 字段=账户级，无 per-symbol 集）。
	if _, err := a.subs.SubscribeOrderUpdate(sctx, &mt5pb.SubscribeOrderUpdateRequest{Id: a.getToken()}); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt5:%s] order-events: subscribe order update: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	log.Printf("[mt5:%s] order-events: subscribed", a.venue)
	stream, err := a.streams.OnOrderUpdate(sctx, &mt5pb.OnOrderUpdateRequest{Id: a.getToken()})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt5:%s] order-events: onorderupdate stream open: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	log.Printf("[mt5:%s] order-events: stream opened", a.venue)
	firstFrame := true
	for {
		if ctx.Err() != nil {
			return
		}
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt5:%s] order-events: stream recv: %v", a.venue, err)
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt5:%s] order-events: reconnect: %v", a.venue, rerr)
				}
			}
			s.reconnectBackoff(ctx)
			return // 外层循环重订阅 + 重开流
		}
		// 首帧标记（含 Update==nil 帧——证明流真活，不只开流成功）。每轮流重开重打一次。
		if firstFrame {
			log.Printf("[mt5:%s] order-events: first frame", a.venue)
			firstFrame = false
		}
		// 余额六字段每帧下沉（含 Update==nil 纯余额帧——此前流数据过即弃）。
		// OrderUpdateSummary 无 Currency 字段→传空串；frameTime=收帧时刻。
		if res := resp.GetResult(); res != nil && s.acctSink != nil {
			s.publishAcct(res)
		}
		// 帧解析：Update == nil → skip（非订单事件帧，如纯余额更新）。
		upd := resp.GetResult().GetUpdate()
		if upd == nil {
			continue
		}
		s.publish(sink, bus.OrderEvent{Venue: a.venue, Ticket: orderUpdateTicket(upd)})
		s.reconIdx = 0 // 成功消费一帧 → 重置重连退避档位
	}
}

// publishAcct D-445 S0：余额帧下沉，同 publish recover 守卫（sink panic 不拉崩流循环）。
func (s *OrderEventSource) publishAcct(res *mt5pb.OrderUpdateSummary) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[mt5:%s] order-events: acct sink panic: %v", s.a.venue, r)
		}
	}()
	s.acctSink(s.a.venue, res.GetBalance(), res.GetEquity(), res.GetMargin(),
		res.GetFreeMargin(), res.GetProfit(), res.GetMarginLevel(), "", time.Now())
}

// publish 调用 sink 并 recover panic（事件丢失可接受——各消费端有兜底，不应 panic 拉崩进程）。
func (s *OrderEventSource) publish(sink func(bus.OrderEvent), e bus.OrderEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[mt5:%s] order-events: sink panic: %v", s.a.venue, r)
		}
	}()
	sink(e)
}

// reconnectBackoff 流重连退避（复用 marginReconnectBackoff 1s/5s/30s 有界，封顶后保持末档）。ctx 取消立即返回。
func (s *OrderEventSource) reconnectBackoff(ctx context.Context) {
	idx := s.reconIdx
	if idx >= len(marginReconnectBackoff) {
		idx = len(marginReconnectBackoff) - 1
	}
	d := marginReconnectBackoff[idx]
	s.reconIdx++ // 下次断流进位（成功消费一帧后由 consumeStream 重置）
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// runOrderEventSource 启动 OrderEventSource（OrderEventSink==nil 时安全 no-op，测试/未注入）。
func (a *MT5Adapter) runOrderEventSource(ctx context.Context) {
	if a.OrderEventSink == nil {
		return
	}
	src := &OrderEventSource{a: a}
	src.SetAccountSink(a.AccountSink) // D-445 S0（nil 安全）
	src.Run(ctx, a.OrderEventSink)
}

// orderUpdateTicket 从 OrderUpdate 提取 ticket（优先 Order → OrderInternal；均 nil → 0 通用通知）。
// 不解析业务语义：ticket 仅作通知载荷，消费端全量回读（前端零信任）。
func orderUpdateTicket(u *mt5pb.OrderUpdate) int64 {
	if o := u.GetOrder(); o != nil {
		return o.GetTicket()
	}
	if oi := u.GetOrderInternal(); oi != nil {
		return oi.GetTicket()
	}
	return 0
}
