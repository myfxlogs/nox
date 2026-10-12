package adapter

// MT5 margin 采集（D-099，2026-08-24；D-006 S1b 帧触发 poll 退役）。
//
// 设计（施工提示词④定稿 + D-006 S1b 增补）：
//   - 数据源不变：AccountSummary.Leverage → 1/Leverage（无限杠杆 ≥1e6 → 0），S6 返修已实证。
//   - 杠杆=准静态数据，低频采集：venue attach 首采一次 + 流重连成功后各采一次。
//     首采失败 → 5/15/30min 递增重试（3 次封顶，成功后停）。无常驻定时器。
//   - D-006 S1b 帧触发 poll 退役根因：帧防抖 lastSuccess 只在成功时推进，失败路径
//     下一帧立即重试（无退避）→ 风暴节拍=swapPerCallTimeout 3s；且 poll 同步阻塞
//     卡死期间 publishPositions/publishAcctFrame 全排队（data_stale 饿源实证）。
//     运行期保证金真相（Margin/FreeMargin/MarginLevel）流帧已送，poll 唯一独占
//     产出只是 Leverage——帧驱动采集本身即形态错误。
//   - 事件流消费回归纯流（publishPositions/publishAcctFrame/emitStreamState），
//     零同步 RPC。
//   - 流重连：RST_STREAM/会话失效 → IsSessionLost + reconnectSession + 重订阅；
//     退避 1s/5s/30s 有界。重连成功补采一次杠杆（低频点的第二落点）。
//   - 解耦：SwapFacts 无 margin 段——margin 完全独立，swap errs 不波及 margin。

import (
	"context"
	"log"
	"time"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
	"nox/internal/data/collect"
	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
)

// marginFirstRetryBackoff 首采失败递增重试退避序列（3 次封顶，成功后停）。
// 包级 var 便于测试缩短（默认值即生产值，D-099 设计定稿 5/15/30min）。
var marginFirstRetryBackoff = []time.Duration{
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

// marginReconnectBackoff 流重连退避序列（有界，流断是连接层问题）。
// 包级 var 便于测试缩短（默认值即生产值，D-099 设计定稿 1s/5s/30s）。
var marginReconnectBackoff = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
}

// marginPollOnce 封装账户级 margin 取值链路（D-099；D-006 S1b 起仅供首采与重连补采调用）。
// 流程：fetchLeverage（mt_swap_helpers.go，失败在 helpers 内打日志返回 false）→ marginRate=1/lev
// （lev>=1e6 无限杠杆哨兵→0，SwapModeNone 先例）→ marginFact(KindInitMarginRate, venue, venue, rate, "mt5")。
// 账户级，Symbol=venue（账户标识非符号）；ok=false 表示本轮取值失败，调用方按场景重试或 skip。
// 放本文件（margin 专用）而非 mt_swap_helpers.go（swap 专用），内聚更合理（D-099 拆分）。
func (a *MT5Adapter) marginPollOnce(ctx context.Context) (fact.Fact, bool) {
	lev, ok := a.fetchLeverage(ctx)
	if !ok {
		return fact.Fact{}, false
	}
	rate := 1 / lev
	if lev >= 1e6 { // Exness 无限杠杆哨兵实证（2e9）：零保证金账号发 0 值（SwapModeNone 先例）
		rate = 0
	}
	return marginFact(fact.KindInitMarginRate, a.venue, "", rate, "mt5"), true // D-496 R4：账号级 symbol=NULL
}

// MarginFactSource 低频 margin 采集源（实现 collect.FactSource，D-099；D-006 S1b 帧触发退役）。
// 每 venue 一个实例，由 MT5Adapter.runMarginSource 启动；MarginSink 由 main 经
// BrokerManager 注入（SwapKick 同模式）。复用 a.streams / a.subs / a.withSessionMD /
// swapPerCallTimeout / IsSessionLost / reconnectSession，不新建 gRPC conn。
type MarginFactSource struct {
	a *MT5Adapter

	reconIdx int // 流重连退避档位（成功消费一帧后重置）
}

// Name 实现 collect.FactSource。
func (s *MarginFactSource) Name() string { return "mt_margin:" + s.a.venue }

// Run 阻塞运行：首采兜底 → OnOrderProfit 事件流消费循环。ctx 取消退出。
func (s *MarginFactSource) Run(ctx context.Context, sink func(fact.Fact)) error {
	// a) 首采兜底：立即采一次，失败 5/15/30min 递增重试，3 次封顶，成功后停。
	s.firstPoll(ctx, sink)
	// b) 事件流消费循环：流断 → 重连退避 → 重订阅重开，循环直到 ctx 取消。
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.consumeStream(ctx, sink)
	}
}

// firstPoll 首采兜底：1 次初始 + 至多 3 次递增重试（5/15/30min），成功后停。
// 全部失败 → 打日志，fact 缺省留空（消费侧按 D-006 S1b 悲观档 20x 估，下次重连补采）。
func (s *MarginFactSource) firstPoll(ctx context.Context, sink func(fact.Fact)) {
	for attempt := 0; attempt <= len(marginFirstRetryBackoff); attempt++ {
		if ctx.Err() != nil {
			return
		}
		if f, ok := s.a.marginPollOnce(ctx); ok {
			sink(f)
			return
		}
		// 最后一次不再等待。
		if attempt < len(marginFirstRetryBackoff) {
			select {
			case <-time.After(marginFirstRetryBackoff[attempt]):
			case <-ctx.Done():
				return
			}
		}
	}
	log.Printf("[mt5:%s] margin first poll: %d attempts failed, fact absent (consumers use pessimistic 20x)", s.a.venue, len(marginFirstRetryBackoff)+1)
}

