package dashboard

import (
	"context"
	"fmt"
	"log"
	"nox/internal/data/keydomain"
	"time"

	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
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
		safego.GoPlain("broker-retry:"+a.Venue, func() {
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
		safego.GoPlain("broker_manager:rule-backfill", func() {
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
// D-150：删号级联——①open 真金仓在 venue 上拒绝删（孤儿仓位 fail-closed）；
// ②qbus 驱逐该 venue 内存报价（不驱逐则 mt-health CEX 兜底径收幽灵 venue 永久 crit）；
// ③采集/派生数据清（ticks/facts/equiv/universe/监控簿）+active 纸面退 venue_removed。
// D-151：③劈两半——派生小表同步清（账号行删除前清完，保正确性），ticks/facts 批量行
// 异步清（同步跑 RPC 卡 ~30s）；异步失败 warn 告警（残留=系统解决不了=推送域）。
// 保留：alerts/positions 等审计域。
func (m *BrokerManager) RemoveBroker(ctx context.Context, venue string) error {
	if n, err := m.store.CountVenueOpenPositions(ctx, venue); err != nil {
		return fmt.Errorf("open positions guard: %w", err)
	} else if n > 0 {
		return fmt.Errorf("venue %s 仍有 %d 个活跃真金仓，先平仓再删号", venue, n)
	}
	m.stopAdapter(venue)
	if m.qbus != nil {
		if evicted := m.qbus.EvictVenue(venue); evicted > 0 {
			log.Printf("[broker] %s qbus evicted %d quotes", venue, evicted)
		}
	}
	purged, err := m.store.PurgeVenueDerived(ctx, venue)
	if err != nil {
		return fmt.Errorf("purge venue data: %w", err)
	}
	if err := m.store.DeleteVenueSymbolMappings(ctx, venue); err != nil {
		return fmt.Errorf("delete symbol mappings: %w", err)
	}
	if err := m.store.DeleteBrokerAccount(ctx, venue); err != nil {
		return fmt.Errorf("delete broker account: %w", err)
	}
	log.Printf("[broker] removed %s (purged=%v)", venue, purged)
	m.purgeVenueBulkAsync(venue)
	return nil
}

// purgeVenueBulkAsync D-151：账号行已删后后台清 ticks/facts 批量观测行。
// 账号在窗内被重建 → 放弃（残行随新采集覆盖，强清会擦掉新数据）。
func (m *BrokerManager) purgeVenueBulkAsync(venue string) {
	safego.GoPlain("broker-purge:"+venue, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		exists, err := m.store.BrokerAccountExists(ctx, venue)
		if err == nil && exists {
			log.Printf("[broker] %s re-added during purge window, bulk purge skipped", venue)
			return
		}
		bulk, perr := m.store.PurgeVenueBulk(ctx, venue)
		if perr != nil {
			log.Printf("[broker] %s bulk purge failed: %v", venue, perr)
			if aerr := m.store.InsertAlert(ctx, nil, "warn",
				fmt.Sprintf("[broker] venue %s 删除后批量清理未完成（%v）——残留 ticks/facts 需人工核查", venue, perr)); aerr != nil {
				log.Printf("[broker] %s purge alert: %v", venue, aerr)
			}
			return
		}
		log.Printf("[broker] %s bulk purge done %v", venue, bulk)
	})
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

// D-164 R5 attach-pending 集：startMT5 入口置位、defer 返回清位（含 Connect 失败径）——
// no-match kick 据此区分竞态窗（延迟重排至 attach 完）与真未知 venue（立即 warn 丢）。
func (m *BrokerManager) setAttaching(venueKey string) {
	m.mu.Lock()
	m.attaching[venueKey] = struct{}{}
	m.mu.Unlock()
}

func (m *BrokerManager) clearAttaching(venueKey string) {
	m.mu.Lock()
	delete(m.attaching, venueKey)
	m.mu.Unlock()
}

// isAttaching kick 键的 attach 是否进行中。键双形态同 queriers() 匹配规：
// 完整键（"acct:mt5"）或 BareOf 命中。
func (m *BrokerManager) isAttaching(venueKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.attaching {
		if k == venueKey || keydomain.BareOf(k) == venueKey {
			return true
		}
	}
	return false
}
