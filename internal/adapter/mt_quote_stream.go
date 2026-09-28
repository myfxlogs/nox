package adapter

import (
	"context"
	"fmt"
	"log"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/bus"
	"arb/internal/safego"
)

// D-432 零帧看门狗参数（包级 var 同 marginReconnectBackoff 模式，测试可缩短）。
var (
	zeroFrameCheckInterval = 30 * time.Second // 看门狗检查周期
	zeroFrameHeartbeatTTL  = 30 * time.Second // lastAnyFrame 新鲜窗（心跳仍在=流活）
	zeroFrameSuspectTTL    = 90 * time.Second // 无真帧超此值+心跳活=疑似终端推送死
	zeroFrameRecoveryTTL   = 60 * time.Second // stage-2 重连后真帧恢复观察窗
)

// saveSubSet 初始化订阅集镜像（Subscribe 时调用，覆盖写）。
func (a *MT5Adapter) saveSubSet(symbols []string) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	a.subSet = make(map[string]bool, len(symbols))
	for _, s := range symbols {
		a.subSet[s] = true
	}
}

// addSubSet 增补符号到订阅集（Resubscribe 时调用，append）。
func (a *MT5Adapter) addSubSet(symbols []string) {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	if a.subSet == nil {
		a.subSet = make(map[string]bool)
	}
	for _, s := range symbols {
		a.subSet[s] = true
	}
}

// snapshotSubSet 返回当前订阅集快照（流重连 resubscribeAll 用，避免持锁调 RPC）。
func (a *MT5Adapter) snapshotSubSet() []string {
	a.subMu.Lock()
	defer a.subMu.Unlock()
	out := make([]string, 0, len(a.subSet))
	for s := range a.subSet {
		out = append(out, s)
	}
	return out
}

// resubscribeAll 用当前订阅集全量重订阅（OnQuote 流重连后调用，恢复网关侧订阅状态）。
// mtapi Subscribe 幂等，全量重订阅安全。
func (a *MT5Adapter) resubscribeAll(sctx context.Context) {
	for _, sym := range a.snapshotSubSet() {
		if _, err := a.subs.Subscribe(sctx, &mt5pb.SubscribeRequest{
			Id:     a.getToken(),
			Symbol: sym,
		}); err != nil {
			log.Printf("[mt5:%s] resubscribe %s: %v", a.venue, sym, err)
		}
	}
}

// runQuoteStream OnQuote 流消费外层循环：开流 → 消费帧 → 断流退避重开，直到 ctx 取消。
// ch 仅在此 defer close——重连不关 ch，下游 broker_manager_adapters 的 for-range 持续接收。
func (a *MT5Adapter) runQuoteStream(ctx context.Context, ch chan<- bus.Quote) {
	// D-432：零帧看门狗随 runQuoteStream 生命周期（episode 跨流重连续存）。
	// zfDone 先于 close(ch) 收口——ch 关闭即看门狗已退出（T6 不泄漏语义由此保证）。
	a.zfReset()
	zfDone := make(chan struct{})
	safego.Go("mt5:zeroframeWatchdog", func() { defer close(zfDone); a.zeroframeWatchdog(ctx) })
	defer func() { <-zfDone; close(ch) }()
	for {
		if ctx.Err() != nil {
			return
		}
		sctx, streamCancel := context.WithCancel(a.withSessionMD(ctx))
		a.zfSetCancel(streamCancel)
		a.resubscribeAll(sctx)
		stream, err := a.streams.OnQuote(sctx, &mt5pb.OnQuoteRequest{Id: a.getToken()})
		if err != nil {
			streamCancel()
			a.zfSetCancel(nil)
			if ctx.Err() != nil {
				return
			}
			log.Printf("[mt5:%s] onquote stream open: %v", a.venue, err)
			a.quoteReconnectBackoff(ctx)
			continue
		}
		ok := a.consumeQuoteFrames(ctx, stream, ch)
		streamCancel()
		a.zfSetCancel(nil)
		if !ok {
			return // ctx 取消
		}
		// 流断 → 外层重开
	}
}

