// Broker 管理器：运行时 MT4/MT5 连接的生命周期所有者（docs/broker管理器实现方案.md §3/§5）。
// 统一管理 DB 账号的适配器启停、报价订阅、执行器/对账注册，支持前端动态添加/删除。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"arb/internal/adapter"
	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/fact"
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
	swap      func(ctx context.Context) ([]fact.Fact, error) // 轮询 swap facts（§3.6，D-036）
}

// staticQuerier 环境变量适配器的 querier（不被动态管理，boot 时与 DB venue 去重）。
type staticQuerier struct {
	venue string
	q     reconcile.VenueQuerier
	swap  func(ctx context.Context) ([]fact.Fact, error) // nil = 无 swap 源
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

	commMu     sync.Mutex
	commission map[string]float64 // venue → 账号真实佣金（bps/腿，D-036）；缺省回退 config 默认

	// 冷启动退避（m.mu 保护）：任一 venue 映射符号缺报价 → 重试直到全部流入；
	// 超过 coldStartBound 仍缺 → 降级接受部分（防病态 symbol 永久阻塞整轮轮询）。
	coldStartSince time.Time
}

// NewBrokerManager 创建管理器。
func NewBrokerManager(store *pgstore.Store, qbus *bus.QuoteBus, reg *exec.Registry) *BrokerManager {
	return &BrokerManager{
		store:      store,
		qbus:       qbus,
		reg:        reg,
		running:    make(map[string]*running),
		commission: make(map[string]float64),
	}
}

// SetQuerierHook 注册 querier 集合变更回调（main 接 reconciler.SetQueriers）。
func (m *BrokerManager) SetQuerierHook(f func([]reconcile.VenueQuerier)) {
	m.onQ = f
}

// AddStaticQuerier 登记环境变量适配器的 querier。
func (m *BrokerManager) AddStaticQuerier(venue string, q reconcile.VenueQuerier) {
	m.addStatic(venue, q, nil)
}

// AddStaticSwapSource 为环境变量适配器登记 swap 采集（§3.6，不留静默缺口）。
func (m *BrokerManager) AddStaticSwapSource(venue string, swap func(ctx context.Context) ([]fact.Fact, error)) {
	m.addStatic(venue, nil, swap)
}

func (m *BrokerManager) addStatic(venue string, q reconcile.VenueQuerier, swap func(ctx context.Context) ([]fact.Fact, error)) {
	m.mu.Lock()
	m.static = append(m.static, staticQuerier{venue: venue, q: q, swap: swap})
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

// stopAdapter 停止运行中适配器并注销执行器/querier（不删 DB 记录，RemoveBroker/DisableBroker 复用）。
func (m *BrokerManager) stopAdapter(venue string) {
	m.mu.Lock()
	r := m.running[venue]
	delete(m.running, venue)
	m.mu.Unlock()

	if r != nil {
		r.stop()
		m.reg.Unregister(venue + ":mt4")
		m.reg.Unregister(venue + ":mt5")
		m.commMu.Lock()
		delete(m.commission, venue)
		m.commMu.Unlock()
		m.syncQueriers()
	}
}

// setCommission 登记账号真实佣金（nil 清除 → 回退 config 默认）。
func (m *BrokerManager) setCommission(venue string, bps *float64) {
	m.commMu.Lock()
	defer m.commMu.Unlock()
	if bps == nil {
		delete(m.commission, venue)
		return
	}
	m.commission[venue] = *bps
}

// SetCommission 运行时热更新账号佣金（D-036 SetBrokerCommission RPC 用）；nil 清除回退默认。
func (m *BrokerManager) SetCommission(venue string, bps *float64) {
	m.setCommission(venue, bps)
}

// CommissionBps 返回 venue 账号真实佣金（bps/腿）；ok=false 表示未配置，调用方回退 config 默认。
func (m *BrokerManager) CommissionBps(venue string) (float64, bool) {
	m.commMu.Lock()
	defer m.commMu.Unlock()
	bps, ok := m.commission[venue]
	return bps, ok
}

// SwapFacts 聚合全部运行中/静态适配器的 swap facts（§3.6）。
// 一个都拿不到时：DB 中还有应运行的账号（status=connected）→ 返回错误（Scheduler 退避重试直到适配器连上，
// 覆盖启动竞态：scheduler 首轮早于适配器连上，§7.4）；无 CFD 部署 / 全部移除·停用 → 返回空，按周期等待不制造噪声。
func (m *BrokerManager) SwapFacts(ctx context.Context) ([]fact.Fact, error) {
	m.mu.Lock()
	var sources int
	swapFns := make([]func(context.Context) ([]fact.Fact, error), 0, len(m.running)+len(m.static))
	for _, r := range m.running {
		if r.swap != nil {
			sources++
			swapFns = append(swapFns, r.swap)
		}
	}
	for _, s := range m.static {
		if s.swap != nil {
			sources++
			swapFns = append(swapFns, s.swap)
		}
	}
	m.mu.Unlock()

	if sources == 0 {
		if m.hasStartableAccounts(ctx) {
			return nil, fmt.Errorf("no swap sources (adapters connecting?)")
		}
		return nil, nil
	}

	var facts []fact.Fact
	var errs []string
	hasCold := false
	for _, fn := range swapFns {
		fs, err := fn(ctx)
		if err != nil {
			if errors.Is(err, adapter.ErrSwapColdStart) {
				hasCold = true
			}
			errs = append(errs, err.Error())
			continue
		}
		facts = append(facts, fs...)
	}
	m.mu.Lock()
	retryCold := false
	if hasCold {
		if m.coldStartSince.IsZero() {
			m.coldStartSince = time.Now()
		}
		retryCold = time.Since(m.coldStartSince) < coldStartBound
	} else {
		m.coldStartSince = time.Time{}
	}
	m.mu.Unlock()
	if hasCold && retryCold {
		return nil, fmt.Errorf("swap cold start: %s", strings.Join(errs, "; "))
	}
	if len(facts) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("swap aggregate: %s", strings.Join(errs, "; "))
	}
	for _, e := range errs {
		log.Printf("[broker] swap source: %v", e)
	}
	return facts, nil
}

