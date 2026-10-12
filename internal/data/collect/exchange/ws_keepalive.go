// ws_keepalive.go —— D-507：公共行情 WS 保活收编 + 重连风暴 fail-loud。
// wsKeepalive 对齐私有流先例（okx_private_ws.go / gate_private_ws.go /
// binance_user_ws.go）：读限只设一次 + pong 续期——gorilla 的 pong 控制帧在
// ReadMessage 内部消化不触发读循环，安静行情窗（tickers 无变化不推）下绝对读限
// 会每周期误断。形态：60s 内 pong 与数据皆无才判死连接（ping 20s < 60s 保活不变）。
package exchange

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 公共行情 WS 时距（gate/okx 默认；测试可覆写 struct 字段缩小观察窗）。
const (
	quoteWSReadIdle         = 60 * time.Second // 读空闲判死窗（对齐私有流 60s 常量族）
	quoteWSPingEvery        = 20 * time.Second // 客户端 ping 周期（不变）
	quoteWSReconnectBackoff = 5 * time.Second  // 断线退避（不变）
	// quoteWSDataIdle 数据帧级判死窗（WSSpec.DataIdle 默认）——须 >> ReadIdle：
	// pong 续期会遮蔽 ReadIdle，此处专门兜「控制面活/数据面死」僵尸连接
	// （D-5xx：mihomo 重启切流后 okx-ws 控制帧畅通但数据帧零达 18min 零日志实证）。
	// 10min 仍远小于安静市场可容忍窗？——不，对多符号 tickers 聚合流 10min
	// 全静默即异常；安静窗由 ReadIdle+pong 语义承接，本窗只判数据帧缺席。
	quoteWSDataIdle = 10 * time.Minute
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
	totalCount int64 // D-512 T4：累计重连（registry 透出用，与滑窗计数分离）
}

const (
	reconnectMeterWindow    = time.Hour     // 滑窗
	reconnectMeterThreshold = 6             // 窗内 ≥6 次重连=风暴
	reconnectMeterRewarn    = 2 * time.Hour // 同 URL 告警再发间隔
)

func newReconnectMeter(url string, alertFn func(level, msg string)) *reconnectMeter {
	m := &reconnectMeter{url: url, alertFn: alertFn, now: time.Now}
	registerMeter(m) // D-512 T4：构造时自注册 registry
	return m
}

// totalReconns 累计重连数（registry 快照用）。
func (m *reconnectMeter) totalReconns() int64 { return m.totalCount }

// record 记一次重连事件；滑窗计数达阈即发 warn（rewarn 间隔内不重复）。
// ②：inCooldownLoop=true（候选冷却循环中）的重连=数据看门狗确定性杀，
// 非链路抖动——不入滑窗不计风暴阈（零帧死通道的降噪归 ws-dead 升级告警域）；
// 累计计数与事件扇出照常（sourceHealth 显示真值不受影响）。
// F-1：尾部 OnReconnectEvent 扇出——快照行变化（totalCount+1）须
// 驱动 sourceHealth 域重组装发射，否则计数变了事件不发=死踢。
func (m *reconnectMeter) record(inCooldownLoop bool) {
	if !inCooldownLoop {
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
		if m.alertFn != nil && len(m.reconnects) >= reconnectMeterThreshold &&
			(m.lastWarn.IsZero() || now.Sub(m.lastWarn) >= reconnectMeterRewarn) {
			m.alertFn("warn", fmt.Sprintf("[ws-storm] %s 1 小时内重连 ≥%d 次（读取超时/心跳无回应/链路异常）", m.url, reconnectMeterThreshold))
			m.lastWarn = now
		}
	}
	m.totalCount++
	if fn := OnReconnectEvent; fn != nil {
		fn()
	}
}

// OnReconnectEvent D-571C-R2 F-1：每次重连事件扇出钩子（main 接 posMon.OnHealthDirty——
// 快照行 totalCount/lastWarn 变化即重组装 diff 发射；nil=不扇出）。包级单点：
// meter 在各自 runLoop 内构造，main 拿不到实例句柄。
var OnReconnectEvent func()

// ===== D-512 T4：meter 注册面（自建轻量 registry）=====
// Claude 会签①：D-507 实况无 registry——meter 为 runLoop 局部实例。本节补构造时
// 自注册的包级 registry + SnapshotReconnects() 聚合口（观测透出用，只读）。

// ReconnectSnapshot 单 WS 源重连计量快照（url/累计重连/末次警告）。
type ReconnectSnapshot struct {
	URL          string
	TotalReconns int64
	LastWarn     time.Time // 零值=从未达风暴阈
}

var (
	meterRegMu    sync.Mutex
	meterRegistry = map[string]*reconnectMeter{} // url → meter（构造时自注册）
)

// registerMeter 构造时自注册（newReconnectMeter 调用；同 url 复用实例防重复计数）。
func registerMeter(m *reconnectMeter) {
	meterRegMu.Lock()
	if _, ok := meterRegistry[m.url]; !ok {
		meterRegistry[m.url] = m
	}
	meterRegMu.Unlock()
}

// SnapshotReconnects 全源重连计量快照（dashboard 取数渲染；只读聚合）。
func SnapshotReconnects() []ReconnectSnapshot {
	meterRegMu.Lock()
	defer meterRegMu.Unlock()
	out := make([]ReconnectSnapshot, 0, len(meterRegistry))
	for _, m := range meterRegistry {
		out = append(out, ReconnectSnapshot{URL: m.url, TotalReconns: m.totalReconns(), LastWarn: m.lastWarn})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}
