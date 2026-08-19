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

// SymbolGroup 服务器符号 + broker 分组 + 合约规格（MT5 GroupName 顶层段；MT4 降级空组/合约 0）。
type SymbolGroup struct {
	Symbol       string
	Group        string
	ContractSize float64 // 0 = 未知（MT4 / MT5 未返回）
}

// running 一个运行中适配器的可停止资源。
type running struct {
	stop         func()                               // 停止报价订阅 + 适配器连接
	querier      reconcile.VenueQuerier
	symbols      func(ctx context.Context) ([]string, error)      // 拉取服务器符号表
	symbolGroups func(ctx context.Context) ([]SymbolGroup, error) // 符号表 + 分组（仅服务 BrokerSymbols 展示，按需拉一次）
	// symbolParams 单符号权威合约规格（MT5 SymbolParams；MT4/未运行 = nil）。零信任：
	// 保存映射的合约校验数据只信 broker 自取，不信前端传值（D-042）。
	symbolParams func(ctx context.Context, symbol string) (float64, bool, error)
	setSymMap    func(map[string]string)                          // 运行时刷新 brokerSymbol → canonical
	resubscribe  func([]string)                                   // 运行时补订阅新符号（保存映射后生效，D-047 缺陷③）
	swap         func(ctx context.Context) ([]fact.Fact, error)   // 轮询 swap facts（§3.6，D-036）

	// per-venue symbolGroups TTL 缓存（D-045 §3.2.1）：避免矩阵页全量拉取时 N 账号 × SymbolList+SymbolParamsMany
	// 直连 mtapi；缓存失效回退直连，失败不下发脏数据。
	sgMu     sync.Mutex
	sgCache  []SymbolGroup
	sgCacheAt time.Time
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

	// symbolGroups TTL（D-045 §3.2.1）：由 config.SymbolsTTL 注入。
	symbolGroupsTTL time.Duration
}

// NewBrokerManager 创建管理器。symbolsTTL 由调用方从 config.SymbolsTTL 注入。
func NewBrokerManager(store *pgstore.Store, qbus *bus.QuoteBus, reg *exec.Registry, symbolsTTL time.Duration) *BrokerManager {
	return &BrokerManager{
		store:           store,
		qbus:            qbus,
		reg:             reg,
		running:         make(map[string]*running),
		commission:      make(map[string]float64),
		symbolGroupsTTL: symbolsTTL,
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

// BrokerSymbols 从运行中适配器拉取服务器符号表（附 broker 分组，辅助补映射搜索）。
// MT5 走 symbolGroups（SymbolParamsMany 取 GroupName 顶层段）；MT4/降级回退 symbols（group 空）。
func (m *BrokerManager) BrokerSymbols(ctx context.Context, venue string) ([]SymbolGroup, error) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.symbols == nil {
		return nil, fmt.Errorf("venue %s 未连接", venue)
	}
	if r.symbolGroups != nil {
		return m.cachedSymbolGroups(ctx, venue, r)
	}
	names, err := r.symbols(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolGroup, 0, len(names))
	for _, s := range names {
		out = append(out, SymbolGroup{Symbol: s})
	}
	return out, nil
}

// Resubscribe 运行时补订阅该 venue 的新符号（保存映射后调用）。
// D-047 缺陷③：映射了不重订阅 = 没报价流；mtapi 会话订阅幂等，已开报价流自动接上新符号。
func (m *BrokerManager) Resubscribe(venue string, symbols []string) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.resubscribe == nil || len(symbols) == 0 {
		return
	}
	r.resubscribe(symbols)
}

// AllSymbolGroups 返回全部 connected 账号的 symbolGroups（D-045 §3.2 装配用）。
// venue → SymbolGroup 列表；拉取失败的 venue 不出现在结果中（调用方通过 accounts 差集识别）。
func (m *BrokerManager) AllSymbolGroups(ctx context.Context) (map[string][]SymbolGroup, error) {
	m.mu.Lock()
	venues := make([]string, 0, len(m.running))
	for v, r := range m.running {
		if r.symbolGroups != nil {
			venues = append(venues, v)
		}
	}
	runningCopy := make(map[string]*running, len(m.running))
	for k, v := range m.running {
		runningCopy[k] = v
	}
	m.mu.Unlock()

	out := make(map[string][]SymbolGroup, len(venues))
	for _, v := range venues {
		r := runningCopy[v]
		groups, err := m.cachedSymbolGroups(ctx, v, r)
		if err != nil {
			log.Printf("[broker] %s symbolGroups for universe: %v", v, err)
			continue
		}
		out[v] = groups
	}
	return out, nil
}

