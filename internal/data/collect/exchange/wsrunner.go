// wsrunner.go —— D-511 S1：公共行情 WS 共享骨架（三所收编）。
// 三所 WS 适配器（okx_ws/gate_ws/binance_market_ws）骨架同构：
// dial→subscribe→读循环→重连→ping，venue 差异只在钩子（URL/订阅帧/帧分发/增量补订）。
// 骨架行为一处钉死：读续期（任意帧或 pong 即续期，走 wsKeepalive 同件）、
// ping 经 Sender 单写者锁、退避重连重订阅、OnReconnect 计 reconnectMeter 风暴
// fail-loud（D-507 语义；D-074 ② 冷却循环中的看门狗确定性重连不入 storm 滑窗）、
// 冷却循环升级「通道死」告警（D-074 ①）、ctx 取消即刻退出。
// 安全契（D-450/R3）：骨架不自行取 httpx.WSDialer 默认 header——spec.Header==nil
// =显式无鉴权头直连，token 永不泄给原生 venue 域；dialer/header 由适配层全权供给。
package exchange

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"nox/internal/infra/dephealth"
	"nox/internal/platform/safego"

	"github.com/gorilla/websocket"
)

// Sender 单写者契约封装（mu 内写，防 ping goroutine 与订阅帧交叉）。
type Sender interface {
	Send(frame any) error
	SendRaw(messageType int, data []byte) error
}

// wsWriteDeadline D-587：WS 写路径有界——半死 TCP（不返 FIN/RST）下裸写可楔死整条
// 写链（mu 持锁下 ping/订阅/控制帧全堵）；写超时显著小于读 idle，10s 对齐
// monitorCallTimeout 量级。gorilla SetWriteDeadline 只作用下一次写，每写前重设。
const wsWriteDeadline = 10 * time.Second

// wsSender Sender 实现：mu 内嵌值（非指针），ping 与订阅帧共用同一把锁。
type wsSender struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *wsSender) Send(frame any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
	return s.conn.WriteJSON(frame)
}

func (s *wsSender) SendRaw(messageType int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
	return s.conn.WriteMessage(messageType, data)
}

// WSSpec 公共行情 WS 规格（venue 差异收敛为钩子）。
type WSSpec struct {
	Name string // meter/log 标（如 "okx-ws"/"gate-ws:url"）
	URL  string // 完整 wss:// 路由（适配层已过 wsViaProxy）
	// URLs 有序候选地址表（cex-proxy 双端点 failover）：非空时代替 URL 字段逐序试拨——
	// 每次连接尝试都从首元素起（主端优先，回切无粘滞=主端恢复即自动回主）。
	URLs   []string
	Dialer *websocket.Dialer // 适配层供给（wsDialer/httpx.WSDialer）；nil=裸默认直连
	Header http.Header       // 显式握手鉴权头（binance=x-proxy-token；nil=显式无头）
	// Candidates 候选级装配（D-541 S3）：非空时代替 URLs/URL+Dialer/Header——
	// 每候选独立拨号件（直连兜底位=裸 dialer+nil header）。
	Candidates []WSCandidate
	// 冷却降级（D-541 S3，测试可覆写）：同候选连续 CooldownKills 次数据看门狗杀
	// →冷却 CooldownWindow（冷却期 dial 跳过该候选）；到期自动回主——主端恒优先，
	// 兜底只在主端僵尸期顶位。零值=默认 2 次/30min。
	CooldownKills  int
	CooldownWindow time.Duration

	InitialSubs func() []any // 首连+每重连订阅帧集（逐帧经 Sender.Send 发出）
	// OnFrame D-154：数据帧分发+业务帧判定——返回「本连接业务目的产出帧」
	// （okx=tickers 产出 quote；gate=产出 quote 的 update；binance=被消费的本类帧）。
	// 仅 true 帧刷新 lastDataAt/sawData——ack/无关帧/funding 帧/解析失败帧不续期
	// （服务端订阅态半丢时杂帧流遮 dataIdle=僵尸不自愈，生产实证 10-09）。
	// nil=全帧计（wsrunner 骨架测试等向后兼容锚）。
	OnFrame     func(data []byte) bool
	OnConnect   func()         // 可选：每连接拨号成功后回调（差集表等每连接状态重置走此）
	OnAliveTick func(s Sender) // 可选：每 ping tick 回调（okx/gate 差集补订走此）
	// OnReconnect 可选：重连错误事件（reconnectMeter.record 走此）。入参=候选冷却
	// 循环中（D-074 ②：true=看门狗确定性杀，storm 判定排除）。
	OnReconnect func(inCooldownLoop bool)
	// DepKey D-579 S2：dephealth 记账键（"venue:ws-频道族"）；空=不记（向后兼容）。
	DepKey string
	// ①：通道死升级告警——同流连续 DeadRounds 轮冷却（各候选均零数据帧）
	// 发单条 warn「通道死（候选全零帧）」，DeadRewarn 内去重（零值=3 轮/6h）。
	// AlertFn=适配层告警管道（与 reconnectMeter 同件，nil=不告警仅冷却）。
	AlertFn    func(level, msg string)
	DeadRounds int
	DeadRewarn time.Duration

	// 时距（零值=ws_keepalive.go 常量族；测试覆写缩小观察窗，对齐 D-507 字段化先例）。
	ReadIdle         time.Duration
	PingEvery        time.Duration
	ReconnectBackoff time.Duration
	// DataIdle 数据帧级看门狗（区别于 ReadIdle 的 ping/pong 层判死）：
	// 控制面存活（pong 正常）但零数据帧持续超此窗 → 强制断开走重连——
	// 补「订阅态丢失/上游静默」僵尸连接的兜底，安静行情窗由较短 pong 判死先兜，
	// 本窗须 >> ReadIdle 才不误杀安静窗（默认 quoteWSDataIdle）。
	DataIdle time.Duration
}