// publishSymbol D-560 S1：emitBlocked 命中（多行组非 primary 变体/零 primary 歧义组
// 成员）→ 降格 brokerSym 原生键发布；否则 canonical 原样。blocked=nil→原样。
// 照 D-383「native 照发」裁定：变体 tick 不丢，只禁占 canonical 槽。
func publishSymbol(brokerSym, canon string, blocked map[string]bool) string {
	if blocked[brokerSym] {
		return brokerSym
	}
	return canon
}

// consumeQuoteFrames 内层 Recv 循环。返回 false = ctx 取消（外层退出）；true = 流断需重连。
// 流断时若 IsSessionLost → 复用 S1 reconnectSession 重建 account 会话（margin 流同模式）。
func (a *MT5Adapter) consumeQuoteFrames(ctx context.Context, stream mt5pb.Streams_OnQuoteClient, ch chan<- bus.Quote) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			log.Printf("[mt5:%s] onquote recv: %v", a.venue, err)
			if IsSessionLost(err) {
				if rerr := a.reconnectSession(ctx); rerr != nil {
					log.Printf("[mt5:%s] onquote reconnect: %v", a.venue, rerr)
				}
			}
			a.quoteReconnectBackoff(ctx)
			log.Printf("[mt5:%s] onquote stream reopened", a.venue)
			return true
		}
		q := resp.GetResult()
		// D-432 S1：帧级计数——任何成功 Recv 记 anyFrame；symbol 非空且 bid/ask
		// 至少一腿非零才算真实报价帧（心跳 q==nil、空 symbol、零价帧均不算）。
		a.zfNoteFrame(q != nil && q.GetSymbol() != "" && (q.GetBid() > 0 || q.GetAsk() > 0))
		if q == nil {
			continue
		}
		brokerSym := q.GetSymbol()
		// 实时报价流统一用本地接收时间——mtapi 网关按经纪商服务器时区编码
		// time（FxPro UTC+3 等），各经纪商偏移各异无法通用补偿；本机时钟相对
		// freshness TTL（60s）毫秒级误差可忽略。
		ts := time.Now()
		ch <- bus.Quote{
			// D-560 S1：报价槽 primary 钉住——emitBlocked 变体/歧义组成员降格
			// 原生键发布，canonical 槽恒 primary（last-write-wins 占槽拒单根因）。
			Symbol:      publishSymbol(brokerSym, a.canonical(brokerSym), a.emitBlockedSet()),
			Venue:       a.venue,
			VenueSymbol: brokerSym,
			VenueType:   "mt5",
			Bid:         q.GetBid(),
			Ask:         q.GetAsk(),
			Time:        ts,
		}
		a.quoteReconIdx = 0    // 成功消费一帧 → 重置重连退避档位
		a.quoteAlerted = false // D-104 P2 U2：成功帧重置 episode 告警
	}
}