// BrokerContractSize 返回该 venue 服务器符号的权威合约规格（D-042 零信任：校验数据只信 broker，
// 不信任前端传值）。MT4 / 未运行 / 查询失败 → known=false（合约未知，调用方跳过规格校验）。
func (m *BrokerManager) BrokerContractSize(ctx context.Context, venue, symbol string) (float64, bool, error) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.symbolParams == nil {
		return 0, false, nil
	}
	return r.symbolParams(ctx, symbol)
}

// backfillContracts 补写 venue 既有映射的 NULL 合约（数据源 = broker 权威分组信息）。
// D-042 前写入的历史映射 contract_size=NULL → 规格画像为空，合约校验形同虚设；
// 启动时回填恢复画像基线。未命中符号（如 MT4/停用）保持 NULL = 合约未知，合法跳过校验。
func (m *BrokerManager) backfillContracts(ctx context.Context, venue string, groups []SymbolGroup) {
	bySym := make(map[string]float64, len(groups))
	for _, g := range groups {
		if g.ContractSize > 0 {
			bySym[g.Symbol] = g.ContractSize
		}
	}
	if len(bySym) == 0 {
		return
	}
	if err := m.store.BackfillSymbolContracts(ctx, venue, bySym); err != nil {
		log.Printf("[broker] %s backfill contracts: %v", venue, err)
		return
	}
	log.Printf("[broker] %s contracts backfilled (%d symbols)", venue, len(bySym))
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
	// MT4 无 GroupName，降级 group 为空（前端仅模糊搜索，无分组 chips）；也无合约规格 → symbolParams=nil
	//（零信任：MT4 未知合约跳过校验，不做无依据的判断）。
	fetchSymbolGroups := func(ctx context.Context) ([]SymbolGroup, error) {
		names, err := fetchSymbols(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]SymbolGroup, 0, len(names))
		for _, s := range names {
			out = append(out, SymbolGroup{Symbol: s})
		}
		return out, nil
	}
	symMap, symbols, err := m.symbolPlan(ctx, a.Venue, fetchSymbolGroups)
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
		stop:         func() { cancel(); ad.Stop() },
		querier:      q,
		symbols:      fetchSymbols,
		symbolGroups: fetchSymbolGroups,
		setSymMap:    ad.SetSymbolMap,
		resubscribe:  ad.Resubscribe,
		swap:         func(ctx context.Context) ([]fact.Fact, error) { return ad.SwapFacts(ctx, m.qbus) },
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
	// 符号表 + 分组：SymbolParamsMany 批量取 SymGroup.GroupName，取反斜杠路径顶层段为分组
	// （Forex\Major\EURUSD→Forex）。Limit 不设——服务端默认 cap，设小 limit 反而在符号超限时抛异常
	// （归档 ListingsMany 经验，D-040）。
	fetchSymbolGroups := func(ctx context.Context) ([]SymbolGroup, error) {
		names, err := fetchSymbols(ctx)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			return nil, nil
		}
		resp, err := ad.MT5Client().SymbolParamsMany(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsManyRequest{Id: ad.Token(), Symbol: names})
		if err != nil {
			return nil, err
		}
		if e := resp.GetError(); e != nil {
			return nil, fmt.Errorf("mt5 symbolParamsMany: %s", e.GetMessage())
		}
		out := make([]SymbolGroup, 0, len(resp.GetResult()))
		for _, sp := range resp.GetResult() {
			if sp == nil {
				continue
			}
			// ContractSize 同批返回（D-042 合约规格校验数据源），0 = 未知。
			out = append(out, SymbolGroup{
				Symbol:       sp.GetSymbol(),
				Group:        topGroup(sp.GetSymbolGroup().GetGroupName()),
				ContractSize: sp.GetSymbolInfo().GetContractSize(),
			})
		}
		return out, nil
	}
	// 单符号权威合约规格（SymbolParams，零信任：保存映射时后端自取校验，不信前端传值）。
	// known=false = 合约未知（查询失败/无结果），调用方跳过规格校验。D-042。
	fetchContract := func(ctx context.Context, symbol string) (float64, bool, error) {
		resp, err := ad.MT5Client().SymbolParams(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsRequest{Id: ad.Token(), Symbol: symbol})
		if err != nil {
			return 0, false, err
		}
		if e := resp.GetError(); e != nil {
			return 0, false, fmt.Errorf("mt5 symbolParams %s: %s", symbol, e.GetMessage())
		}
		sp := resp.GetResult()
		if sp == nil || sp.GetSymbolInfo() == nil {
			return 0, false, nil
		}
		cs := sp.GetSymbolInfo().GetContractSize()
		return cs, cs > 0, nil
	}
	symMap, symbols, err := m.symbolPlan(ctx, a.Venue, fetchSymbolGroups)
	if err != nil || len(symMap) == 0 {
		log.Printf("[broker] %s symbol plan: %v", a.Venue, err)
		symMap, symbols = identityMap(defaultSymbols)
	}
	ad.SetSymbolMap(symMap)
	// 历史映射（D-042 前）无合约 → 启动时回填，恢复规格画像（零信任校验基线）。
	if gs, gerr := fetchSymbolGroups(ctx); gerr == nil {
		m.backfillContracts(ctx, a.Venue, gs)
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
		stop:         func() { cancel(); ad.Stop() },
		querier:      q,
		symbols:      fetchSymbols,
		symbolGroups: fetchSymbolGroups,
		symbolParams: fetchContract,
		setSymMap:    ad.SetSymbolMap,
		resubscribe:  ad.Resubscribe,
		swap:         func(ctx context.Context) ([]fact.Fact, error) { return ad.SwapFacts(ctx, m.qbus) },
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

// topGroup 取 SymGroup.GroupName 反斜杠路径的顶层段（Forex\Major\EURUSD → Forex）。
// 无分隔符原样返回。MT5 服务器品种分组数据源（归档 symbol_map_handlers.go topGroup 同法）。
// accountTypeGroupPrefix：部分 broker 把账号类型作为分组路径第一段（Exness 实测
// `Standard\Forex\EURUSDm` 全 356 符号第一段均 Standard），市场分组在第二段；
// 这些 token 一律跳过取下一段，否则分组被账号类型拍平（浏览器只剩一个"Standard"）。
var accountTypeGroupPrefix = map[string]bool{
	"standard": true, "cent": true, "micro": true, "demo": true, "real": true,
	"ecn": true, "fixed": true, "pro": true, "netting": true, "hedging": true, "instant": true,
}

// topGroup 取分组路径的"市场分组"段：跳过账号类型前缀（Standard/Cent/Micro…），
// 返回第一个非账号类型段；无分隔/全为账号类型 → 原样返回。
func topGroup(name string) string {
	parts := strings.Split(name, "\\")
	for _, p := range parts {
		if p == "" || accountTypeGroupPrefix[strings.ToLower(p)] {
			continue
		}
		return p
	}
	return name
}

// symbolPlan 决定订阅符号与映射（D-042 用户策展模型，canonical 数量用户自定）：
// 1. DB 已有该 venue 人工映射 → 校验 brokerSymbol 存在于服务器，失效项删除，返回。
// 2. 无映射 → 自动映射：既有 canonical（跨全部 venue 已映射）∪ 默认种子，名字+合约匹配者
//    落库并订阅（默认种子 = 首 broker 开箱 bootstrap；合约未知任一侧不阻塞）。
// 3. 全部不中 → identityMap 保底（原样订阅默认符号，个别不存在由订阅容错）。
func (m *BrokerManager) symbolPlan(ctx context.Context, venue string, fetchGroups func(context.Context) ([]SymbolGroup, error)) (map[string]string, []string, error) {
	groups, err := fetchGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		return nil, nil, err
	}
	if len(mappings) > 0 {
		// 校验映射有效性：brokerSymbol 必须存在于服务器符号表，否则失效（如旧版 identity 默认表
		// 未匹配 cent 后缀）。失效项删除，全部失效则落到自动映射。
		set := make(map[string]bool, len(groups))
		for _, g := range groups {
			set[g.Symbol] = true
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
	all, err := m.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	idx := buildCanonicalIdx(all, defaultSymbols)
	sm := make(map[string]string)
	var syms []string
	for _, sug := range classifySymbols(groups, idx) {
		if sug.Status != StatusAuto {
			continue
		}
		sm[sug.BrokerSymbol] = sug.Canonical
		syms = append(syms, sug.BrokerSymbol)
		var cs *float64
		if sug.ContractSize > 0 {
			v := sug.ContractSize
			cs = &v
		}
		_ = m.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
			Venue: venue, BrokerSymbol: sug.BrokerSymbol, Canonical: sug.Canonical, ContractSize: cs,
		})
	}
	if len(sm) > 0 {
		return sm, syms, nil
	}
	sm, syms = identityMap(defaultSymbols)
	return sm, syms, nil
}

