package adapter

// MT5 margin 采集事件驱动化（D-099，2026-08-24）。
//
// 设计（施工提示词④定稿，勿改）：
//   - 数据源不变：AccountSummary.Leverage → 1/Leverage（无限杠杆 ≥1e6 → 0），S6 返修已实证。
//   - 事件触发器：每 venue 1 条 OnOrderProfit server-streaming 流（SubscribeOrderProfit 注册 +
//     OnOrderProfit 消费）。任何事件帧到达 = 账户活跃/会话活着 → 触发一次 margin 补采。
//     流内 Margin/Balance/Equity/Profit/Orders 字段只当触发器不消费（动态占用保证金 ≠ 杠杆率，
//     无法直接换算；Orders 留待自动平仓立项）。
//   - 首采兜底：venue attach 后立即采一次；失败 → 5/15/30min 递增重试（3 次封顶，成功后停），
//     覆盖空仓账户（无浮盈变化 → 事件稀薄）的首采保证。无常驻定时器（首次成功后事件驱动接管）。
//   - 防抖：距上次成功采集 <5min 的事件直接 skip（静态数据，防事件洪峰重复查库）。
//   - 失败语义：事件触发采集失败 → 打日志（WP1 禁静默），下次事件自然重试——事件驱动天然短重试。
//   - 流重连：RST_STREAM/会话失效（S1 背景）→ 复用 IsSessionLost 判定 + reconnectSession 重连 +
//     重订阅；重连退避 1s/5s/30s 有界（流断是连接层问题，每 tick 有界）。
//   - 解耦：SwapFacts 已删 margin 段——margin 完全独立，swap 的 errs 误伤不再波及 margin。

import (
	"context"
	"log"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/collect"
	"arb/internal/fact"
)

// marginDebounce 事件触发防抖窗口（静态数据，防事件洪峰重复查库）。
// 包级 var 便于测试缩短（默认值即生产值，D-099 设计定稿 5min）。
var marginDebounce = 5 * time.Minute

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

// MarginFactSource 事件驱动 margin 采集源（实现 collect.FactSource，D-099）。
// 每 venue 一个实例，由 MT5Adapter.runMarginSource 启动；MarginSink 由 main 经
// BrokerManager 注入（SwapKick 同模式）。复用 a.streams / a.subs / a.withSessionMD /
// swapPerCallTimeout / IsSessionLost / reconnectSession，不新建 gRPC conn。
type MarginFactSource struct {
	a *MT5Adapter

	lastSuccess time.Time // 防抖基准：最近一次成功采集时间（首采与事件触发共享）
	reconIdx    int       // 流重连退避档位（成功消费一帧后重置）
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
// 全部失败 → 打日志，事件驱动接管（不再等 1h，下次事件即重试）。
func (s *MarginFactSource) firstPoll(ctx context.Context, sink func(fact.Fact)) {
	for attempt := 0; attempt <= len(marginFirstRetryBackoff); attempt++ {
		if ctx.Err() != nil {
			return
		}
		if f, ok := s.a.marginPollOnce(ctx); ok {
			sink(f)
			s.lastSuccess = time.Now()
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
	log.Printf("[mt5:%s] margin first poll: %d attempts failed, event-driven takeover", s.a.venue, len(marginFirstRetryBackoff)+1)
}

// consumeStream 注册订阅 → 开流 → 消费事件帧循环。流错误 → 会话失效重连 + 退避后返回（外层重开）。
func (s *MarginFactSource) consumeStream(ctx context.Context, sink func(fact.Fact)) {
	a := s.a
	sctx := a.withSessionMD(ctx)
	// SubscribeOrderProfit 注册（幂等，重连后重订阅）。
	if _, err := a.subs.SubscribeOrderProfit(sctx, &mt5pb.SubscribeOrderProfitRequest{Id: a.token}); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt5:%s] margin: subscribe order profit: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
	stream, err := a.streams.OnOrderProfit(sctx, &mt5pb.OnOrderProfitRequest{Id: a.token})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("[mt5:%s] margin: onorderprofit stream open: %v", a.venue, err)
		s.reconnectBackoff(ctx)
		return
	}
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
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt5:%s] margin: reconnect: %v", a.venue, rerr)
				}
			}
			s.reconnectBackoff(ctx)
			return // 外层循环重订阅 + 重开流
		}
		// 事件帧到达 = 账户活跃/会话活着 → 触发 margin 补采（流内字段不消费）。
		_ = resp // 仅当触发器，不读 Result 字段
		// 防抖：距上次成功采集 <5min 直接 skip（静态数据，防事件洪峰重复查库）。
		if time.Since(s.lastSuccess) < marginDebounce {
			continue
		}
		if f, ok := a.marginPollOnce(ctx); ok {
			sink(f)
			s.lastSuccess = time.Now()
			s.reconIdx = 0 // 成功消费一帧 → 重置重连退避档位
		} else {
			// 事件触发采集失败 → 打日志（WP1 禁静默），下次事件自然重试。
			log.Printf("[mt5:%s] margin: event-triggered poll failed", a.venue)
		}
	}
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