// RunWS 公共骨架入口：dial→OnConnect→InitialSubs→ping 保活→读循环→退避重连→阻塞至 ctx 取消。
// 双连接（gate spot+futures / binance perp+spot）由调用方启两个 goroutine 各传一 WSSpec。
func RunWS(ctx context.Context, spec WSSpec) {
	backoff := spec.ReconnectBackoff
	if backoff == 0 {
		backoff = quoteWSReconnectBackoff
	}
	st := &wsCandState{} // 冷却记忆跨重连持久（D-541 S3）
	st.cur.Store(-1)
	for {
		if err := runWSOnceSt(ctx, spec, st); err != nil {
			dephealth.Mark(spec.DepKey, false, err) // D-579：应用层成败事实记账
			if spec.OnReconnect != nil {
				// meter fail-loud（每连接一 meter，单 goroutine 持有）；
				// ②：冷却循环中=true——看门狗确定性重连不入 storm 滑窗。
				spec.OnReconnect(st.cooldownLoopActive())
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				log.Printf("[%s] reconnect after error: %v", spec.Name, err)
			}
		}
	}
}

// runWSOnce 单次连接（无记忆：测试直调——新建冷却态等价语义）。
func runWSOnce(ctx context.Context, spec WSSpec) error {
	st := &wsCandState{}
	st.cur.Store(-1)
	return runWSOnceSt(ctx, spec, st)
}

