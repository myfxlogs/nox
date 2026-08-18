// Broker 管理器：运行时 MT4/MT5 连接的生命周期所有者（docs/broker管理器实现方案.md §3/§5）。
// 统一管理 DB 账号的适配器启停、报价订阅、执行器/对账注册，支持前端动态添加/删除。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"sync"

	"arb/internal/adapter"
	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc/metadata"
)

// running 一个运行中适配器的可停止资源。
type running struct {
	stop      func() // 停止报价订阅 + 适配器连接
	querier   reconcile.VenueQuerier
	symbols   func(ctx context.Context) ([]string, error) // 拉取服务器符号表
	setSymMap func(map[string]string)                     // 运行时刷新 brokerSymbol → canonical
}

// staticQuerier 环境变量适配器的 querier（不被动态管理，boot 时与 DB venue 去重）。
type staticQuerier struct {
	venue string
	q     reconcile.VenueQuerier
}

// BrokerManager 管理 DB 中 broker_accounts 的动态适配器。
type BrokerManager struct {
	store *pgstore.Store
	qbus  *bus.QuoteBus
	reg   *exec.Registry

	mu      sync.Mutex
	running map[string]*running // venue → running
	static  []staticQuerier
	onQ     func([]reconcile.VenueQuerier) // querier 集合变更回调
}

// NewBrokerManager 创建管理器。
func NewBrokerManager(store *pgstore.Store, qbus *bus.QuoteBus, reg *exec.Registry) *BrokerManager {
	return &BrokerManager{
		store:   store,
		qbus:    qbus,
		reg:     reg,
		running: make(map[string]*running),
	}
}

// SetQuerierHook 注册 querier 集合变更回调（main 接 reconciler.SetQueriers）。
func (m *BrokerManager) SetQuerierHook(f func([]reconcile.VenueQuerier)) {
	m.onQ = f
}

// AddStaticQuerier 登记环境变量适配器的 querier。
func (m *BrokerManager) AddStaticQuerier(venue string, q reconcile.VenueQuerier) {
	m.mu.Lock()
	m.static = append(m.static, staticQuerier{venue: venue, q: q})
	m.mu.Unlock()
	m.syncQueriers()
}

// StartAll 启动 DB 中全部 status=connected 账号（boot 恢复）。
func (m *BrokerManager) StartAll(ctx context.Context) {
	accounts, err := m.store.ListBrokerAccounts(ctx)
	if err != nil {
		log.Printf("[broker] list accounts: %v", err)
		return
	}
	m.mu.Lock()
	staticVenues := make(map[string]bool, len(m.static))
	for _, s := range m.static {
		staticVenues[s.venue] = true
	}
	m.mu.Unlock()

	for _, a := range accounts {
		if a.Status != "connected" {
			continue
		}
		if staticVenues[a.Venue] {
			log.Printf("[broker] skip %s: env adapter occupies venue", a.Venue)
			continue
		}
		if err := m.startAdapter(ctx, &a); err != nil {
			log.Printf("[broker] start %s: %v", a.Venue, err)
		}
	}
}

// AddBroker 持久化账号并启动适配器。连接失败不落库成功状态。
func (m *BrokerManager) AddBroker(ctx context.Context, a *pgstore.BrokerAccount) error {
	a.Status = "connected"
	if err := m.store.InsertBrokerAccount(ctx, a); err != nil {
		return fmt.Errorf("insert broker: %w", err)
	}
	if err := m.startAdapter(ctx, a); err != nil {
		_ = m.store.SetBrokerAccountStatus(ctx, a.Venue, "failed")
		return err
	}
	return nil
}

// RemoveBroker 停止适配器并删除 DB 记录与符号映射。
func (m *BrokerManager) RemoveBroker(ctx context.Context, venue string) error {
	m.mu.Lock()
	r := m.running[venue]
	delete(m.running, venue)
	m.mu.Unlock()

	if r != nil {
		r.stop()
		m.reg.Unregister(venue + ":mt4")
		m.reg.Unregister(venue + ":mt5")
		m.syncQueriers()
	}
	if err := m.store.DeleteVenueSymbolMappings(ctx, venue); err != nil {
		return fmt.Errorf("delete symbol mappings: %w", err)
	}
	if err := m.store.DeleteBrokerAccount(ctx, venue); err != nil {
		return fmt.Errorf("delete broker account: %w", err)
	}
	log.Printf("[broker] removed %s", venue)
	return nil
}

// ReloadSymbolMap 重读 venue 映射并推送到运行中适配器（保存/删除映射后运行时生效）。
func (m *BrokerManager) ReloadSymbolMap(ctx context.Context, venue string) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.setSymMap == nil {
		return
	}
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		log.Printf("[broker] %s list mappings: %v", venue, err)
		return
	}
	sm := make(map[string]string, len(mappings))
	for _, mp := range mappings {
		sm[mp.BrokerSymbol] = mp.Canonical
	}
	r.setSymMap(sm)
	log.Printf("[broker] %s symbol map refreshed (%d)", venue, len(sm))
}

// BrokerSymbols 从运行中适配器拉取服务器符号表。
func (m *BrokerManager) BrokerSymbols(ctx context.Context, venue string) ([]string, error) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.symbols == nil {
		return nil, fmt.Errorf("venue %s 未连接", venue)
	}
	return r.symbols(ctx)
}