// mappingStatus 建议映射分类（D-042 自动映射 + 建议共用判据）。
type mappingStatus string

const (
	StatusAuto     mappingStatus = "auto"     // 名字+合约匹配（或任一侧未知无法证伪）→ 自动落库 / 一键采纳
	StatusConflict mappingStatus = "conflict" // 名字匹配但合约不符——真实错配（ETF/股票撞原油）→ 待人工确认
)

// Suggestion 名字匹配既有 canonical 的候选映射。
type Suggestion struct {
	BrokerSymbol string
	Group        string
	ContractSize float64
	Canonical    string
	Status       mappingStatus
	Reason       string
}

// canonicalIdx canonical 索引：别名（canonical 名 ∪ 已映射 brokerSymbol）→ canonical；
// 期望合约 = 该 canonical 既有映射合约共识（无已知 = 0，无法校验）。
type canonicalIdx struct {
	aliases  map[string]string
	contract map[string]float64
}

// buildCanonicalIdx 从全部映射 + 默认种子构建索引。种子 canonical 自身名即别名（首 broker bootstrap）。
// 同一别名映射到不同 canonical 的异常数据：首见优先（校验后正常数据不出现）。
func buildCanonicalIdx(all []pgstore.SymbolMapping, seeds []string) canonicalIdx {
	idx := canonicalIdx{
		aliases:  make(map[string]string),
		contract: make(map[string]float64),
	}
	for _, c := range seeds {
		idx.aliases[c] = c
	}
	counts := make(map[string]map[float64]int)
	for _, m := range all {
		if _, dup := idx.aliases[m.BrokerSymbol]; !dup {
			idx.aliases[m.BrokerSymbol] = m.Canonical
		}
		if m.ContractSize != nil {
			if counts[m.Canonical] == nil {
				counts[m.Canonical] = make(map[float64]int)
			}
			counts[m.Canonical][*m.ContractSize]++
		}
	}
	for c, m := range counts {
		best, n := 0.0, 0
		for v, k := range m {
			if k > n {
				best, n = v, k
			}
		}
		idx.contract[c] = best
	}
	return idx
}