// coldStartBound 冷启动退避窗口：期间任一 venue 缺报价即整轮重试；超窗接受部分，等下一轮补全。
const coldStartBound = 60 * time.Second

// hasStartableAccounts 是否还有应运行的 CFD 账号（status=connected，StartAll 的启动谓词）。
// 查询失败保守视为有——启动竞态（scheduler 首轮早于适配器连上）宁可退避重试，不可静默吞掉。
func (m *BrokerManager) hasStartableAccounts(ctx context.Context) bool {
	accounts, err := m.store.ListBrokerAccounts(ctx)
	if err != nil {
		log.Printf("[broker] hasStartableAccounts: %v", err)
		return true
	}
	for _, a := range accounts {
		if a.Status == "connected" {
			return true
		}
	}
	return false
}

// RemoveBroker 停止适配器并删除 DB 记录与符号映射。
func (m *BrokerManager) RemoveBroker(ctx context.Context, venue string) error {
	m.stopAdapter(venue)
	if err := m.store.DeleteVenueSymbolMappings(ctx, venue); err != nil {
		return fmt.Errorf("delete symbol mappings: %w", err)
	}
	if err := m.store.DeleteBrokerAccount(ctx, venue); err != nil {
		return fmt.Errorf("delete broker account: %w", err)
	}
	log.Printf("[broker] removed %s", venue)
	return nil
}

// DisableBroker 停用账号（D-033）：停止适配器 + status=stopped，保留 DB 行与符号映射（启用可恢复）。
func (m *BrokerManager) DisableBroker(ctx context.Context, venue string) error {
	m.stopAdapter(venue)
	if err := m.store.SetBrokerAccountStatus(ctx, venue, "stopped"); err != nil {
		return fmt.Errorf("disable broker: %w", err)
	}
	log.Printf("[broker] disabled %s", venue)
	return nil
}

// EnableBroker 启用账号：读取凭据重新启动适配器，status=connected（失败回写 failed）。
func (m *BrokerManager) EnableBroker(ctx context.Context, venue string) error {
	a, err := m.store.GetBrokerAccount(ctx, venue)
	if err != nil {
		return fmt.Errorf("get broker %s: %w", venue, err)
	}
	if err := m.startAdapter(ctx, a); err != nil {
		_ = m.store.SetBrokerAccountStatus(ctx, venue, "failed")
		return err
	}
	if err := m.store.SetBrokerAccountStatus(ctx, venue, "connected"); err != nil {
		return fmt.Errorf("enable broker: %w", err)
	}
	log.Printf("[broker] enabled %s", venue)
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
	m.setCommission(a.Venue, a.CommissionBps)
	if a.Platform == 0 {
		return m.startMT4(ctx, a)
	}
	return m.startMT5(ctx, a)
}

// startMT4 启动 MT4 适配器并接入 QuoteBus / 执行器 / 对账。
func (m *BrokerManager) startMT4(ctx context.Context, a *pgstore.BrokerAccount) error {
	ad := adapter.NewMT4Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
	if err := ad.Connect(ctx); err != nil {
		return fmt.Errorf("mt4 connect: %w", err)
	}
	fetchSymbols := func(ctx context.Context) ([]string, error) {
		resp, err := ad.MT4Client().Symbols(tokenCtx(ctx, ad.Token()), &mt4pb.SymbolsRequest{Id: ad.Token()})
		if err != nil {
			return nil, err
		}
		if e := resp.GetError(); e != nil {
			return nil, fmt.Errorf("mt4 symbols: %s", e.GetMessage())
		}
		return resp.GetResult(), nil
	}
	symMap, symbols, err := m.symbolPlan(ctx, a.Venue, defaultSymbols, fetchSymbols)
	if err != nil || len(symMap) == 0 {
		log.Printf("[broker] %s symbol plan: %v", a.Venue, err)
		symMap, symbols = identityMap(defaultSymbols)
	}
	ad.SetSymbolMap(symMap)

	ch, cancel := ad.Subscribe(symbols)
	go func() {
		for q := range ch {
			m.qbus.Publish(q)
		}
	}()
	m.reg.Register(exec.NewMT4Executor(a.Venue, ad.Token(), ad.TradingClient()))
	q := reconcile.NewMT4Querier(a.Venue, ad.Token(), ad.MT4Client())

	r := &running{
		stop:      func() { cancel(); ad.Stop() },
		querier:   q,
		symbols:   fetchSymbols,
		setSymMap: ad.SetSymbolMap,
		swap:      func(ctx context.Context) ([]fact.Fact, error) { return ad.SwapFacts(ctx, m.qbus) },
	}
	m.attach(a.Venue, r)
	log.Printf("[broker] %s (MT4) ready", a.Venue)
	return nil
}

