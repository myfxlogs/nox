// dephealth.go —— D-579：上游依赖健康面（三所+mtapi 全平面「中断/恢复」可观测）。
// 被动记账注册表：不合成探针、不新增流量——全部上游交互成败事件经 Mark 喂入
// (venue:plane) 状态机；degraded↔healthy 双向变迁走 alertFn 告警道；
// Snapshot 并入 ListSourceHealth 透出（proto 零改）。
// 重启语义=内存态诚实复位 unknown（KB cron-monitor-state-persistence：重启后
// 仍劣化重警一次=正确噪音，勿持久化防假绿）。监控集=运行时自注册——key 只在
// 真实调用首次 Mark 时出现，禁预枚举（KB monitor-scope-from-persistent-residue）。
package dephealth

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 平面枚举：同 venue 多平面独立态。ws=WS 连接存活语义；rest=CEX HTTP；rpc=mtapi unary。
const (
	PlaneWS   = "ws"
	PlaneREST = "rest"
	PlaneRPC  = "rpc"
)

// 状态枚举（透出文案即存储值——前端 rows 泛化渲染）。
const (
	StateUnknown  = "unknown"
	StateHealthy  = "healthy"
	StateSuspect  = "suspect"  // 连败未达阈
	StateDegraded = "degraded" // 连败达阈——进 30s 确认窗，warn 待确认
	StateIdle     = "idle"     // 低频面 >idleWindow 无事件（无流量≠down）
)

const (
	// FailThreshold 连败达此数判 degraded（告警一次，态内不重警）。
	FailThreshold = 3
	// IdleWindow REST/RPC 低频面「无近期调用」透出窗。
	IdleWindow = 30 * time.Minute
	// DegradedConfirmWindow D-038①：degraded 确认窗——进入后满窗无成功才发 warn；
	// 窗内任一成功静默回 healthy（部署重启突发连败去抖，修复 warn/info 同秒误报对）。
	DegradedConfirmWindow = 30 * time.Second
	// BootSilenceWindow D-038③：Init 后静默窗——只记状态变迁不告警
	// （boot 时 SymbolParams×127/swap/sessions 并发突发撞 deadline 属已知噪音）。
	BootSilenceWindow = 60 * time.Second
)

// Row 单依赖平面快照行（ListSourceHealth 透出载体）。
type Row struct {
	Key           string // "venue[:venueType]:plane"（wsrunner 侧 "venue:ws-频道族"）
	State         string // unknown/healthy/suspect/degraded/idle
	LastEventUnix int64  // 末次成败事件时标
	LastOKUnix    int64
	LastErrUnix   int64
	LastErr       string // 末次错因摘要（透出可定位）
	ConsecFails   int
	DownSince     int64 // degraded 起点 unix（恢复告警算中断时长；0=未劣化）
}

type entry struct {
	state       string
	lastEventAt time.Time
	lastOKAt    time.Time
	lastErrAt   time.Time
	lastErr     string
	consecFails int
	downSince   time.Time
	// D-038①：warned=已确认告警（恢复才发 info）；degradeTimer=30s 确认窗定时器
	// （成功 Stop 之；到期经 warnCheck 判后 warn/重排——看门狗告警语义不携业务数据）。
	warned       bool
	degradeTimer *time.Timer
}

// AlertFn 变迁告警注入点（main 装配 alertCooler.Wrap2 包装的 InsertAlert）。
type AlertFn func(level, msg string)

var (
	mu      sync.Mutex
	reg     = map[string]*entry{}
	alertFn AlertFn
	// OnChange D-579-R3 F-3a：状态变迁扇出钩子（装配期一次性赋值；读点在 mu 内，
	// 调用在锁外——踢 posMon.OnHealthDirty 重组装 sourceHealth，防「态变了无事件」死踢）。
	OnChange func()
	// nowFn 时钟可注入（测试控时——T1 连败/恢复时序锚）。
	nowFn = time.Now
	// afterFunc 定时器可注入（测试手动触发确认窗回调）。
	afterFunc = time.AfterFunc
	// initAt D-038③：Init 时刻（静默窗起点；零值=未初始化→窗不生效）。
	initAt time.Time
)

// Init 装配告警出口（进程级一次；nil=静默只记账）。D-038③：记 initAt 为静默窗起点。
func Init(fn AlertFn) {
	mu.Lock()
	defer mu.Unlock()
	alertFn = fn
	initAt = nowFn()
}

// silentWindow 启动静默窗内=true（只记状态不告警）。
func silentWindow(now time.Time) bool {
	return !initAt.IsZero() && now.Sub(initAt) < BootSilenceWindow
}

