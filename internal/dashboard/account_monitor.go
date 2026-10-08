// AccountMonitor 账户资产视图的单一真相源（D-053；D-571 包B 事件化）。
// 事件源三源汇入：REST 只挂事件点（boot 首读/kick 定向回读/手动 Refresh）、MT5 流帧
// 六字段 patch（ObserveStreamAccount）、CEX 私有流余额账本命中 patch（OnLedgerAcct）
// → dirty 尾沿合批 → 逐 venue case 16 AccountEvent 发射。推（StreamUpdates case 16）
// 与拉（ListAccounts/Snapshot 读 cur）同源；前端 keyed merge。显示链零定时器。
package dashboard

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/keydomain"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// acctVenueState 单个账户查询源的状态（在线 + 最近成功快照）。
type acctVenueState struct {
	online     bool // 生效在线态（streamKnown?streamOnline:restOnline）
	restOnline bool // REST 成败（流状态缺席时兜底，镜像 bpMon）
	acct       *reconcile.AccountInfo
	asOf       time.Time // 数据时点：REST=采到时刻 / 流帧=FrameTime / ledger=entry.At
}

// AccountMonitor 跟踪全部账户（CFD + CEX）资金并发射逐 venue 变更事件。
type AccountMonitor struct {
	bm      *BrokerManager             // CFD 账户查询器来源（每轮重取，venue 启停自动跟随）
	exchs   ExchangeStore              // CEX 交易所配置来源（D-054：每轮重取，后台增删即时生效）
	statics []reconcile.AccountQuerier // 附加查询器（测试注入 fake 用；生产为空）
	evTopic *bus.Topic[*v1.AccountEvent]

	mu     sync.RWMutex
	states map[string]acctVenueState // venueKey → 状态
	// D-571 包B：事件发射面（镜像 broker_position_events.go）。
	cur          map[string]*v1.AccountInfo // 已发射态（回放源+去重基准）
	dirty        map[string]struct{}        // 待发射 venue 集
	flushT       *time.Timer                // 尾沿合批
	streamKnown  map[string]bool            // venue 流状态已知（MT5 域；CEX 永不置位→restOnline 兜底）
	streamOnline map[string]bool            // 流权威在线态
	pendingKick  map[string]struct{}
	kickSig      chan struct{}
	// 热修 hotfix-loops（OOM 19:01 实证）：kick 防抖——帧 nil-acct 10s/键、账本事件 2s/键；
	// 无防抖时 REST 失败+acct 持续 nil → 每帧 kick → poll 风暴。
	frameKickAt  map[string]time.Time
	ledgerKickAt map[string]time.Time
	// CEX querier 缓存：exchange_configs 内容哈希未变则复用（原每 poll 全量重建+REST）。
	qCacheHash uint64
	qCache     []reconcile.AccountQuerier
	balLedger  *bus.BalanceLedger // D-571 包B：CEX 余额活账本（SetBalanceLedger 注入）
	// cooldown 账号 RPC 冷却（D-077 修复⑩ G2：连续超时 ≥2 → 10s 冷却跳过）。
	cooldown *Cooldown

	lastPoll time.Time // 防抖基准：最近一次全量 kick 触发 poll 的时间（仅全量 kick 路径更新）

	// D-445 S0：MT5 OnOrderUpdate 余额帧 tap（venueKey → 最近一帧；Refresh 失败兜底+加温）。
	streamAccts sync.Map // venueKey → StreamAcct

	// simWarned D-516：simulated=t 配置行 fail-closed warn-once（queriers 每轮询重跑防刷屏）。
	simWarned map[int64]bool

	// fundsHook D-568：资金变更通知口（MT5 流帧/poll changed 双源汇入，SetFundsHook 注入）。
	fundsHook func()
	// snapWriter D-132：REST-only CEX 面审计留痕搭车写口（SetSnapshotWriter 注入；
	// nil=未接线跳过）。WS 覆盖键不写（事件径已有），值不变不写（acctSame 去重）。
	snapWriter func(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error
}

// NewAccountMonitor 创建 monitor（D-571 包B：interval 参死净——ticker 已删，事件驱动）。
func NewAccountMonitor(bm *BrokerManager, evTopic *bus.Topic[*v1.AccountEvent]) *AccountMonitor {
	return &AccountMonitor{
		bm:           bm,
		evTopic:      evTopic,
		states:       make(map[string]acctVenueState),
		cur:          make(map[string]*v1.AccountInfo),
		dirty:        make(map[string]struct{}),
		streamKnown:  make(map[string]bool),
		streamOnline: make(map[string]bool),
		pendingKick:  make(map[string]struct{}),
		frameKickAt:  make(map[string]time.Time),
		ledgerKickAt: make(map[string]time.Time),
		cooldown:     NewCooldown(2, 10*time.Second), // D-077 修复⑩ G2
		kickSig:      make(chan struct{}, 1),         // cap1 非阻塞 send = 事件合并
	}
}

// SetExchangeStore 注入 CEX 交易所配置源（D-054，boot 时调用；此后 querier 随 DB 增删自动变化）。
func (m *AccountMonitor) SetExchangeStore(s ExchangeStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exchs = s
}

// AddStatic 注册附加账户查询器（测试注入 fake 用；生产 CEX 走 SetExchangeStore）。
func (m *AccountMonitor) AddStatic(q reconcile.AccountQuerier) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statics = append(m.statics, q)
}