// startMT5 启动 MT5 适配器。
func (m *BrokerManager) startMT5(ctx context.Context, a *pgstore.BrokerAccount) error {
	ad := adapter.NewMT5Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
	if err := ad.Connect(ctx); err != nil {
		return fmt.Errorf("mt5 connect: %w", err)
	}
	fetchSymbols := func(ctx context.Context) ([]string, error) {
		resp, err := ad.MT5Client().SymbolList(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolListRequest{Id: ad.Token()})
		if err != nil {
			return nil, err
		}
		if e := resp.GetError(); e != nil {
			return nil, fmt.Errorf("mt5 symbols: %s", e.GetMessage())
		}
		return resp.GetResult(), nil
	}
	symMap, symbols, err := m.symbolPlan(ctx, a.Venue, defaultSymbols, fetchSymbols)
	if err != nil || len(symMap) == 0 {
		log.Printf("[broker] %s symbol plan: %v", a.Venue, err)
		symMap, symbols = identityMap(defaultSymbols)
	}
	ad.SetSymbolMap(symMap)

	ch, cancel := ad.Subscribe(symbols)
	go func() {
		for q := range ch {
			m.qbus.Publish(q)
		}
	}()
	m.reg.Register(exec.NewMT5Executor(a.Venue, ad.Token(), ad.TradingClient()))
	q := reconcile.NewMT5Querier(a.Venue, ad.Token(), ad.MT5Client())

	r := &running{
		stop:      func() { cancel(); ad.Stop() },
		querier:   q,
		symbols:   fetchSymbols,
		setSymMap: ad.SetSymbolMap,
		swap:      func(ctx context.Context) ([]fact.Fact, error) { return ad.SwapFacts(ctx, m.qbus) },
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

// symbolPlan 决定订阅符号与映射：DB 有人工映射用 DB；否则按服务器实际符号解析默认表并落库
// （兼容 MT5/MT4 cent 账号 EURUSDm 后缀，首次添加即开箱订阅）。
func (m *BrokerManager) symbolPlan(ctx context.Context, venue string, defaults []string, fetchServer func(context.Context) ([]string, error)) (map[string]string, []string, error) {
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		return nil, nil, err
	}
	if len(mappings) > 0 {
		// 校验映射有效性：brokerSymbol 必须存在于服务器符号表，否则失效（如旧版 identity 默认表
		// 未匹配 cent 后缀）。失效项删除，全部失效则落到默认解析。
		server, err := fetchServer(ctx)
		if err != nil {
			return nil, nil, err
		}
		set := make(map[string]bool, len(server))
		for _, s := range server {
			set[s] = true
		}
		sm := make(map[string]string)
		keys := make([]string, 0, len(mappings))
		for _, mp := range mappings {
			if !set[mp.BrokerSymbol] {
				_ = m.store.DeleteSymbolMapping(ctx, mp.Venue, mp.BrokerSymbol)
				continue
			}
			sm[mp.BrokerSymbol] = mp.Canonical
			keys = append(keys, mp.BrokerSymbol)
		}
		if len(sm) > 0 {
			return sm, keys, nil
		}
	}
	server, err := fetchServer(ctx)
	if err != nil {
		return nil, nil, err
	}
	sm, syms := resolveDefaults(defaults, server)
	for k, v := range sm {
		_ = m.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{Venue: venue, BrokerSymbol: k, Canonical: v})
	}
	return sm, syms, nil
}

// resolveDefaults 在服务器符号里解析默认交易对：精确匹配优先，否则试 cent 后缀（EURUSDm）。
// 只订阅匹配到的；一个都对不上返回空映射。
func resolveDefaults(defaults, serverSymbols []string) (map[string]string, []string) {
	set := make(map[string]bool, len(serverSymbols))
	for _, s := range serverSymbols {
		set[s] = true
	}
	sm := make(map[string]string)
	var syms []string
	for _, c := range defaults {
		switch {
		case set[c]:
			sm[c] = c
			syms = append(syms, c)
		case set[c+"m"]:
			sm[c+"m"] = c
			syms = append(syms, c+"m")
		}
	}
	return sm, syms
}

// identityMap 保底 identity 映射（服务器符号解析失败时用，原样订阅默认符号）。
func identityMap(defaults []string) (map[string]string, []string) {
	sm := make(map[string]string, len(defaults))
	for _, s := range defaults {
		sm[s] = s
	}
	return sm, defaults
}

// tokenCtx 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func tokenCtx(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "id", token)
}
