// position_monitor.go —— D-571 包C：positions（case4 复活）/personalPositions（case18）/
// hedge（case17 聚合域单槽）/sourceHealth（case5 复活）行级事件发射器。
// 独立于 bpMon（数据源=DB 行+派生组装，非 venue 持仓查询器）：
//
//	kick 源=posChange 总线（positions 写点统一回调）+ bpMon evTopic（派生字段
//	floating_pnl/net 依赖 broker 利润匹配联动重算）+ hedgeTopic（TRX 写源）+
//	StreamStateSink 流状态扇出（health 域）。全部事件驱动，零定时器。
//
// 组装面与 RPC 同一条路径（assemblePositions/assemblePersonalPositions/
// assembleHedgeData/assembleSourceHealth——禁第二套组装）；逐 id proto.Equal diff，
// 变更行才发事件；boot 构建 latest 不立即广播（回放通道承担新连接水合）。
// 锁纪律同包A/B：锁内取快照→锁外构 proto→回锁比对写 latest→锁外发射。
// 事件路径一切外部调用 bounded（D-575：组装调用点包 monitorCallCtx 10s 界）。
package dashboard

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/proto"
)

// posFlushDelay 尾沿合批窗口（镜像 acctFlushDelay；测试可缩）。
var posFlushDelay = 200 * time.Millisecond

// PositionMonitor D-571 包C：positions/personal/hedge/sourceHealth 四域行级事件发射器。
type PositionMonitor struct {
	s        *Service
	evPos    *bus.Topic[*v1.Position]
	evPers   *bus.Topic[*v1.PersonalPositionEvent]
	evHedge  *bus.Topic[*v1.HedgeData]
	evHealth *bus.Topic[*v1.SourceHealth]

	mu           sync.RWMutex
	booted       bool
	latestPos    map[int64]*v1.Position
	latestPers   map[int64]*v1.PersonalPosition
	latestHedge  *v1.HedgeData
	latestHealth map[string]*v1.SourceHealth
	dPos         bool // positions 域脏（posChange 写点+bpMon 派生联动）
	dPers        bool // personalPositions 域脏（posChange 同源）
	dHedge       bool // hedge 聚合域脏（hedgeTopic）
	dHealth      bool // sourceHealth 域脏（流状态事件）
	flushT       *time.Timer
	probeT       *time.Timer // D-047③：探针到期唤醒（仅叫醒——决策=ExpireDueProbes 查 DB）
}

// NewPositionMonitor 创建四域发射器（topics 由 main 持有——回放与流订阅共享同实例）。
func NewPositionMonitor(s *Service, evPos *bus.Topic[*v1.Position], evPers *bus.Topic[*v1.PersonalPositionEvent], evHedge *bus.Topic[*v1.HedgeData], evHealth *bus.Topic[*v1.SourceHealth]) *PositionMonitor {
	return &PositionMonitor{
		s:            s,
		evPos:        evPos,
		evPers:       evPers,
		evHedge:      evHedge,
		evHealth:     evHealth,
		latestPos:    make(map[int64]*v1.Position),
		latestPers:   make(map[int64]*v1.PersonalPosition),
		latestHealth: make(map[string]*v1.SourceHealth),
	}
}

// Kick positions+personal 域脏化（posChange 写点回调——positions/personal 两表变更同源）。
func (m *PositionMonitor) Kick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dPos = true
	m.dPers = true
	m.dirtyLocked()
}

// KickPositions 仅 positions 域脏化（bpMon 帧 flush——派生列联动重算，行本体多半未变）。
func (m *PositionMonitor) KickPositions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dPos = true
	m.dirtyLocked()
}

// KickHedge hedge 聚合域脏化（hedgeTopic——TRX 写源变更）。
func (m *PositionMonitor) KickHedge() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dHedge = true
	m.dirtyLocked()
}

// KickPersonal 仅 personalPositions 域脏化（D-571C-R2 F-2：personal_monitor
// loadPositions 集合 diff 非空回推——personal_positions 表不经 posChange）。
func (m *PositionMonitor) KickPersonal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dPers = true
	m.dirtyLocked()
}

// OnStreamState 流状态事件（StreamStateSink 第三挂点）→ sourceHealth 域脏化。
func (m *PositionMonitor) OnStreamState(venueKey string, online bool) {
	m.OnHealthDirty()
}

// OnHealthDirty sourceHealth 域脏化（D-571C-R2 F-1：ws reconnectMeter 每次
// record 扇出——totalCount/lastWarn 行变化须重组装 diff 发射，否则死踢）。
func (m *PositionMonitor) OnHealthDirty() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dHealth = true
	m.dirtyLocked()
}

// dirtyLocked 挂尾沿 flush（mu 持锁前提；镜像 acctMon dirtyLocked——bound 调度时刻捕获）。
func (m *PositionMonitor) dirtyLocked() {
	if m.flushT == nil {
		bound := bpFlushBound // D-575：调度时刻同步取值
		m.flushT = time.AfterFunc(posFlushDelay, func() {
			ctx, cancel := context.WithTimeout(context.Background(), bound)
			defer cancel()
			m.flush(ctx)
		})
	}
}

