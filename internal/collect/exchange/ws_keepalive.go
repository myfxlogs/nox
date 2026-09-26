// ws_keepalive.go —— D-507：公共行情 WS 保活收编 + 重连风暴 fail-loud。
// wsKeepalive 对齐私有流先例（okx_private_ws.go / gate_private_ws.go /
// binance_user_ws.go）：读限只设一次 + pong 续期——gorilla 的 pong 控制帧在
// ReadMessage 内部消化不触发读循环，安静行情窗（tickers 无变化不推）下绝对读限
// 会每周期误断。形态：60s 内 pong 与数据皆无才判死连接（ping 20s < 60s 保活不变）。
package exchange

import (
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// 公共行情 WS 时距（gate/okx 默认；测试可覆写 struct 字段缩小观察窗）。
const (
	quoteWSReadIdle         = 60 * time.Second // 读空闲判死窗（对齐私有流 60s 常量族）
	quoteWSPingEvery        = 20 * time.Second // 客户端 ping 周期（不变）
	quoteWSReconnectBackoff = 5 * time.Second  // 断线退避（不变）
)

// wsKeepalive 连接期调用一次：设初始读限 + pong 到达即续期。
// 对齐 okx_private_ws.go:129-132 先例（_ = SetReadDeadline 沿先例可）。
func wsKeepalive(conn *websocket.Conn, idle time.Duration) {
	_ = conn.SetReadDeadline(time.Now().Add(idle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(idle))
	})
}

// reconnectMeter 重连风暴计量器——事件驱动（仅重连事件 record()，无定时器）。
// 单 goroutine 持有：每 runLoop 一实例、无锁，勿跨 goroutine 共享。
type reconnectMeter struct {
	url        string
	alertFn    func(level, msg string)
	now        func() time.Time
	reconnects []time.Time
	lastWarn   time.Time
}

const (
	reconnectMeterWindow    = time.Hour     // 滑窗
	reconnectMeterThreshold = 6             // 窗内 ≥6 次重连=风暴
	reconnectMeterRewarn    = 2 * time.Hour // 同 URL 告警再发间隔
)

func newReconnectMeter(url string, alertFn func(level, msg string)) *reconnectMeter {
	return &reconnectMeter{url: url, alertFn: alertFn, now: time.Now}
}

// record 记一次重连事件；滑窗计数达阈即发 warn（rewarn 间隔内不重复）。
func (m *reconnectMeter) record() {
	if m.alertFn == nil {
		return
	}
	now := m.now()
	m.reconnects = append(m.reconnects, now)
	cut := now.Add(-reconnectMeterWindow)
	kept := m.reconnects[:0]
	for _, ts := range m.reconnects {
		if ts.After(cut) {
			kept = append(kept, ts)
		}
	}
	m.reconnects = kept
	if len(m.reconnects) >= reconnectMeterThreshold &&
		(m.lastWarn.IsZero() || now.Sub(m.lastWarn) >= reconnectMeterRewarn) {
		m.alertFn("warn", fmt.Sprintf("[ws-storm] %s 1小时内重连≥%d次（读限/pong/链路异常）", m.url, reconnectMeterThreshold))
		m.lastWarn = now
	}
}