// classifySymbols 把服务器符号对照 canonical 索引分类。名字匹配（精确优先，否则 cent 后缀
// EURUSDm→EURUSD）；合约双方已知且不符 = conflict（真错配），其余（匹配或任一侧未知）= auto。
func classifySymbols(groups []SymbolGroup, idx canonicalIdx) []Suggestion {
	var out []Suggestion
	for _, g := range groups {
		canon, ok := idx.aliases[g.Symbol]
		if !ok {
			if c, found := idx.aliases[trimCentSuffix(g.Symbol)]; found {
				canon, ok = c, true
			}
		}
		if !ok {
			continue
		}
		sug := Suggestion{
			BrokerSymbol: g.Symbol, Group: g.Group, ContractSize: g.ContractSize,
			Canonical: canon, Status: StatusAuto,
		}
		if exp := idx.contract[canon]; exp > 0 && g.ContractSize > 0 && exp != g.ContractSize {
			sug.Status = StatusConflict
			sug.Reason = fmt.Sprintf("合约规格 %v 与 %s 期望 %v 不符，套利对比不可靠", g.ContractSize, canon, exp)
		}
		out = append(out, sug)
	}
	return out
}

// trimCentSuffix 去 MT5 cent 账号符号后缀（EURUSDm → EURUSD）。
func trimCentSuffix(s string) string {
	if strings.HasSuffix(s, "m") {
		return s[:len(s)-1]
	}
	return s
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

// cachedSymbolGroups 带 per-venue TTL 缓存的 symbolGroups（D-045 §3.2.1）。
// 缓存命中直接返回；过期回退直连，成功更新缓存，失败不下发脏数据（返回 error）。
func (m *BrokerManager) cachedSymbolGroups(ctx context.Context, venue string, r *running) ([]SymbolGroup, error) {
	ttl := m.symbolGroupsTTL
	if ttl <= 0 {
		return r.symbolGroups(ctx)
	}
	r.sgMu.Lock()
	if len(r.sgCache) > 0 && time.Since(r.sgCacheAt) < ttl {
		out := r.sgCache
		r.sgMu.Unlock()
		return out, nil
	}
	r.sgMu.Unlock()

	groups, err := r.symbolGroups(ctx)
	if err != nil {
		return nil, err
	}
	r.sgMu.Lock()
	r.sgCache = groups
	r.sgCacheAt = time.Now()
	r.sgMu.Unlock()
	return groups, nil
}