// quoteReconnectBackoff OnQuote 流重连退避（复用 marginReconnectBackoff 1s/5s/30s，同因同退避）。
// 封顶后保持末档；ctx 取消立即返回。D-104 P2 U2：封顶档 episode 告警（去重，成功帧后重置）。
func (a *MT5Adapter) quoteReconnectBackoff(ctx context.Context) {
	idx := a.quoteReconIdx
	capped := idx >= len(marginReconnectBackoff)
	if capped {
		idx = len(marginReconnectBackoff) - 1
		// D-104 P2 U2：封顶档 episode 告警（连续封顶只告警一次，成功帧后 quoteAlerted 重置可再告）。
		if !a.quoteAlerted && a.AlertSink != nil {
			a.quoteAlerted = true
			a.AlertSink("warn", fmt.Sprintf("mt5:%s quote stream unstable: %d consecutive reconnects", a.venue, a.quoteReconIdx))
		}
	}
	d := marginReconnectBackoff[idx]
	a.quoteReconIdx++
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// --- D-432：OnQuote 零帧死推检测告警（zeroframe watchdog） ---
// 失效形态（KB mtapi-onquote-zeroframe-terminal-dead）：流建连/订阅成功、Recv 无 error，
// 但只吐 {} 心跳空帧、零真实报价帧——断流重连机制够不着（它只认 Recv error）。
// 判定矩阵：心跳活+真帧死>90s → stage-1 warn（episode 去重）+stage-2 单发强制重连；
// 流静止（心跳也停，含周末休市）→ 不动作（Recv error 路径已覆盖）；真帧恢复 → episode 清零。

// zfReset 看门狗基线初始化（runQuoteStream 起调，episode 跨流重连续存不重置）。
func (a *MT5Adapter) zfReset() {
	a.zfMu.Lock()
	defer a.zfMu.Unlock()
	a.zfStartedAt = time.Now()
	a.zfLastAnyFrameAt = time.Time{}
	a.zfLastRealFrameAt = time.Time{}
	a.zfAlerted = false
	a.zfReconnectAt = time.Time{}
	a.zfPersistWarned = false
	a.zfCancelStream = nil
}

// zfSetCancel 登记当前流取消器（stage-2 强制重连用；nil=流空窗期）。
func (a *MT5Adapter) zfSetCancel(c context.CancelFunc) {
	a.zfMu.Lock()
	defer a.zfMu.Unlock()
	a.zfCancelStream = c
}

// zfNoteFrame 帧级计数（consumeQuoteFrames 每帧成功 Recv 调用）。
// 真帧到达即清零 episode 标记；stage-2 后恢复记 info 证据行。
func (a *MT5Adapter) zfNoteFrame(real bool) {
	a.zfMu.Lock()
	a.zfLastAnyFrameAt = time.Now()
	var recovered bool
	var hadReconnect bool
	if real {
		a.zfLastRealFrameAt = a.zfLastAnyFrameAt
		recovered = a.zfAlerted || !a.zfReconnectAt.IsZero()
		hadReconnect = !a.zfReconnectAt.IsZero()
		a.zfAlerted = false
		a.zfReconnectAt = time.Time{}
		a.zfPersistWarned = false
	}
	a.zfMu.Unlock()
	if recovered && hadReconnect {
		log.Printf("[mt5:%s] zeroframe recovered after reconnect", a.venue)
	} else if recovered {
		log.Printf("[mt5:%s] zeroframe recovered", a.venue)
	}
}

// zeroframeWatchdog 每 adapter 一条，随 runQuoteStream 生命周期；ctx 取消即退出。
func (a *MT5Adapter) zeroframeWatchdog(ctx context.Context) {
	t := time.NewTicker(zeroFrameCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.zfCheck()
		}
	}
}

// zfCheck 单拍判定（锁内只算状态，sink/cancel/log 出锁执行防回调重入死锁）。
func (a *MT5Adapter) zfCheck() {
	a.zfMu.Lock()
	now := time.Now()
	var alert string
	var cancel context.CancelFunc
	var persist bool
	// 心跳活（lastAny 新鲜）才进判定；流静止=传输/连接问题归 Recv error 路径，不误报
	//（周末休市特征=心跳也停，同落本行）。
	if !a.zfLastAnyFrameAt.IsZero() && now.Sub(a.zfLastAnyFrameAt) <= zeroFrameHeartbeatTTL {
		realBase := a.zfLastRealFrameAt
		if realBase.IsZero() {
			realBase = a.zfStartedAt // 从未收真帧：从看门狗基线起算
		}
		if realAge := now.Sub(realBase); realAge > zeroFrameSuspectTTL {
			if !a.zfAlerted {
				a.zfAlerted = true
				alert = fmt.Sprintf("mt5:%s quote stream zeroframe suspect: heartbeats alive, no real frame for %s（终端推送死疑似，KB: mtapi-onquote-zeroframe-terminal-dead）", a.venue, realAge.Round(time.Second))
			}
			if a.zfReconnectAt.IsZero() {
				// stage-2 单发：取消当前流 → consume Recv 返 Canceled → 外层循环重开
				//（不重建 session）。本 episode 仅此一次，防重连风暴。
				a.zfReconnectAt = now
				cancel = a.zfCancelStream
			} else if !a.zfPersistWarned && now.Sub(a.zfReconnectAt) > zeroFrameRecoveryTTL {
				a.zfPersistWarned = true
				persist = true
			}
		}
	}
	a.zfMu.Unlock()
	if alert != "" && a.AlertSink != nil {
		a.AlertSink("warn", alert)
	}
	if cancel != nil {
		log.Printf("[mt5:%s] zeroframe stage-2: forcing stream reopen (single shot)", a.venue)
		cancel()
	}
	if persist {
		log.Printf("[mt5:%s] zeroframe persists after reconnect——终端侧死，重连不治本", a.venue)
	}
}
