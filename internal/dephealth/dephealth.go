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
	StateDegraded = "degraded" // 连败达阈——已告警
	StateIdle     = "idle"     // 低频面 >idleWindow 无事件（无流量≠down）
)

const (
	// FailThreshold 连败达此数判 degraded（告警一次，态内不重警）。
	FailThreshold = 3
	// IdleWindow REST/RPC 低频面「无近期调用」透出窗。
	IdleWindow = 30 * time.Minute
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
)

// Init 装配告警出口（进程级一次；nil=静默只记账）。
func Init(fn AlertFn) {
	mu.Lock()
	defer mu.Unlock()
	alertFn = fn
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
			downFor := now.Sub(e.downSince).Truncate(time.Second)
			e.downSince = time.Time{}
			alertLevel, alertMsg = "info", fmt.Sprintf("%s 面已恢复（中断 %s）", key, downFor)
		}
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
				alertLevel, alertMsg = "warn", fmt.Sprintf("%s 面判定劣化：连败 %d 次，末错=%s", key, e.consecFails, e.lastErr)
			}
		case StateDegraded:
			// 继续败只计数不重警（变迁单发=天然去重）。
		}
	}
	fn := alertFn
	cb := OnChange
	changed := prev != e.state
	mu.Unlock()
	if fn != nil && alertMsg != "" {
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

// resetForTest 清注册表+告警口（包内测试隔离）。
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	reg = map[string]*entry{}
	alertFn = nil
	OnChange = nil
}
