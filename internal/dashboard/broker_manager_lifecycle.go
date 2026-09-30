package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

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
		// D-209：boot 路径用 startWithRetry（指数退避），并行化避免慢/失败连接阻塞其他 venue。
		// AddBroker 手动路径保持一次性 startAdapter（需即时错误反馈）。
		a := a
		safego.Go("broker-retry:"+a.Venue, func() {
			m.startWithRetry(ctx, a.Venue, func() error { return m.startAdapter(ctx, &a) })
		})
	}
}

// AddBroker 持久化账号并启动适配器。连接失败不落库成功状态。
// D-142 ②：startAdapter 成功后异步触发 BackfillRuleMappings（免重启补 regex 规则映射）。
func (m *BrokerManager) AddBroker(ctx context.Context, a *pgstore.BrokerAccount) error {
	a.Status = "connected"
	if err := m.store.InsertBrokerAccount(ctx, a); err != nil {
		return fmt.Errorf("insert broker: %w", err)
	}
	if err := m.startAdapter(ctx, a); err != nil {
		// D-388 S0.2：回写失败不再吞（硬化 setter 而留吞错调用方=静默失败换层不换形态）
		if serr := m.store.SetBrokerAccountStatus(ctx, a.Venue, "failed"); serr != nil {
			log.Printf("[broker] mark %s failed: %v", a.Venue, serr)
		}
		return err
	}
	// D-142 ②：异步触发规则库补录（best-effort，失败只 log）；symbolPlan 自动映射 source='manual'，BackfillRuleMappings 补 regex 命中项 source='rule'。
	if m.RuleBackfill != nil {
		safego.Go("broker_manager:rule-backfill", func() {
			bctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			m.RuleBackfill(bctx, a.Venue)
		})
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
		m.reg.Unregister(keydomain.Join(venue, "mt5"))
		m.commMu.Lock()
		delete(m.commission, venue)
		m.commMu.Unlock()
		m.syncQueriers()
		m.syncHistQueriers()
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

// UpdateBrokerCredentials D-493 S4：凭据编辑（空串/0=不改动）。更新行后重启运行中适配器
// 使新凭据生效——connected/failed 复用 EnableBroker 通路（失败回写 failed）；stopped 不启动。
func (m *BrokerManager) UpdateBrokerCredentials(ctx context.Context, venue, host string, port int, server, password string) error {
	a, err := m.store.GetBrokerAccount(ctx, venue)
	if err != nil {
		return fmt.Errorf("get broker %s: %w", venue, err)
	}
	if err := m.store.UpdateBrokerCredentials(ctx, venue, host, port, server, password); err != nil {
		return fmt.Errorf("update broker credentials: %w", err)
	}
	m.stopAdapter(venue)
	if a.Status == "stopped" {
		return nil
	}
	return m.EnableBroker(ctx, venue)
}

// EnableBroker 启用账号：读取凭据重新启动适配器，status=connected（失败回写 failed）。
func (m *BrokerManager) EnableBroker(ctx context.Context, venue string) error {
	a, err := m.store.GetBrokerAccount(ctx, venue)
	if err != nil {
		return fmt.Errorf("get broker %s: %w", venue, err)
	}
	if err := m.startAdapter(ctx, a); err != nil {
		// D-388 S0.2：回写失败不再吞（同上）
		if serr := m.store.SetBrokerAccountStatus(ctx, venue, "failed"); serr != nil {
			log.Printf("[broker] mark %s failed: %v", venue, serr)
		}
		return err
	}
	if err := m.store.SetBrokerAccountStatus(ctx, venue, "connected"); err != nil {
		return fmt.Errorf("enable broker: %w", err)
	}
	log.Printf("[broker] enabled %s", venue)
	return nil
}