// flush 尾沿合批：drain 四域脏标 → boot 首组装只填 latest 不广播（回放承担水合），
// 之后逐域重组装+proto.Equal diff→变更行发射。
func (m *PositionMonitor) flush(ctx context.Context) {
	m.mu.Lock()
	m.flushT = nil
	dPos, dPers, dHedge, dHealth := m.dPos, m.dPers, m.dHedge, m.dHealth
	m.dPos, m.dPers, m.dHedge, m.dHealth = false, false, false, false
	booted := m.booted
	m.mu.Unlock()

	if !booted {
		m.boot(ctx)
		m.refreshProbeWake(ctx) // D-047③：boot 臂存量探针表（锁外）
		return
	}
	if dPos {
		m.flushPositions(ctx)
		m.refreshProbeWake(ctx) // D-047③：仓变更后重排探针到期表
	}
	if dPers {
		m.flushPersonal(ctx)
	}
	if dHedge {
		m.flushHedge(ctx)
	}
	if dHealth {
		m.flushHealth(ctx)
	}
}

// boot 首次全量组装四域 latest（零广播——新连接水合由 streamInit 回放承担，
// 存量连接本就持有同一份 RPC 水合态）。
func (m *PositionMonitor) boot(ctx context.Context) {
	qctx, cancel := monitorCallCtx(ctx)
	pos, _, posErr := m.s.assemblePositions(qctx, "", 200, 0)
	pers, persErr := m.s.assemblePersonalPositions(qctx)
	hedge, hedgeErr := m.s.assembleHedgeData(qctx)
	cancel()
	health := m.s.assembleSourceHealth()
	if posErr != nil || persErr != nil || hedgeErr != nil {
		log.Printf("[posMon] boot assemble err pos=%v pers=%v hedge=%v（dirty 重脏化下轮补）", posErr, persErr, hedgeErr)
		m.mu.Lock()
		m.dPos, m.dPers, m.dHedge, m.dHealth = true, true, true, true
		m.dirtyLocked()
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pb := range pos {
		m.latestPos[pb.Id] = pb
	}
	for _, pb := range pers {
		m.latestPers[pb.Id] = pb
	}
	m.latestHedge = hedge
	for _, h := range health {
		m.latestHealth[healthKey(h)] = h
	}
	m.booted = true
}

// flushPositions positions 域重组装+逐 id proto.Equal diff→变更行 case4 发射。
func (m *PositionMonitor) flushPositions(ctx context.Context) {
	qctx, cancel := monitorCallCtx(ctx)
	rows, _, err := m.s.assemblePositions(qctx, "", 200, 0)
	cancel()
	if err != nil {
		log.Printf("[posMon] assemble positions: %v", err)
		return
	}
	var changed []*v1.Position
	m.mu.Lock()
	for _, pb := range rows {
		if old := m.latestPos[pb.Id]; old == nil || !proto.Equal(old, pb) {
			m.latestPos[pb.Id] = pb
			changed = append(changed, pb)
		}
	}
	m.mu.Unlock()
	if m.evPos != nil {
		for _, pb := range changed {
			m.evPos.Publish(pb)
		}
	}
}

// flushPersonal personalPositions 域同构（case18 逐行 upsert）。
func (m *PositionMonitor) flushPersonal(ctx context.Context) {
	qctx, cancel := monitorCallCtx(ctx)
	rows, err := m.s.assemblePersonalPositions(qctx)
	cancel()
	if err != nil {
		log.Printf("[posMon] assemble personal: %v", err)
		return
	}
	var changed []*v1.PersonalPosition
	m.mu.Lock()
	for _, pb := range rows {
		if old := m.latestPers[pb.Id]; old == nil || !proto.Equal(old, pb) {
			m.latestPers[pb.Id] = pb
			changed = append(changed, pb)
		}
	}
	m.mu.Unlock()
	if m.evPers != nil {
		for _, pb := range changed {
			m.evPers.Publish(&v1.PersonalPositionEvent{Upsert: pb})
		}
	}
}

// flushHedge hedge 聚合域单槽（case17：proto.Equal diff→整帧发射）。
func (m *PositionMonitor) flushHedge(ctx context.Context) {
	qctx, cancel := monitorCallCtx(ctx)
	hd, err := m.s.assembleHedgeData(qctx)
	cancel()
	if err != nil || hd == nil {
		if err != nil {
			log.Printf("[posMon] assemble hedge: %v", err)
		}
		return
	}
	m.mu.Lock()
	changed := m.latestHedge == nil || !proto.Equal(m.latestHedge, hd)
	if changed {
		m.latestHedge = hd
	}
	m.mu.Unlock()
	if changed && m.evHedge != nil {
		m.evHedge.Publish(hd)
	}
}

// flushHealth sourceHealth 域逐 source diff（case5 行级发射）。
func (m *PositionMonitor) flushHealth(ctx context.Context) {
	rows := m.s.assembleSourceHealth()
	var changed []*v1.SourceHealth
	m.mu.Lock()
	for _, h := range rows {
		k := healthKey(h)
		if old := m.latestHealth[k]; old == nil || !proto.Equal(old, h) {
			m.latestHealth[k] = h
			changed = append(changed, h)
		}
	}
	m.mu.Unlock()
	if m.evHealth != nil {
		for _, h := range changed {
			m.evHealth.Publish(h)
		}
	}
}
