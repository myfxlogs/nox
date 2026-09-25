package adapter

// MT4 订单/余额事件流消费（D-469b S4，克隆 mt_order_events.go MT5 先例）。
//
// 设计（与 MT5 同构）：
//   - OnOrderUpdate：SubscribeOrderUpdate 注册 + OnOrderUpdate 消费，帧 Update==nil → skip；
//     事件最小化只透传 {Venue, Ticket}（消费端全量回读，前端零信任）。
//   - OnOrderProfit：SubscribeOrderProfit 注册 + OnOrderProfit 消费，每帧 → MarginKick()
//     （装配=acctMon.Kick，MT5 MarginSink→OrderProfitKick 同语义——MT4 margin 采集为
//     REST 轮询域，事件只改唤醒时机不改取数源）。
//   - 流重连复用 marginReconnectBackoff + IsSessionLost/reconnectSession（同 MT5）。

import (
	"context"
	"log"
	"time"

	"arb/internal/bus"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
)

// MT4OrderEventSource MT4 OnOrderUpdate/OnOrderProfit 流消费源（D-469b S4）。
// 每 venue 一个实例，由 runMT4OrderEventSource 启动（OrderEventSink/MarginKick 双 nil → no-op）。
type MT4OrderEventSource struct {
	a *MT4Adapter

	reconIdx int
}

// Run 阻塞运行：双流消费循环（订单流与 profit 流各自独立重连）。ctx 取消退出。
func (s *MT4OrderEventSource) Run(ctx context.Context) {
	done := make(chan struct{}, 2)
	go func() {
		for {
			if ctx.Err() != nil {
				break
			}
			s.consumeOrderStream(ctx)
		}
		done <- struct{}{}
	}()
	go func() {
		for {
			if ctx.Err() != nil {
				break
			}
			s.consumeProfitStream(ctx)
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

func (s *MT4OrderEventSource) consumeOrderStream(ctx context.Context) {
	a := s.a
	sctx := a.withSessionMD(ctx)
	if _, err := a.subs.SubscribeOrderUpdate(sctx, &mt4pb.SubscribeOrderUpdateRequest{Id: a.getToken()}); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt4:%s] order-events: subscribe order update: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	stream, err := a.streams.OnOrderUpdate(sctx, &mt4pb.OnOrderUpdateRequest{Id: a.getToken()})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt4:%s] order-events: onorderupdate stream open: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	log.Printf("[mt4:%s] order-events: stream opened", a.venue)
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
			log.Printf("[mt4:%s] order-events: stream recv: %v", a.venue, err)
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt4:%s] order-events: reconnect: %v", a.venue, rerr)
				}
			}
			s.reconnectBackoff(ctx)
			return
		}
		if firstFrame {
			log.Printf("[mt4:%s] order-events: first frame", a.venue)
			firstFrame = false
		}
		upd := resp.GetResult().GetUpdate()
		if upd == nil {
			continue
		}
		var ticket int64
		if o := upd.GetOrder(); o != nil {
			ticket = int64(o.GetTicket())
		}
		s.publishOrder(bus.OrderEvent{Venue: a.venue, Ticket: ticket})
		s.reconIdx = 0
	}
}

func (s *MT4OrderEventSource) consumeProfitStream(ctx context.Context) {
	a := s.a
	if a.MarginKick == nil {
		return // 未注入=不起流（测试/未接线）
	}
	sctx := a.withSessionMD(ctx)
	if _, err := a.subs.SubscribeOrderProfit(sctx, &mt4pb.SubscribeOrderProfitRequest{Id: a.getToken()}); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt4:%s] profit-events: subscribe order profit: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	stream, err := a.streams.OnOrderProfit(sctx, &mt4pb.OnOrderProfitRequest{Id: a.getToken()})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt4:%s] profit-events: stream open: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	log.Printf("[mt4:%s] profit-events: stream opened", a.venue)
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := stream.Recv(); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt4:%s] profit-events: stream recv: %v", a.venue, err)
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt4:%s] profit-events: reconnect: %v", a.venue, rerr)
				}
			}
			s.reconnectBackoff(ctx)
			return
		}
		s.publishKick(a.MarginKick)
		s.reconIdx = 0
	}
}

// publishOrder/publishKick sink 调用 recover 守卫（事件丢失可接受——消费端有兜底）。
func (s *MT4OrderEventSource) publishOrder(e bus.OrderEvent) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[mt4:%s] order-events: sink panic: %v", s.a.venue, r)
		}
	}()
	if s.a.OrderEventSink != nil {
		s.a.OrderEventSink(e)
	}
}

func (s *MT4OrderEventSource) publishKick(kick func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[mt4:%s] profit-events: kick panic: %v", s.a.venue, r)
		}
	}()
	kick()
}

// reconnectBackoff 同 MT5 marginReconnectBackoff 有界序列（共享同档）。
func (s *MT4OrderEventSource) reconnectBackoff(ctx context.Context) {
	idx := s.reconIdx
	if idx >= len(marginReconnectBackoff) {
		idx = len(marginReconnectBackoff) - 1
	}
	d := marginReconnectBackoff[idx]
	s.reconIdx++
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// RunOrderEventSource attach 完成后由 BrokerManager 启动（OrderEventSink/MarginKick 双 nil → no-op）。
func (a *MT4Adapter) RunOrderEventSource(ctx context.Context) {
	if a.OrderEventSink == nil && a.MarginKick == nil {
		return
	}
	(&MT4OrderEventSource{a: a}).Run(ctx)
}