// accountKickDebounce D-107 P2：全量 kick（""）防抖窗口——margin 事件洪峰防全场 REST
// （D-099 先例）；定向 kick（vk≠""）无防抖直读（镜像 bpMon reconcileVenueKeys 即时语义）。
var accountKickDebounce = 30 * time.Second // D-469a S5：事件化后 5min 防抖失去意义降 30s

// Kick 事件触发账户回读（D-107 P2 起，D-571 包B 改 venue 定向）。
// venueKey 完整键（"acct:mt5"/"binance:perp"）或裸 venue 名（"binance"→全部类型键）；
// 集合去重 + cap1 信号 = 事件合并。空串 = 全 venue（走防抖）。
func (m *AccountMonitor) Kick(venueKey string) {
	m.mu.Lock()
	m.pendingKick[venueKey] = struct{}{}
	m.mu.Unlock()
	select {
	case m.kickSig <- struct{}{}:
	default: // 已有待处理信号，合并丢弃
	}
}

// pollIfDue 全量 kick 防抖 poll：距上次全量 kick-poll <30s 直接返回；否则 poll 并更新基准。
// （D-571 包B 收窄：仅全量 kick 路径用；定向 kick 无防抖直读。）
func (m *AccountMonitor) pollIfDue(ctx context.Context) {
	m.mu.Lock()
	if time.Since(m.lastPoll) < accountKickDebounce {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	m.poll(ctx)

	m.mu.Lock()
	m.lastPoll = time.Now()
	m.mu.Unlock()
}

// Run 事件驱动主循环（D-571 包B：ticker 删净）。启动即 poll 一次=事件源重建（设计 §3.2 豁免），
// 此后只响应 kickSig：""=全量防抖 poll，vk=定向 pollVenueKeys 直读。
func (m *AccountMonitor) Run(ctx context.Context) {
	m.poll(ctx)
	m.mu.Lock()
	m.lastPoll = time.Now() // 初始 poll 后设防抖基准（防首 kick 绕过防抖）
	m.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.kickSig:
			for _, kick := range m.drainKicks() {
				if kick == "" {
					m.pollIfDue(ctx)
				} else {
					m.pollVenueKeys(ctx, kick)
				}
			}
		}
	}
}

// Refresh 同步执行一次轮询并返回当前视图。
func (m *AccountMonitor) Refresh(ctx context.Context) []*v1.AccountInfo {
	m.poll(ctx)
	m.flushBounded(ctx) // D-575：直调 flush 同包 20s 界
	return m.Snapshot()
}

// Snapshot 返回当前视图（ListAccounts 拉路径——cur 已发射态排序同源）。
func (m *AccountMonitor) Snapshot() []*v1.AccountInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*v1.AccountInfo, 0, len(m.cur))
	for _, k := range sortedKeys(m.cur) {
		out = append(out, m.cur[k])
	}
	return out
}

// EquityObserved D-032 ③：venue 权益 + 数据时点直读（供风控闸做超龄判定）。
// asOf=最后观测时点（流帧 FrameTime / REST 采到时刻 / 账本 entry.At），
// 区别于 Snapshot().UpdatedAt（发布口径=最后真变时刻，不能度量数据新鲜度）。
// ok=false=该 venue 无账户态（从未采到）；online 单独返回由调用方判。
func (m *AccountMonitor) EquityObserved(venueKey string) (equity float64, currency string, asOf time.Time, online bool, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, exists := m.states[venueKey]
	if !exists || st.acct == nil {
		return 0, "", time.Time{}, false, false
	}
	return st.acct.Equity, st.acct.Currency, st.asOf, st.online, true
}