// consumeStream 注册订阅 → 开流 → 消费事件帧循环。流错误 → 会话失效重连 + 退避后返回（外层重开）。
func (s *MarginFactSource) consumeStream(ctx context.Context, sink func(fact.Fact)) {
	a := s.a
	sctx := a.withSessionMD(ctx)
	// SubscribeOrderProfit 注册（幂等，重连后重订阅）。
	if _, err := a.subs.SubscribeOrderProfit(sctx, &mt5pb.SubscribeOrderProfitRequest{Id: a.getToken()}); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt5:%s] margin: subscribe order profit: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	stream, err := a.streams.OnOrderProfit(sctx, &mt5pb.OnOrderProfitRequest{Id: a.getToken()})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt5:%s] margin: onorderprofit stream open: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	onlineReported := false
	for {
		if ctx.Err() != nil {
			return
		}
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt5:%s] margin: stream recv: %v", a.venue, err)
			s.emitStreamState(false)
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt5:%s] margin: reconnect: %v", a.venue, rerr)
				} else if f, ok := a.marginPollOnce(ctx); ok {
					// 重连成功补采一次杠杆（准静态低频第二落点）。
					sink(f)
				}
			}
			s.reconnectBackoff(ctx)
			return // 外层循环重订阅 + 重开流
		}
		s.reconIdx = 0 // 成功消费一帧 → 重置重连退避档位
		// 首帧证明流真活 → 透出 online（每轮流只报一次；venue_online 由此承担，D-571 包A）。
		if !onlineReported {
			s.emitStreamState(true)
			onlineReported = true
		}
		// 包A：帧内 Orders 逐仓载荷下沉（此前过即弃）——patch 语义，
		// 帧外仓不动不删（缺仓只 REST 权威裁决），映射与 QueryOpenedOrders 同源。
		if orders := resp.GetResult().GetOrders(); len(orders) > 0 {
			s.publishPositions(ctx, orders)
		}
		// 包B：ProfitUpdate 六字段帧下沉（此前过即弃 :171 注释自述）——
		// OrderUpdateSummary 同字段帧已走 acctSink（mt_order_events.go:107），本帧补 ProfitUpdate 源。
		if res := resp.GetResult(); res != nil {
			s.publishAcctFrame(res)
		}
		// 帧路径零同步 RPC——margin poll 已退役（杠杆=首采+重连成功两次低频点）。
	}
}

// publishPositions D-571 包A：帧内 Orders → venue.Order（MT5OrdersToOrders 单一映射点）
// → PositionsSink。recover 守卫同 order-events publish（sink panic 不拉崩流循环）。
func (s *MarginFactSource) publishPositions(ctx context.Context, orders []*mt5pb.Order) {
	if s.a.PositionsSink == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[mt5:%s] margin: positions sink panic: %v", s.a.venue, r)
		}
	}()
	vos := MT5OrdersToOrders(s.a.venue, orders)
	s.a.ApplyLastQuote(ctx, vos)
	s.a.PositionsSink(keydomain.Join(s.a.venue, "mt5"), vos)
}

// publishAcctFrame D-571 包B：ProfitUpdate 六字段 → AccountSink（AcctFrameSink 同签名）。
// Currency 无字段传空串（sink 侧不覆盖已有币种）。recover 守卫同 publish 模式。
func (s *MarginFactSource) publishAcctFrame(res *mt5pb.ProfitUpdate) {
	if s.a.AccountSink == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[mt5:%s] margin: account sink panic: %v", s.a.venue, r)
		}
	}()
	s.a.AccountSink(keydomain.Join(s.a.venue, "mt5"), res.Balance, res.Equity, res.Margin, res.FreeMargin, res.Profit, res.MarginLevel, "", time.Now())
}

// emitStreamState D-571 包A：流状态透出（nil 安全 + recover 守卫，同 publish 模式）。
func (s *MarginFactSource) emitStreamState(online bool) {
	if s.a.StreamStateSink == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[mt5:%s] margin: stream-state sink panic: %v", s.a.venue, r)
		}
	}()
	s.a.StreamStateSink(keydomain.Join(s.a.venue, "mt5"), online)
}

// reconnectBackoff 流重连退避（1s/5s/30s 有界，封顶后保持末档）。ctx 取消立即返回。
func (s *MarginFactSource) reconnectBackoff(ctx context.Context) {
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

// runMarginSource 启动 MarginFactSource（MarginSink==nil 时安全 no-op，测试/未注入）。
func (a *MT5Adapter) runMarginSource(ctx context.Context) {
	if a.MarginSink == nil {
		return
	}
	_ = (&MarginFactSource{a: a}).Run(ctx, a.MarginSink)
}

// 编译期断言：MarginFactSource 实现 collect.FactSource。
var _ collect.FactSource = (*MarginFactSource)(nil)
