// private_ws_probe.go —— D-166 私有流控制面探针（decisions.md D-156 裁定实施）。
// 判死语义=服务端订阅面存活证（控制帧 ack），非「无事件=死」（私有流安静常态）。
// 纯控制帧不携业务数据=D-004 看门狗合规形态；REST 双轨兜底不动。
package exchange

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// subProbe per-channel pending→ack 追踪（OKX 同连接重订阅探针状态机）。
// sent 记 pending；ack 清位；overdue 列超界未达频道=订阅面疑死。
type subProbe struct {
	mu      sync.Mutex
	pending map[string]time.Time
}

func newSubProbe() *subProbe { return &subProbe{pending: make(map[string]time.Time)} }

func (p *subProbe) sent(chs []string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ch := range chs {
		p.pending[ch] = now
	}
}

func (p *subProbe) ack(ch string) {
	p.mu.Lock()
	delete(p.pending, ch)
	p.mu.Unlock()
}

// overdue pending 中超 bound 未 ack 的频道（判疑死）。
func (p *subProbe) overdue(now time.Time, bound time.Duration) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for ch, sent := range p.pending {
		if now.Sub(sent) > bound {
			out = append(out, ch)
		}
	}
	return out
}

// chanSilence per-channel 末帧时刻+warn 冷却（gate 静默 warn 档——静≠死仅可观测性，
// 判死仍靠重连探针的 ack 证据面）。
type chanSilence struct {
	mu   sync.Mutex
	last map[string]time.Time
	warn map[string]time.Time
}

func newChanSilence() *chanSilence {
	return &chanSilence{last: make(map[string]time.Time), warn: make(map[string]time.Time)}
}

func (c *chanSilence) touch(ch string, now time.Time) {
	c.mu.Lock()
	c.last[ch] = now
	c.mu.Unlock()
}

// due 静默超 idle 且距上次 warn≥cooldown 的频道列（各记冷却位）。
func (c *chanSilence) due(chs []string, now time.Time, idle, cooldown time.Duration) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ch := range chs {
		last, seen := c.last[ch]
		if !seen || now.Sub(last) < idle || now.Sub(c.warn[ch]) < cooldown {
			continue
		}
		c.warn[ch] = now
		out = append(out, ch)
	}
	return out
}

// 探针周期（venue 幂等面：OKX 同连接重订阅幂等已证【decisions.md D-156】，
// gate 私有频道未证→周期重连重订阅=无条件幂等）。包级 var 便于测试缩短。
var (
	okxPrivateProbeInterval  = 30 * time.Minute
	gatePrivateProbeInterval = 30 * time.Minute
	// okxProbeAckTimeout/okxProbeCheckEvery：ack 超 2×readIdle 疑死界+检出粒度（票面钦定值，
	// 包级 var 供测试缩短同 D-154/D-507 先例）。
	okxProbeAckTimeout = 2 * okxPrivateReadIdle
	okxProbeCheckEvery = okxPrivateReadIdle / 2
	// gatePrivateSilentAfter/Cooldown per-channel 静默 warn 档（可观测性非判死——
	// 订单/持仓频道安静属常态，warn 只给「该频道久未出声」线索）。
	gatePrivateSilentAfter  = 24 * time.Hour
	gatePrivateWarnCooldown = 24 * time.Hour
)

// okxProbeChannels 探针目标键集：频道名；带 instType 的 arg 以 channel:instType 为键
// （OKX 各 arg 独立回 ack——orders SWAP/SPOT 双 arg 各自证活）。
func okxProbeChannels() []string {
	var out []string
	for _, a := range okxPrivateSubscribeArgs() {
		k := a["channel"]
		if it := a["instType"]; it != "" {
			k += ":" + it
		}
		out = append(out, k)
	}
	return out
}

// okxAckKey subscribe ack 帧 → 探针键（wsMsg.Arg 仅 channel 时退纯频道名）。
func okxAckKey(channel, instType string) string {
	if instType != "" {
		return channel + ":" + instType
	}
	return channel
}

// probeSubLoop OKX 控制面探针：周期 T 重发全频道 op:subscribe（幂等），逐频道
// pending→ack；ack 超 2×readIdle 未达=订阅面疑死 → warn+MarkDirty+conn.Close 主动重连
// （重连后 subscribe 就绪触发 onReconnect 对账 kick；纯控制帧零业务数据）。
func (o *OKXPrivateWS) probeSubLoop(ctx context.Context, conn *websocket.Conn, done <-chan struct{}, probe *subProbe) {
	ticker := time.NewTicker(okxPrivateProbeInterval)
	defer ticker.Stop()
	check := time.NewTicker(okxProbeCheckEvery) // ack 疑死检出粒度独立于 30min 发射节拍
	defer check.Stop()
	for {
		select {
		case <-ticker.C:
			chs := okxProbeChannels()
			o.writeMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
			err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": okxPrivateSubscribeArgs()})
			o.writeMu.Unlock()
			if err != nil {
				return // 写死=连接已败，读循环自行判死重连
			}
			probe.sent(chs, time.Now())
		case <-check.C:
			if dead := probe.overdue(time.Now(), okxProbeAckTimeout); len(dead) > 0 {
				log.Printf("[okx-private-ws] subscribe probe ack 超时未达 %v（订阅面疑死→warn+主动重连）", dead)
				o.balLedger.MarkDirty("okx:unified")
				_ = conn.Close() // 读循环报错→runOnce 返回→Run 重连+重订阅+onReconnect kick
				return
			}
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// gateProbeLoop Gate 控制面探针：周期 T conn.Close→runOnce 返回→Run 重连重订阅
// （重连幂等无条件；重订阅 ack 帧即订阅面存活证）。per-channel 静默 warn 并挂——
// 静≠死仅线索，判死仍靠重连后 ack 面。
func (g *GatePrivateWS) probeLoop(ctx context.Context, conn *websocket.Conn, done <-chan struct{}, sil *chanSilence) {
	ticker := time.NewTicker(gatePrivateProbeInterval)
	defer ticker.Stop()
	warnTick := time.NewTicker(time.Minute)
	defer warnTick.Stop()
	for {
		select {
		case <-ticker.C:
			log.Printf("[gate-ws] probe: 周期重连重订阅（订阅面探活）")
			_ = conn.Close()
			return
		case <-warnTick.C:
			if chs := sil.due(gatePrivateChannels, time.Now(), gatePrivateSilentAfter, gatePrivateWarnCooldown); len(chs) > 0 {
				log.Printf("[gate-ws] per-channel 静默 warn: %v（%s 无帧；静≠死仅线索，判死靠重连探针）", chs, gatePrivateSilentAfter)
			}
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}