// runWSOnceSt 单次连接：dial（候选级，冷却序选+逐候选拨号件）→OnConnect→
// 逐帧发 InitialSubs→ping 保活（数据看门狗杀记候选冷却/早候选到期回主）→读循环。
func runWSOnceSt(ctx context.Context, spec WSSpec, st *wsCandState) error {
	cands := specCandidates(spec)
	var conn *websocket.Conn
	var err error
	var lastURL string
	st.cur.Store(-1)
	st.dataKil.Store(false)
	st.sawData.Store(false) // D-074 ①：每连接重置数据帧见证（通道活过证据）
	defer st.noteExit(spec) // 归因只在主流程这一处写
	for _, i := range st.dialOrder(len(cands)) {
		c := cands[i]
		lastURL = c.URL
		dialer := c.Dialer
		if dialer == nil {
			dialer = &websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second}
		}
		conn, _, err = dialer.DialContext(ctx, c.URL, c.Header)
		if err == nil {
			st.cur.Store(int32(i))
			break
		}
		if ctx.Err() != nil {
			break // ctx 取消——不再试下一候选
		}
		log.Printf("[%s] dial %s failed: %v（尝试下一候选）", spec.Name, c.URL, err)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", lastURL, err)
	}
	defer conn.Close()

	dephealth.Mark(spec.DepKey, true, nil) // D-579：拨号成功标 ok（进入读循环前防 half-open 假绿）

	if spec.OnConnect != nil {
		spec.OnConnect()
	}

	// sender 持本连接唯一写锁——InitialSubs/ping/OnAliveTick 补订帧全经同一锁（单写者契约）。
	sender := &wsSender{conn: conn}
	if spec.InitialSubs != nil {
		for _, frame := range spec.InitialSubs() {
			if err := sender.Send(frame); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
		}
	}

	readIdle := spec.ReadIdle
	if readIdle == 0 {
		readIdle = quoteWSReadIdle
	}
	// 订阅成功后设一次+pong 续期（wsKeepalive 同件，读循环不设逐读绝对读限）。
	wsKeepalive(conn, readIdle)

	dataIdle := spec.DataIdle
	if dataIdle == 0 {
		dataIdle = quoteWSDataIdle
	}
	var lastDataAt atomic.Int64             // unixnano；数据帧到达即更新，ping goroutine 判读
	lastDataAt.Store(time.Now().UnixNano()) // 连接起点=订阅完成时刻，防握手窗误杀

	pingEvery := spec.PingEvery
	if pingEvery == 0 {
		pingEvery = quoteWSPingEvery
	}
	done := make(chan struct{})
	defer close(done)
	safego.GoPlain(spec.Name+":ws-ping", func() {
		ticker := time.NewTicker(pingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := sender.SendRaw(websocket.PingMessage, nil); err != nil {
					return
				}
				// 数据帧看门狗：pong 续期证明 TCP/WS 控制面活着，但数据帧
				// 长期缺席=订阅态丢失/上游静默（readIdle 被 pong 遮蔽兜不住）——
				// Close 使读循环报错，走既有退避重连。
				if elapsed := time.Since(time.Unix(0, lastDataAt.Load())); elapsed > dataIdle {
					// kill 归因——与「对端 1006/readIdle 读超时」日志可区分；
					// 判据=业务帧（杂帧流不续期 lastDataAt）。
					log.Printf("[%s] data watchdog: no business data frames for %v (>%v), closing to force reconnect", spec.Name, elapsed, dataIdle)
					st.dataKil.Store(true) // D-541 S3：僵尸杀归当前候选（连续达阈→冷却）
					_ = conn.Close()
					return
				}
				// 回主：兜底连接在服而早候选冷却已到期→断连重拨（主端恒优先）。
				if st.cur.Load() > 0 && st.earlierReady() {
					log.Printf("[%s] 早候选冷却到期——断开兜底连接回主", spec.Name)
					_ = conn.Close()
					return
				}
				if spec.OnAliveTick != nil {
					spec.OnAliveTick(sender)
				}
			case <-done:
				return
			}
		}
	})

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			// readIdle 到期=net timeout 显式标注（与 data watchdog/对端
			// close 帧三类死因日志可区分）。
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return fmt.Errorf("read idle >%v: %w", readIdle, err)
			}
			return fmt.Errorf("read: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(readIdle))
		// 帧≠业务数据——OnFrame 回报本帧是否业务产出帧，仅业务帧刷新
		// lastDataAt/sawData（杂帧遮死判径病灶根治）；nil=全帧计兼容锚。
		if spec.OnFrame == nil || spec.OnFrame(data) {
			lastDataAt.Store(time.Now().UnixNano())
			st.sawData.Store(true) // D-074 ①：业务帧到=通道活过（冷却循环断链证据）
		}
	}
}
