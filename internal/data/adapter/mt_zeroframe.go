package adapter

import (
	"context"
	"fmt"
	"log"
	"time"
)

// 零帧看门狗参数（包级 var 同 marginReconnectBackoff 模式，测试可缩短）。
var (
	zeroFrameCheckInterval = 30 * time.Second // 看门狗检查周期
	zeroFrameHeartbeatTTL  = 30 * time.Second // lastAnyFrame 新鲜窗（心跳仍在=流活）
	zeroFrameSuspectTTL    = 90 * time.Second // 无真帧超此值+心跳活=疑似终端推送死
	zeroFrameRecoveryTTL   = 60 * time.Second // stage-2 重连后真帧恢复观察窗
)

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
	// 判定两型（stage-2 公共段合一）：
	// ①零帧——整流无一帧（lastAny 恒零，含心跳也无）自基线 >TTL → 流开不出/订阅楔死
	//   疑似（D-563 S5，D6 实证盲孔：lastAny 恒零使判定块整段跳过=静默死）；
	// ②心跳活但真帧死 >TTL → 终端推送死疑似（周末休市=心跳停→lastAny 陈旧不入②不误报）。
	zeroFrame := a.zfLastAnyFrameAt.IsZero() && now.Sub(a.zfStartedAt) > zeroFrameSuspectTTL
	heartbeatDead := !a.zfLastAnyFrameAt.IsZero() && now.Sub(a.zfLastAnyFrameAt) <= zeroFrameHeartbeatTTL
	realBase := a.zfLastRealFrameAt
	if realBase.IsZero() {
		realBase = a.zfStartedAt // 从未收真帧：从看门狗基线起算
	}
	if zeroFrame || (heartbeatDead && now.Sub(realBase) > zeroFrameSuspectTTL) {
		if !a.zfAlerted {
			a.zfAlerted = true
			if zeroFrame {
				alert = fmt.Sprintf("mt5:%s quote stream zeroframe suspect: no frame at all since watchdog start %s（流开不出/订阅楔死疑似，KB: mtapi-onquote-zeroframe-terminal-dead）", a.venue, now.Sub(a.zfStartedAt).Round(time.Second))
			} else {
				alert = fmt.Sprintf("mt5:%s quote stream zeroframe suspect: heartbeats alive, no real frame for %s（终端推送死疑似，KB: mtapi-onquote-zeroframe-terminal-dead）", a.venue, now.Sub(realBase).Round(time.Second))
			}
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
	a.zfMu.Unlock()
	// venue 级闭市闸——session 判闭市=静默预期不发警（表缺席/判定不了=照报不误赦）。
	if alert != "" && a.ClosedCheck != nil && a.ClosedCheck() {
		alert = ""
	}
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