// isMethodLevelErr D-038②：方法级错（未实现/参数错/资源缺）非面级故障——
// 记事件但不计连败不变迁（Unimplemented=网关未实现某 RPC，如 RequiredMargin；
// InvalidArgument/NotFound=单次调用参数/资源问题，与面连通性无关）。
func isMethodLevelErr(err error) bool {
	if err == nil {
		return false
	}
	// D-038 R1：生产 err 形态=grpc status（depUnaryInterceptor 透传 grpc client err），
	// 非 connect 协议错——connect.CodeOf 恒 Unknown 恒不命中=方法级豁免永不生效（R1 退回）。
	switch status.Code(err) {
	case codes.Unimplemented, codes.InvalidArgument, codes.NotFound:
		return true
	}
	return false
}

// warnCheck 确认窗到点判定（degradeTimer 回调）：仍 degraded 且未警 → warn；
// 启动静默窗内到期 → 重排到窗尾再判（持续劣化过窗仍该警，瞬态劣化已被成功撤销）。
func warnCheck(key string) {
	var fn AlertFn
	var msg string
	mu.Lock()
	if e := reg[key]; e != nil && e.state == StateDegraded && !e.warned {
		now := nowFn()
		if remain := initAt.Add(BootSilenceWindow).Sub(now); silentWindow(now) && remain > 0 {
			e.degradeTimer = afterFunc(remain, func() { warnCheck(key) })
		} else {
			e.warned = true
			msg = fmt.Sprintf("%s 面判定劣化：连败 %d 次且 ≥%s 无成功，末错=%s", key, e.consecFails, DegradedConfirmWindow, e.lastErr)
			fn = alertFn
		}
	}
	mu.Unlock()
	if fn != nil {
		fn("warn", msg)
	}
}

// Mark 喂入一次上游交互成败事实。key 空=不记（向后兼容未挂点路径）。
func Mark(key string, ok bool, err error) {
	if key == "" {
		return
	}
	var alertLevel, alertMsg string
	mu.Lock()
	e := reg[key]
	if e == nil {
		e = &entry{state: StateUnknown}
		reg[key] = e
	}
	now := nowFn()
	e.lastEventAt = now
	prev := e.state // F-3a：变迁前态——锁外比对决定是否扇出
	if ok {
		wasDegraded := e.state == StateDegraded
		e.state = StateHealthy
		e.consecFails = 0
		e.lastOKAt = now
		if wasDegraded {
			if e.degradeTimer != nil {
				e.degradeTimer.Stop()
				e.degradeTimer = nil
			}
			downFor := now.Sub(e.downSince).Truncate(time.Second)
			e.downSince = time.Time{}
			if e.warned {
				// 已确认告警的劣化 → 恢复发 info（中断时长自 downSince 起算）。
				e.warned = false
				alertLevel, alertMsg = "info", fmt.Sprintf("%s 面已恢复（中断 %s）", key, downFor)
			}
			// 未确认（确认窗内恢复/静默窗内）→ 零告警（D-038①③）
		}
	} else if isMethodLevelErr(err) {
		// D-038②：方法级错记事件不计连败不变迁。
		e.lastErrAt = now
		e.lastErr = err.Error()
	} else {
		e.consecFails++
		e.lastErrAt = now
		if err != nil {
			e.lastErr = err.Error()
		}
		switch e.state {
		case StateUnknown, StateHealthy:
			e.state = StateSuspect
		case StateSuspect:
			if e.consecFails >= FailThreshold {
				e.state = StateDegraded
				e.downSince = now
				e.warned = false
				e.degradeTimer = afterFunc(DegradedConfirmWindow, func() { warnCheck(key) })
			}
		case StateDegraded:
			// 继续败只计数不重警（变迁单发=天然去重）。
		}
	}
	fn := alertFn
	cb := OnChange
	changed := prev != e.state
	silent := silentWindow(now)
	mu.Unlock()
	if fn != nil && alertMsg != "" && !silent {
		fn(alertLevel, alertMsg)
	}
	if changed && cb != nil {
		cb() // 锁外调——钩子回读 Snapshot 不死锁；变迁才踢天然去重
	}
}

// Snapshot 全量行快照（排序稳定；>IdleWindow 无事件的非 degraded 项透出 idle）。
func Snapshot() []Row {
	mu.Lock()
	defer mu.Unlock()
	now := nowFn()
	rows := make([]Row, 0, len(reg))
	for k, e := range reg {
		st := e.state
		if st != StateDegraded && st != StateUnknown && now.Sub(e.lastEventAt) > IdleWindow {
			st = StateIdle
		}
		r := Row{Key: k, State: st, ConsecFails: e.consecFails, LastErr: e.lastErr}
		if !e.lastEventAt.IsZero() {
			r.LastEventUnix = e.lastEventAt.Unix()
		}
		if !e.lastOKAt.IsZero() {
			r.LastOKUnix = e.lastOKAt.Unix()
		}
		if !e.lastErrAt.IsZero() {
			r.LastErrUnix = e.lastErrAt.Unix()
		}
		if !e.downSince.IsZero() {
			r.DownSince = e.downSince.Unix()
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows
}

// resetForTest 清注册表+告警口+时钟/定时器/静默窗（包内测试隔离）。
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	reg = map[string]*entry{}
	alertFn = nil
	OnChange = nil
	initAt = time.Time{}
}