// queriers 汇总查询器：CFD（brokerMgr 每轮重取）+ CEX（DB 每轮重取，D-054）+ statics（测试注入）。
// poll 全量轮询（boot 首读/全量 kick/Refresh）：逐查询器 → 落态 → dirty 尾沿发射。
// D-460 S1 两阶段拆分（D-447 遗留 wart 真修）：无锁段跑网络 RPC，锁内只做内存
// merge——旧实现持写锁跨逐 venue RPC 循环，Snapshot（ListAccounts 拉路径）全程排队。
func (m *AccountMonitor) poll(ctx context.Context) {
	m.pollImpl(ctx, nil)
	m.flushBounded(ctx)
}

// pollVenueKeys 定向回读（kick 键：完整 venueKey 直查；裸 venue 名=其全部类型键——
// 镜像 broker_position_events.go reconcileVenueKeys 匹配法）。无防抖直读。
func (m *AccountMonitor) pollVenueKeys(ctx context.Context, kick string) {
	m.pollImpl(ctx, func(vk string) bool {
		return vk == kick || keydomain.BareOf(vk) == kick
	})
	m.flushBounded(ctx)
}

// pollImpl 轮询骨架：match==nil=全量；否则仅命中键回读。RPC 在无锁段，merge 在锁内段。
func (m *AccountMonitor) pollImpl(ctx context.Context, match func(vk string) bool) {
	// queriers() 内部取 m.mu.RLock——必须先于写锁获取（RWMutex 非重入，持写锁再读锁会自锁）。
	qs := m.queriers(ctx)

	// 无锁段：纯网络 RPC。cooldown 自带互斥（Skip/Record 均线程安全），不依赖 m.mu。
	type pollRes struct {
		vk   string
		acct *reconcile.AccountInfo
		err  error
	}
	results := make([]pollRes, 0, len(qs))
	for _, q := range qs {
		if q == nil {
			continue
		}
		vk := q.VenueKey()
		if match != nil && !match(vk) {
			continue
		}
		// D-077 修复⑩ G2：RPC 冷却——连续超时 ≥2 → 10s 冷却跳过（archive #22 事故防御）。
		// D-362 S4③ 核证：cooldown/offline 状态以生效键（含 #id 后缀）为键——撞键双配置
		// 各自独立冷却/在线状态=正确语义（demo 超时不遮蔽 real）。
		if m.cooldown.Skip(vk) {
			continue
		}
		// D-575：querier 级 per-call 10s 界——venue RPC 挂死不再冻结 acctMon Run
		// （kick 饥饿）且 cooldown 靠超时返回才生效，永不返回=cooldown 永不触发。
		qctx, cancel := monitorCallCtx(ctx)
		acct, err := q.Account(qctx)
		cancel()
		results = append(results, pollRes{vk: vk, acct: acct, err: err})
	}
	for _, r := range results {
		m.applyAcctResult(ctx, r.vk, r.acct, r.err)
	}
}

// applyAcctResult REST 结果落态：err→restOnline=false（在线翻转则脏化，离线保留最近
// 成功快照 D-052）；ok→restOnline=true+acct 落态+dirty。cooldown Record 在锁内。
func (m *AccountMonitor) applyAcctResult(ctx context.Context, vk string, acct *reconcile.AccountInfo, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldown.Record(vk, err)
	st := m.states[vk]
	if err != nil {
		st.restOnline = false
		wasOnline := st.online
		st.online = m.onlineLocked(vk, st)
		if !wasOnline {
			// 从未在线：也记录一次（CEX 首次配置失败曾静默无日志，难排障）
			log.Printf("[account-mon] %s first-poll error: %v", vk, err)
			return
		}
		m.states[vk] = st
		log.Printf("[account-mon] %s offline: %v", vk, err)
		if !st.online {
			m.dirtyLocked(vk)
		}
		return
	}
	changed := !acctSame(st.acct, acct)
	st.restOnline = true
	prev := st.acct // D-136：快照去重比较基准（入库列口径，先于覆写捕获）
	cp := *acct     // 防共享指针原地改写：querier 侧复用/突变对象不回流本态
	st.acct = &cp
	st.asOf = time.Now()
	st.online = m.onlineLocked(vk, st)
	m.states[vk] = st
	m.dirtyLocked(vk)
	m.mu.Unlock()
	m.writeRestSnapshot(ctx, vk, prev, acct) // D-132/D-136：REST-only 面入库列值变→审计留痕
	if changed {
		m.notifyFunds() // D-568：REST 值变→funds 通知（值不变不触）
	}
	m.mu.Lock()
}

// accountToProto 已移至 account_monitor_proto.go（D-107 P2 拆分）；accountFingerprint 随
// D-571 包B 事件化退役删除（dirty 集即发射集，proto.Equal 在 flush 去重）。
