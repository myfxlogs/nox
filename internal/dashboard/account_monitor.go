// AccountMonitor 账户资产视图的单一真相源（D-053）。
// 轮询逐查询器取账户资金（CFD AccountSummary / CEX 签名余额）→ 跟踪在线状态（离线保留最近成功快照）
// → 变更时经 Topic 广播全量快照。推（StreamUpdates case 9）与拉（ListAccounts）同源；前端全量替换。
package dashboard

import (
	"context"
	"log"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// acctVenueState 单个账户查询源的状态（在线 + 最近成功快照）。
type acctVenueState struct {
	online bool
	acct   *reconcile.AccountInfo
}

// AccountMonitor 跟踪全部账户（CFD + CEX）资金并广播变更。
type AccountMonitor struct {
	bm       *BrokerManager             // CFD 账户查询器来源（每轮重取，venue 启停自动跟随）
	exchs    ExchangeStore              // CEX 交易所配置来源（D-054：每轮重取，后台增删即时生效）
	statics  []reconcile.AccountQuerier // 附加查询器（测试注入 fake 用；生产为空）
	interval time.Duration
	topic    *bus.Topic[*v1.AccountSnapshot]

	mu     sync.RWMutex
	states map[string]acctVenueState // venueKey → 状态
	cache  []*v1.AccountInfo         // 最近构建的视图（含离线快照）
	lastFP string                    // 上次广播指纹（变更检测）
	// cooldown 账号 RPC 冷却（D-077 修复⑩ G2：连续超时 ≥2 → 10s 冷却跳过）。
	cooldown *Cooldown
}

// ExchangeStore 提供 CEX 交易所配置（pgstore.Store 满足）。
type ExchangeStore interface {
	ListExchangeConfigs(ctx context.Context) ([]pgstore.ExchangeConfig, error)
}

// NewAccountMonitor 创建 monitor。interval 复用 ARB_BROKER_POS_POLL_MS（D-053 不新增 knob）。
func NewAccountMonitor(bm *BrokerManager, interval time.Duration, topic *bus.Topic[*v1.AccountSnapshot]) *AccountMonitor {
	return &AccountMonitor{
		bm:       bm,
		interval: interval,
		topic:    topic,
		states:   make(map[string]acctVenueState),
		cooldown: NewCooldown(2, 10*time.Second), // D-077 修复⑩ G2
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

// Run 轮询直至 ctx 取消。启动即轮询一次（首个快照立即可用）。
func (m *AccountMonitor) Run(ctx context.Context) {
	m.poll(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

// Refresh 同步执行一次轮询并返回当前视图。
func (m *AccountMonitor) Refresh(ctx context.Context) []*v1.AccountInfo {
	m.poll(ctx)
	return m.Snapshot()
}

// Snapshot 返回当前缓存视图（ListAccounts 拉路径）。
func (m *AccountMonitor) Snapshot() []*v1.AccountInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cache
}

// resolveExchangeBaseURL 推导 CEX 交易所 base（D-059 抽公共，账户查询与执行器共用）。
// 行为与原 queriers() 内联一致：base_url 显式 → 用之；空 → binance 默认 BINANCE_PROXY_URL
// （无则 fapi 直连，451 由部署负责）、okx 默认 www.okx.com；binance 代理 base 是 fapi 风格，
// 无 /api/ 段则 fapi→api 得 spot base（与采集一致）。
func resolveExchangeBaseURL(c pgstore.ExchangeConfig) string {
	base := c.BaseURL
	if base == "" {
		switch c.Venue {
		case "binance":
			base = os.Getenv("BINANCE_PROXY_URL")
			if base == "" {
				base = "https://fapi.binance.com" // 无代理直连；451 由部署负责
			}
		case "okx":
			base = "https://www.okx.com" // 实盘域名；模拟盘走 x-simulated-trading:1（D-054）
		}
	}
	if c.Venue == "binance" && c.VenueType == "spot" && !strings.Contains(base, "/api/") {
		// 与采集一致：仅 spot 需从 fapi 风格代理 base 推导 spot base。
		// binance:perp 不能 replace——其 base_url 形如 …/demofapi，含 "fapi" 子串，
		// naive replace 会错改成 demoapi（D-059 实施修正② 复核实测：模拟盘合约在
		// 独立子域 demo-fapi.binance.com，同 key 有效）。
		base = strings.Replace(base, "fapi", "api", 1)
	}
	return base
}

// queriers 汇总查询器：CFD（brokerMgr 每轮重取）+ CEX（DB 每轮重取，D-054）+ statics（测试注入）。
func (m *AccountMonitor) queriers(ctx context.Context) []reconcile.AccountQuerier {
	qs := m.bm.AccountQueriers()
	m.mu.RLock()
	exchs := m.exchs
	m.mu.RUnlock()
	if exchs != nil {
		cfgs, err := exchs.ListExchangeConfigs(ctx)
		if err != nil {
			log.Printf("[account-mon] list exchanges: %v", err)
		} else {
			for i := range cfgs {
				c := &cfgs[i]
				base := resolveExchangeBaseURL(*c)
				qs = append(qs, reconcile.NewCEXAccountQuerier(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated))
				log.Printf("[account] cex querier: %s:%s", c.Venue, c.VenueType)
			}
		}
	}
	m.mu.RLock()
	qs = append(qs, m.statics...)
	m.mu.RUnlock()
	return qs
}

// poll 一次轮询：逐查询器 → 更新状态 → 构建视图 → 变更则广播。
func (m *AccountMonitor) poll(ctx context.Context) {
	// queriers() 内部取 m.mu.RLock——必须先于写锁获取（RWMutex 非重入，持写锁再读锁会自锁）。
	qs := m.queriers(ctx)
	m.mu.Lock()
	for _, q := range qs {
		if q == nil {
			continue
		}
		vk := q.VenueKey()
		// D-077 修复⑩ G2：RPC 冷却——连续超时 ≥2 → 10s 冷却跳过（archive #22 事故防御）。
		if m.cooldown.Skip(vk) {
			continue
		}
		acct, err := q.Account(ctx)
		m.cooldown.Record(vk, err)
		st := m.states[vk]
		if err != nil {
			// 离线：保留最近成功快照（D-052 业主决策），不覆盖 acct。
			if !st.online {
				// 从未在线：也记录一次（CEX 首次配置失败曾静默无日志，难排障）
				log.Printf("[account-mon] %s first-poll error: %v", vk, err)
				continue
			}
			st.online = false
			m.states[vk] = st
			log.Printf("[account-mon] %s offline: %v", vk, err)
			continue
		}
		m.states[vk] = acctVenueState{online: true, acct: acct}
	}
	m.mu.Unlock()

	view := m.build()
	fp := accountFingerprint(view)

	m.mu.Lock()
	m.cache = view
	changed := fp != m.lastFP
	if changed {
		m.lastFP = fp
	}
	m.mu.Unlock()

	if changed {
		if m.topic != nil {
			m.topic.Publish(&v1.AccountSnapshot{Accounts: view})
		}
		log.Printf("[account-mon] snapshot changed: %d accounts", len(view))
	}
}

// build 按 venueKey 排序（确定性）构建全量视图。离线账户仍显示（最近快照），online=false 诚实标注。
func (m *AccountMonitor) build() []*v1.AccountInfo {
	m.mu.RLock()
	keys := make([]string, 0, len(m.states))
	for k := range m.states {
		keys = append(keys, k)
	}
	states := maps.Clone(m.states)
	m.mu.RUnlock()
	sort.Strings(keys)

	out := make([]*v1.AccountInfo, 0, len(keys))
	now := time.Now()
	for _, vk := range keys {
		st := states[vk]
		if st.acct == nil {
			continue
		}
		out = append(out, accountToProto(st.acct, st.online, now))
	}
	return out
}

// accountToProto 账户视图 → proto（D-053）。
func accountToProto(a *reconcile.AccountInfo, online bool, now time.Time) *v1.AccountInfo {
	pb := &v1.AccountInfo{
		VenueKey:   a.VenueKey,
		Venue:      a.Venue,
		VenueType:  a.VenueType,
		AssetType:  a.AssetType,
		Balance:    a.Balance,
		Equity:     a.Equity,
		Margin:     a.Margin,
		FreeMargin: a.FreeMargin,
		Credit:     a.Credit,
		Profit:     a.Profit,
		Leverage:   a.Leverage,
		Currency:   a.Currency,
		Online:     online,
		UpdatedAt:  timestamppb.New(now),
	}
	return pb
}

// accountFingerprint 视图指纹：任一账户的余额/净值/占用/可用/信用/浮盈/在线状态变化即触发广播。
// 权益实时跳变 → 值变了就推（5s 节奏，全量快照量小，可接受）。
func accountFingerprint(view []*v1.AccountInfo) string {
	var sb strings.Builder
	for _, a := range view {
		sb.WriteString(a.VenueKey)
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Balance, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Equity, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Margin, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.FreeMargin, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Credit, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatFloat(a.Profit, 'g', -1, 64))
		sb.WriteString("|")
		sb.WriteString(strconv.FormatBool(a.Online))
		sb.WriteString(";")
	}
	return sb.String()
}