// startAdapter 平台分发：构造适配器 → 连接 → 订阅 → 注册执行器/querier。
func (m *BrokerManager) startAdapter(ctx context.Context, a *pgstore.BrokerAccount) error {
	if a.Platform == 0 {
		return m.startMT4(ctx, a)
	}
	return m.startMT5(ctx, a)
}

// startMT4 启动 MT4 适配器并接入 QuoteBus / 执行器 / 对账。
func (m *BrokerManager) startMT4(ctx context.Context, a *pgstore.BrokerAccount) error {
	ad := adapter.NewMT4Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
	symMap, symbols := m.loadSymbolMap(ctx, a.Venue, defaultSymbols)
	ad.SetSymbolMap(symMap)

	if err := ad.Connect(ctx); err != nil {
		return fmt.Errorf("mt4 connect: %w", err)
	}
	ch, cancel := ad.Subscribe(symbols)
	go func() {
		for q := range ch {
			m.qbus.Publish(q)
		}
	}()
	m.reg.Register(exec.NewMT4Executor(a.Venue, ad.Token(), ad.TradingClient()))
	q := reconcile.NewMT4Querier(a.Venue, ad.Token(), ad.MT4Client())

	r := &running{
		stop:    func() { cancel(); ad.Stop() },
		querier: q,
		symbols: func(ctx context.Context) ([]string, error) {
			resp, err := ad.MT4Client().Symbols(tokenCtx(ctx, ad.Token()), &mt4pb.SymbolsRequest{Id: ad.Token()})
			if err != nil {
				return nil, err
			}
			if e := resp.GetError(); e != nil {
				return nil, fmt.Errorf("mt4 symbols: %s", e.GetMessage())
			}
			return resp.GetResult(), nil
		},
		setSymMap: ad.SetSymbolMap,
	}
	m.attach(a.Venue, r)
	log.Printf("[broker] %s (MT4) ready", a.Venue)
	return nil
}

// startMT5 启动 MT5 适配器。
func (m *BrokerManager) startMT5(ctx context.Context, a *pgstore.BrokerAccount) error {
	ad := adapter.NewMT5Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
	symMap, symbols := m.loadSymbolMap(ctx, a.Venue, defaultSymbols)
	ad.SetSymbolMap(symMap)

	if err := ad.Connect(ctx); err != nil {
		return fmt.Errorf("mt5 connect: %w", err)
	}
	ch, cancel := ad.Subscribe(symbols)
	go func() {
		for q := range ch {
			m.qbus.Publish(q)
		}
	}()
	m.reg.Register(exec.NewMT5Executor(a.Venue, ad.Token(), ad.TradingClient()))
	q := reconcile.NewMT5Querier(a.Venue, ad.Token(), ad.MT5Client())

	r := &running{
		stop:    func() { cancel(); ad.Stop() },
		querier: q,
		symbols: func(ctx context.Context) ([]string, error) {
			resp, err := ad.MT5Client().SymbolList(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolListRequest{Id: ad.Token()})
			if err != nil {
				return nil, err
			}
			if e := resp.GetError(); e != nil {
				return nil, fmt.Errorf("mt5 symbols: %s", e.GetMessage())
			}
			return resp.GetResult(), nil
		},
		setSymMap: ad.SetSymbolMap,
	}
	m.attach(a.Venue, r)
	log.Printf("[broker] %s (MT5) ready", a.Venue)
	return nil
}

// attach 记录 running 并刷新 querier 集合。
func (m *BrokerManager) attach(venue string, r *running) {
	m.mu.Lock()
	m.running[venue] = r
	m.mu.Unlock()
	m.syncQueriers()
}

// syncQueriers 汇总 static + running querier 并触发回调（reconciler 刷新）。
func (m *BrokerManager) syncQueriers() {
	m.mu.Lock()
	qs := make([]reconcile.VenueQuerier, 0, len(m.static)+len(m.running))
	for _, s := range m.static {
		qs = append(qs, s.q)
	}
	for _, r := range m.running {
		qs = append(qs, r.querier)
	}
	onQ := m.onQ
	m.mu.Unlock()
	if onQ != nil {
		onQ(qs)
	}
}

// defaultSymbols 无映射时默认订阅（与 cmd/core brokerSymbols 默认一致）。
var defaultSymbols = []string{"EURUSD", "GBPUSD", "XAUUSD", "BTCUSD", "ETHUSD"}

// loadSymbolMap 读取 venue 符号映射；空则写回 identity 默认表。
func (m *BrokerManager) loadSymbolMap(ctx context.Context, venue string, defaults []string) (map[string]string, []string) {
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		log.Printf("[broker] %s list mappings: %v", venue, err)
	}
	if len(mappings) == 0 {
		sm := make(map[string]string, len(defaults))
		for _, s := range defaults {
			sm[s] = s
			_ = m.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{Venue: venue, BrokerSymbol: s, Canonical: s})
		}
		return sm, defaults
	}
	sm := make(map[string]string, len(mappings))
	keys := make([]string, 0, len(mappings))
	for _, mp := range mappings {
		sm[mp.BrokerSymbol] = mp.Canonical
		keys = append(keys, mp.BrokerSymbol)
	}
	return sm, keys
}

// tokenCtx 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func tokenCtx(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "id", token)
}
